package provenance

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// marshalRecord and unmarshalRecord are the single definition of how a record is
// written. Everything that serialises one goes through here, so the on-disk shape
// cannot drift between writers.
func marshalRecord(r Record) ([]byte, error) { return json.Marshal(r) }

func unmarshalRecord(b []byte) (Record, error) {
	var r Record
	err := json.Unmarshal(b, &r)
	return r, err
}

// CorpusWriter writes provenance records as JSONL.
//
// JSONL rather than Parquet for now, deliberately: Parquet is the right final
// format and is on the roadmap, but it brings a schema-definition dependency and
// a column-type mapping to get wrong, and neither is worth blocking provenance on.
// A JSONL file loads into `datasets`, DuckDB, and Polars today, and converting it
// later is a read-and-rewrite rather than a re-crawl.
//
// Streaming, not buffered: a corpus is the largest thing this program produces,
// and holding it in memory to get an ordered file would trade a bounded crawl for
// a tidy one. Callers who need a total order have storage.SortItems for the
// extracted items; the corpus is keyed by content hash, which is order-independent
// by construction.
type CorpusWriter struct {
	mu    sync.Mutex
	f     *os.File
	w     *bufio.Writer
	path  string
	count int64

	// skipped counts records refused for being incomplete, so the summary can say
	// so rather than the file quietly being short.
	skipped int64
}

// NewCorpusWriter creates or truncates a corpus file at path.
func NewCorpusWriter(path string) (*CorpusWriter, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("provenance: create corpus dir: %w", err)
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("provenance: create corpus: %w", err)
	}
	return &CorpusWriter{f: f, w: bufio.NewWriter(f), path: path}, nil
}

// Write appends a record.
//
// An incomplete record is skipped rather than written. It could not answer the
// question the corpus exists to answer, and shipping it alongside ones that can is
// how a dataset's guarantees get quietly averaged down — better to be one row
// short and say so.
func (c *CorpusWriter) Write(r Record) error {
	if !r.Complete() {
		c.mu.Lock()
		c.skipped++
		c.mu.Unlock()
		return nil
	}

	if r.SchemaVersion == 0 {
		r.SchemaVersion = SchemaVersion
	}

	line, err := marshalRecord(r)
	if err != nil {
		return fmt.Errorf("provenance: encode record for %s: %w", r.URL, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, err := c.w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("provenance: write record: %w", err)
	}
	c.count++
	return nil
}

// Stats reports what the writer has accepted and refused.
func (c *CorpusWriter) Stats() (written, skipped int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count, c.skipped
}

// Path returns the file being written.
func (c *CorpusWriter) Path() string { return c.path }

// Close flushes and closes the corpus file.
func (c *CorpusWriter) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.f == nil {
		return nil
	}
	f := c.f
	c.f = nil

	if err := c.w.Flush(); err != nil {
		f.Close()
		return fmt.Errorf("provenance: flush corpus: %w", err)
	}
	return f.Close()
}

// ReadCorpus loads every record from a corpus file.
//
// Diagnostic and test helper: it holds the whole file in memory, which is exactly
// what the writer refuses to do. Anything processing a real corpus should stream.
func ReadCorpus(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("provenance: open corpus: %w", err)
	}
	defer f.Close()

	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		r, err := unmarshalRecord(line)
		if err != nil {
			// A truncated final line is what an interrupted run looks like. Stop
			// there rather than failing: the records before it are intact.
			break
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("provenance: scan corpus: %w", err)
	}
	return out, nil
}

// Summary describes a corpus for a report.
type Summary struct {
	Records int `json:"records"`

	// Restrictive is how many records came from a source that asked to be left out
	// of AI training. Reported rather than acted on: the number is the point, and
	// a corpus that had silently dropped them would show zero here and look clean.
	Restrictive int `json:"restrictive"`

	// RobotsDisallowed should be zero for any crawl that respected robots.txt.
	// Counted anyway, because a non-zero value is the loudest possible signal that
	// something is wrong, and it costs one comparison to find out.
	RobotsDisallowed int `json:"robots_disallowed"`

	Licensed   int            `json:"licensed"`
	Licences   map[string]int `json:"licences,omitempty"`
	MIMETypes  map[string]int `json:"mime_types,omitempty"`
	Languages  map[string]int `json:"languages,omitempty"`
	AISiteWide int            `json:"ai_blocked_sites"`
}

// Summarise counts a corpus.
func Summarise(records []Record) Summary {
	s := Summary{
		Licences:  map[string]int{},
		MIMETypes: map[string]int{},
		Languages: map[string]int{},
	}

	for _, r := range records {
		s.Records++
		if r.Restrictive() {
			s.Restrictive++
		}
		if !r.RobotsAllowed {
			s.RobotsDisallowed++
		}
		if r.Signals.Licence != "" {
			s.Licensed++
			s.Licences[r.Signals.Licence]++
		}
		if r.MIMEType != "" {
			s.MIMETypes[r.MIMEType]++
		}
		if r.Language != "" {
			s.Languages[r.Language]++
		}
		if r.AIDirectives != nil && len(r.AIDirectives.AgentsBlocked) > 0 {
			s.AISiteWide++
		}
	}
	return s
}
