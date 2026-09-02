package provenance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

// parquetRecord is Record projected onto flat columns.
//
// Deliberately not the same shape as Record. The JSON form nests signals and AI
// directives, which reads well as a document; a corpus is read by DuckDB, Polars,
// and `datasets`, where `WHERE noai` beats `WHERE signals.noai` and where some
// readers handle nested groups poorly enough to be worth avoiding. So the JSON
// schema stays canonical and this is a projection of it, with the mapping in one
// place rather than spread across whatever tool loads the file.
//
// The repeated string columns are the exception: a list is a list, and flattening
// those would mean either a delimiter to get wrong or a column per agent.
type parquetRecord struct {
	SchemaVersion int32 `parquet:"schema_version"`

	URL          string `parquet:"url,zstd"`
	CanonicalURL string `parquet:"canonical_url,optional,zstd"`
	ContentHash  string `parquet:"content_hash,zstd"`

	FetchedAt  time.Time `parquet:"fetched_at,timestamp(millisecond)"`
	StatusCode int32     `parquet:"status_code"`
	MIMEType   string    `parquet:"mime_type,optional,dict,zstd"`
	FinalURL   string    `parquet:"final_url,optional,zstd"`

	// dict encoding: one crawl uses a handful of identities across millions of
	// rows, so the dictionary is tiny and the column nearly free.
	CrawlerIdentity string `parquet:"crawler_identity,optional,dict,zstd"`

	// Cache validators, verbatim. Not dict-encoded: an ETag is close to unique per
	// page, so a dictionary would hold one entry per row and cost more than it
	// saves. Last-Modified repeats across a statically generated site, but not
	// reliably enough to be worth a different encoding from its neighbour.
	ETag         string `parquet:"etag,optional,zstd"`
	LastModified string `parquet:"last_modified,optional,zstd"`

	Text     string `parquet:"text,optional,zstd"`
	Title    string `parquet:"title,optional,zstd"`
	Language string `parquet:"language,optional,dict,zstd"`

	ExtractionConfidence float64 `parquet:"extraction_confidence"`

	RobotsAllowed bool `parquet:"robots_allowed"`

	// RobotsPresent is optional so that "no robots.txt was seen" stays distinct
	// from "a robots.txt was seen and imposed nothing" — the same distinction the
	// JSON form keeps by omitting ai_directives entirely.
	RobotsPresent *bool `parquet:"robots_present,optional"`

	AIAgentsAddressed []string `parquet:"ai_agents_addressed,list,optional,zstd"`
	AIAgentsBlocked   []string `parquet:"ai_agents_blocked,list,optional,zstd"`
	AIVendorsBlocked  []string `parquet:"ai_vendors_blocked,list,optional,zstd"`

	NoIndex   bool `parquet:"noindex"`
	NoFollow  bool `parquet:"nofollow"`
	NoAI      bool `parquet:"noai"`
	NoImageAI bool `parquet:"noimageai"`

	// Optional, and that is the whole point: a page that said nothing about text
	// and data mining must not arrive in the corpus as a 0. Silence and permission
	// are different answers, and a non-nullable column would erase the difference
	// on the way to disk — the one place it would be hardest to notice.
	TDMReservation *int32 `parquet:"tdm_reservation,optional"`
	TDMPolicy      string `parquet:"tdm_policy,optional,zstd"`

	Licence       string `parquet:"licence,optional,dict,zstd"`
	LicenceSource string `parquet:"licence_source,optional,dict"`

	// Restrictive is derived, not stored input. Written anyway because the whole
	// point of the corpus is that someone filters on it, and requiring every
	// consumer to re-derive the rule invites each of them to get it slightly
	// different.
	Restrictive bool `parquet:"restrictive"`

	CrawlID string `parquet:"crawl_id,optional,dict,zstd"`
}

