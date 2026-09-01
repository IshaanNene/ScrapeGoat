package provenance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// AssertionWriter writes assertions as JSONL.
//
// A separate stream from the records rather than a repeated field inside them.
// A corpus is two things — the pages observed and the claims made about them — and
// they have different cardinalities: one page yields anywhere from zero claims to
// hundreds. Nesting the claims inside the page row would make the record file's
// width depend on how many selectors the operator happened to configure, and the
// Parquet projection in this package is deliberately flat because some readers
// handle nested repeated groups badly. Two flat tables joined on the content hash
// is the shape DuckDB, Polars and `datasets` all want anyway.
type AssertionWriter struct {
	mu    sync.Mutex
	f     *os.File
	w     *bufio.Writer
	path  string
	count int64

	// skipped counts assertions refused for having no observation to attach to.
	skipped int64
}

// NewAssertionWriter creates or truncates an assertion file at path.
func NewAssertionWriter(path string) (*AssertionWriter, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("provenance: create assertion dir: %w", err)
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("provenance: create assertion file: %w", err)
	}
	return &AssertionWriter{f: f, w: bufio.NewWriter(f), path: path}, nil
}

// Write appends one assertion.
//
// An assertion whose evidence names no observation is refused. It would be a claim
// with no stated source in a file whose entire purpose is that claims have stated
// sources, and it could not be joined back to the record that explains where it
// came from.
func (a *AssertionWriter) Write(as Assertion) error {
	if as.Evidence.ObservationHash == "" || as.Field == "" {
		a.mu.Lock()
		a.skipped++
		a.mu.Unlock()
		return nil
	}

	if as.SchemaVersion == 0 {
		as.SchemaVersion = SchemaVersion
	}

	line, err := json.Marshal(as)
	if err != nil {
		return fmt.Errorf("provenance: encode assertion %s: %w", as.Field, err)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, err := a.w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("provenance: write assertion: %w", err)
	}
	a.count++
	return nil
}

// Stats reports what the writer has accepted and refused.
func (a *AssertionWriter) Stats() (written, skipped int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.count, a.skipped
}

// Path returns the file being written.
func (a *AssertionWriter) Path() string { return a.path }

// Close flushes and closes the file.
func (a *AssertionWriter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.f == nil {
		return nil
	}
	f := a.f
	a.f = nil

	if err := a.w.Flush(); err != nil {
		f.Close()
		return fmt.Errorf("provenance: flush assertions: %w", err)
	}
	return f.Close()
}

// ReadAssertions loads every assertion from a file.
//
// Diagnostic and test helper, like ReadCorpus: it holds the whole file in memory,
// which is what the writer exists to avoid.
func ReadAssertions(path string) ([]Assertion, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("provenance: open assertions: %w", err)
	}
	defer f.Close()

	var out []Assertion
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var a Assertion
		if err := json.Unmarshal([]byte(line), &a); err != nil {
			return nil, fmt.Errorf("provenance: decode assertion: %w", err)
		}
		out = append(out, a)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("provenance: read assertions: %w", err)
	}
	return out, nil
}

// AssertionPathFor derives the assertion file's path from the corpus file's.
//
// Alongside the corpus rather than behind its own flag: the two files are one
// artifact in two tables, and an operator who has one and not the other has half a
// corpus. Deriving the name means they cannot be separated by forgetting a flag.
func AssertionPathFor(corpusPath string) string {
	ext := filepath.Ext(corpusPath)
	base := strings.TrimSuffix(corpusPath, ext)
	// Always JSONL, even next to a Parquet corpus: the Parquet projection here is
	// hand-written per column and assertions carry an `any` value, which has no
	// single column type. Converting later is a read-and-rewrite.
	return base + ".assertions.jsonl"
}

// AssertionSink is what the engine writes derived claims to.
//
// An interface so that a crawl can send assertions somewhere other than a file —
// a test collecting them in memory, a future writer batching into Parquet —
// without the engine knowing which.
type AssertionSink interface {
	Write(Assertion) error
	Stats() (written, skipped int64)
	Path() string
	Close() error
}

var _ AssertionSink = (*AssertionWriter)(nil)