func toParquet(r Record) parquetRecord {
	// The three narrowing conversions below are bounded by construction:
	// SchemaVersion is a compile-time constant, StatusCode comes from net/http and
	// is three digits, and TDMReservation is validated to 0 or 1 before it is ever
	// stored. Overflow would need one of those invariants to break first.
	p := parquetRecord{
		SchemaVersion:        int32(r.SchemaVersion), // #nosec G115 -- a package constant
		URL:                  r.URL,
		CanonicalURL:         r.CanonicalURL,
		ContentHash:          r.ContentHash,
		FetchedAt:            r.FetchedAt,
		StatusCode:           int32(r.StatusCode), // #nosec G115 -- an HTTP status
		MIMEType:             r.MIMEType,
		FinalURL:             r.FinalURL,
		CrawlerIdentity:      r.CrawlerIdentity,
		ETag:                 r.ETag,
		LastModified:         r.LastModified,
		Text:                 r.Text,
		Title:                r.Title,
		Language:             r.Language,
		ExtractionConfidence: r.ExtractionConfidence,
		RobotsAllowed:        r.RobotsAllowed,
		NoIndex:              r.Signals.NoIndex,
		NoFollow:             r.Signals.NoFollow,
		NoAI:                 r.Signals.NoAI,
		NoImageAI:            r.Signals.NoImageAI,
		TDMPolicy:            r.Signals.TDMPolicy,
		Licence:              r.Signals.Licence,
		LicenceSource:        r.Signals.LicenceSource,
		Restrictive:          r.Restrictive(),
		CrawlID:              r.CrawlID,
	}

	if r.Signals.TDMReservation != nil {
		v := int32(*r.Signals.TDMReservation) // #nosec G115 -- validated to 0 or 1
		p.TDMReservation = &v
	}

	if r.AIDirectives != nil {
		present := r.AIDirectives.RobotsPresent
		p.RobotsPresent = &present
		p.AIAgentsAddressed = r.AIDirectives.AgentsAddressed
		p.AIAgentsBlocked = r.AIDirectives.AgentsBlocked
		p.AIVendorsBlocked = r.AIDirectives.VendorsBlocked
	}

	return p
}

func fromParquet(p parquetRecord) Record {
	r := Record{
		SchemaVersion:        int(p.SchemaVersion),
		URL:                  p.URL,
		CanonicalURL:         p.CanonicalURL,
		ContentHash:          p.ContentHash,
		FetchedAt:            p.FetchedAt,
		StatusCode:           int(p.StatusCode),
		MIMEType:             p.MIMEType,
		FinalURL:             p.FinalURL,
		CrawlerIdentity:      p.CrawlerIdentity,
		ETag:                 p.ETag,
		LastModified:         p.LastModified,
		Text:                 p.Text,
		Title:                p.Title,
		Language:             p.Language,
		ExtractionConfidence: p.ExtractionConfidence,
		RobotsAllowed:        p.RobotsAllowed,
		CrawlID:              p.CrawlID,
		Signals: PageSignals{
			NoIndex:       p.NoIndex,
			NoFollow:      p.NoFollow,
			NoAI:          p.NoAI,
			NoImageAI:     p.NoImageAI,
			TDMPolicy:     p.TDMPolicy,
			Licence:       p.Licence,
			LicenceSource: p.LicenceSource,
			Canonical:     p.CanonicalURL,
		},
	}

	if p.TDMReservation != nil {
		v := int(*p.TDMReservation)
		r.Signals.TDMReservation = &v
	}

	if p.RobotsPresent != nil {
		r.AIDirectives = &AIDirectiveSummary{
			RobotsPresent:   *p.RobotsPresent,
			AgentsAddressed: p.AIAgentsAddressed,
			AgentsBlocked:   p.AIAgentsBlocked,
			VendorsBlocked:  p.AIVendorsBlocked,
		}
	}

	return r
}

// ParquetWriter writes provenance records as Parquet.
//
// Buffered by row group, not by file: parquet-go accumulates a row group in
// memory and flushes it, so peak memory is one row group rather than the corpus.
// That keeps the property the JSONL writer has — a crawl of a hundred million
// pages must not be made to hold them all — while producing a file that DuckDB,
// Polars, and `datasets` load directly.
type ParquetWriter struct {
	mu      sync.Mutex
	f       *os.File
	w       *parquet.GenericWriter[parquetRecord]
	path    string
	count   int64
	skipped int64
	closed  bool
}

// rowGroupSize is how many records accumulate before a flush.
//
// Parquet's column statistics are per row group, so a reader skipping row groups
// on a predicate gets finer granularity from smaller ones — and pays for it in
// file size and per-group overhead. Ten thousand text records is a few tens of
// megabytes, which is a reasonable working set and still lets a query on, say,
// mime_type skip most of a large file.
const rowGroupSize = 10_000

// NewParquetWriter creates or truncates a Parquet corpus at path.
func NewParquetWriter(path string) (*ParquetWriter, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("provenance: create corpus dir: %w", err)
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("provenance: create corpus: %w", err)
	}

	// zstd over snappy: a corpus is mostly prose, which compresses well, and it is
	// written once and read many times. Spending write CPU to make every later
	// read cheaper is the right side of that trade.
	w := parquet.NewGenericWriter[parquetRecord](f,
		parquet.Compression(&zstd.Codec{Level: zstd.DefaultLevel}),
		parquet.MaxRowsPerRowGroup(rowGroupSize),
	)

	return &ParquetWriter{f: f, w: w, path: path}, nil
}

// Write appends a record. Incomplete records are skipped and counted, exactly as
// in the JSONL writer — the two must not disagree about what belongs in a corpus.
func (p *ParquetWriter) Write(r Record) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return fmt.Errorf("provenance: write to a closed corpus")
	}
	if !r.Complete() {
		p.skipped++
		return nil
	}
	if r.SchemaVersion == 0 {
		r.SchemaVersion = SchemaVersion
	}

	if _, err := p.w.Write([]parquetRecord{toParquet(r)}); err != nil {
		return fmt.Errorf("provenance: write record for %s: %w", r.URL, err)
	}
	p.count++
	return nil
}

// Stats reports what the writer has accepted and refused.
func (p *ParquetWriter) Stats() (written, skipped int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count, p.skipped
}

// Path returns the file being written.
func (p *ParquetWriter) Path() string { return p.path }

// Close flushes the final row group and writes the footer.
//
// Unlike a JSONL file, a Parquet file is unreadable without its footer — an
// unclosed corpus is not a short corpus, it is not a corpus at all. So a close
// error is reported rather than swallowed, and callers should treat it as fatal
// to the run's output.
func (p *ParquetWriter) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true

	if err := p.w.Close(); err != nil {
		p.f.Close()
		return fmt.Errorf("provenance: finalise corpus (the file is unreadable without its footer): %w", err)
	}
	return p.f.Close()
}

// ReadParquetCorpus loads every record from a Parquet corpus.
//
// Diagnostic and test helper, like ReadCorpus: it holds the whole file in memory,
// which is what the writer exists to avoid. Real consumers should use DuckDB or
// Polars, which is the entire reason for writing Parquet.
func ReadParquetCorpus(path string) ([]Record, error) {
	rows, err := parquet.ReadFile[parquetRecord](path)
	if err != nil {
		return nil, fmt.Errorf("provenance: read parquet corpus: %w", err)
	}

	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromParquet(row))
	}
	return out, nil
}

// RecordWriter is what the engine holds. Both corpus formats satisfy it, so the
// crawl path does not know or care which one it is feeding.
type RecordWriter interface {
	Write(Record) error
	Stats() (written, skipped int64)
	Path() string
	Close() error
}

var (
	_ RecordWriter = (*CorpusWriter)(nil)
	_ RecordWriter = (*ParquetWriter)(nil)
)

// OpenCorpus picks a writer from the file extension.
//
// Inferring beats a separate --format flag here: the extension is the thing a
// reader will look at to decide how to open the file, so letting the two disagree
// would be a way to produce a .parquet full of JSON.
func OpenCorpus(path string) (RecordWriter, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".parquet", ".pq":
		return NewParquetWriter(path)
	default:
		return NewCorpusWriter(path)
	}
}

// ReadAnyCorpus loads a corpus of either format, again by extension.
func ReadAnyCorpus(path string) ([]Record, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".parquet", ".pq":
		return ReadParquetCorpus(path)
	default:
		return ReadCorpus(path)
	}
}
