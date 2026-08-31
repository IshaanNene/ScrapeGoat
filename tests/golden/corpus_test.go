// Package golden freezes what the crawl derives from a fixed set of real pages.
//
// The corpus in testdata/books-toscrape is a recorded fetch log: 17 fetches from
// books.toscrape.com, stored as content-addressed bodies with the responses that
// produced them. Replaying it is offline and deterministic — robots.txt is served
// from the log like everything else, and every timestamp on the output comes from
// the recording rather than from the clock.
//
// It exists as the safety net for the Item/Assertion migration. types.Item and
// provenance.Record are today two parallel models built by two paths that never
// meet; the plan is for Item to become a view over Assertion. The risk in that is
// not that it fails loudly, it is that it succeeds quietly and changes a value
// somewhere in the middle of a corpus nobody re-reads. So the current output of
// both paths is frozen here first, against real pages rather than hand-written
// HTML, and the migrated pipeline has to reproduce it byte for byte.
//
// These files pin current behaviour, not correct behaviour. Some of what they
// record is arguably wrong — that is the point. A change detector cannot tell you
// what the output should be, only that it moved. Regenerate deliberately:
//
//	go test ./tests/golden -update
package golden

import (
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetchlog"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

var update = flag.Bool("update", false, "rewrite golden files from current output")

const (
	corpusDir = "testdata/books-toscrape"

	// crawlID is fixed so the records do not change identity between runs. A real
	// crawl generates one; a golden corpus needs the same one every time.
	crawlID = "golden-books-toscrape"
)

// corpusRules are applied to every page in the corpus.
//
// Fixed across the corpus so a golden diff reflects a change in derivation rather
// than a change in what was asked for. Chosen to cover the derivation paths that
// have to carry evidence spans once assertions land: element text, an attribute, a
// selector matching many elements, and one matching nothing — because "the field is
// absent" is a derivation outcome too, and a migration that silently starts
// emitting empty strings instead would pass a test that only checked the hits.
var corpusRules = []config.ParseRule{
	{Name: "title", Selector: "title", Type: "css"},
	{Name: "product_title", Selector: ".product_main h1", Type: "css"},
	{Name: "price", Selector: ".price_color", Type: "css"},
	{Name: "availability", Selector: ".availability", Type: "css"},
	{Name: "breadcrumb", Selector: ".breadcrumb li", Type: "css"},
	{Name: "book_links", Selector: "h3 a", Type: "css", Attribute: "href"},
	{Name: "absent", Selector: ".definitely-not-present", Type: "css"},
}

// goldenItem is types.Item in a stable, diffable shape.
type goldenItem struct {
	URL        string         `json:"url"`
	SpiderName string         `json:"spider_name,omitempty"`
	Depth      int            `json:"depth"`
	Timestamp  string         `json:"timestamp"`
	Checksum   string         `json:"checksum,omitempty"`
	Fields     map[string]any `json:"fields"`
}

// goldenRecord is provenance.Record reduced to the fields a migration could move.
//
// Not the whole struct: text runs to kilobytes per page and would make the golden
// file unreadable and its diffs useless. Text is pinned by length and by its first
// and last words instead, which moves whenever extraction moves without burying the
// rest of the comparison.
type goldenRecord struct {
	URL                  string   `json:"url"`
	CanonicalURL         string   `json:"canonical_url,omitempty"`
	ContentHash          string   `json:"content_hash"`
	FetchedAt            string   `json:"fetched_at"`
	StatusCode           int      `json:"status_code"`
	MIMEType             string   `json:"mime_type,omitempty"`
	FinalURL             string   `json:"final_url,omitempty"`
	Title                string   `json:"title,omitempty"`
	TextLen              int      `json:"text_len"`
	TextHead             string   `json:"text_head,omitempty"`
	TextTail             string   `json:"text_tail,omitempty"`
	ExtractionConfidence float64  `json:"extraction_confidence,omitempty"`
	RobotsAllowed        bool     `json:"robots_allowed"`
	Restrictive          bool     `json:"restrictive"`
	AgentsBlocked        []string `json:"agents_blocked,omitempty"`
	Licence              string   `json:"licence,omitempty"`
	CrawlID              string   `json:"crawl_id,omitempty"`
}

// TestGoldenItems pins what the parser derives from the corpus.
//
// When Item becomes a view over Assertion, this file is what the derived items
// must still equal. Nothing about the assertion path needs to exist for the
// comparison to be written — that is the point of writing it now.
func TestGoldenItems(t *testing.T) {
	items, _ := replayCorpus(t)

	got := make([]goldenItem, 0, len(items))
	for _, it := range items {
		got = append(got, goldenItem{
			URL:        it.URL,
			SpiderName: it.SpiderName,
			Depth:      it.Depth,
			Timestamp:  it.Timestamp.UTC().Format(time.RFC3339Nano),
			Checksum:   it.Checksum,
			Fields:     it.Fields,
		})
	}
	// Replay order depends on worker scheduling even at concurrency 1, because the
	// frontier is a priority queue. Sort on something intrinsic to the item.
	sort.Slice(got, func(i, j int) bool {
		if got[i].URL != got[j].URL {
			return got[i].URL < got[j].URL
		}
		return got[i].Depth < got[j].Depth
	})

	compareGolden(t, "items.golden.json", got)
}

// TestGoldenRecords pins the provenance side of the same replay.
//
// The two halves are frozen from one run rather than two, so that a change which
// moves an item and its record together is still visible as one diff.
func TestGoldenRecords(t *testing.T) {
	_, records := replayCorpus(t)

	got := make([]goldenRecord, 0, len(records))
	for _, r := range records {
		g := goldenRecord{
			URL:                  r.URL,
			CanonicalURL:         r.CanonicalURL,
			ContentHash:          r.ContentHash,
			FetchedAt:            r.FetchedAt.UTC().Format(time.RFC3339Nano),
			StatusCode:           r.StatusCode,
			MIMEType:             r.MIMEType,
			FinalURL:             r.FinalURL,
			Title:                r.Title,
			TextLen:              len(r.Text),
			TextHead:             head(r.Text, 60),
			TextTail:             tail(r.Text, 60),
			ExtractionConfidence: r.ExtractionConfidence,
			RobotsAllowed:        r.RobotsAllowed,
			Restrictive:          r.Restrictive(),
			Licence:              r.Signals.Licence,
			CrawlID:              r.CrawlID,
		}
		if r.AIDirectives != nil {
			g.AgentsBlocked = r.AIDirectives.AgentsBlocked
		}
		got = append(got, g)
	}
	sort.Slice(got, func(i, j int) bool {
		if got[i].URL != got[j].URL {
			return got[i].URL < got[j].URL
		}
		return got[i].ContentHash < got[j].ContentHash
	})

	compareGolden(t, "records.golden.json", got)
}

// TestCorpusIsIntact checks the fixture before anything is concluded from it.
//
// A golden file compared against a corrupted corpus is a test that fails for a
// reason nobody will guess. Content-addressed storage makes the check cheap, so
// there is no reason to skip it.
func TestCorpusIsIntact(t *testing.T) {
	store, err := fetchlog.NewStore(corpusDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	entries, err := fetchlog.ReadLog(corpusDir)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the corpus is empty; testdata/books-toscrape is missing or truncated")
	}

	for _, e := range entries {
		if e.Digest == "" {
			continue // a failed fetch stores no body
		}
		body, err := store.Get(e.Digest)
		if err != nil {
			t.Errorf("%s: body %s is missing from the store: %v", e.URL, e.Digest, err)
			continue
		}
		if got := fetchlog.Digest(body); got != e.Digest {
			t.Errorf("%s: body hashes to %s but is filed under %s", e.URL, got, e.Digest)
		}
	}
}

// replayCorpus runs the current pipeline over the frozen log and returns what it
// derived. Offline: the player serves every fetch, robots.txt included.
func replayCorpus(t *testing.T) ([]*types.Item, []provenance.Record) {
	t.Helper()

	manifest, err := fetchlog.ReadManifest(corpusDir)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	cfg := config.DefaultConfig()
	if len(manifest.Config) > 0 {
		if err := json.Unmarshal(manifest.Config, cfg); err != nil {
			t.Fatalf("parse recorded config: %v", err)
		}
	}
	// One worker: the derivation is what is under test, not the scheduler, and a
	// single worker removes a source of run-to-run variation that would otherwise
	// have to be sorted away.
	cfg.Engine.Concurrency = 1
	cfg.Engine.PolitenessDelay = 0
	cfg.Parser.Rules = corpusRules
	cfg.Storage.OutputPath = t.TempDir()

	player, err := fetchlog.NewPlayer(corpusDir)
	if err != nil {
		t.Fatalf("open player: %v", err)
	}
	defer player.Close()

	eng := engine.New(cfg, testLogger())
	eng.SetFetcher("http", player)
	eng.SetParser(parser.NewCompositeParser(testLogger()))

	sink := &recordSink{}
	eng.SetCorpusWriter(sink, crawlID)

	results := eng.ResultsChan()

	for _, seed := range manifest.Seeds {
		if err := eng.AddSeed(seed); err != nil {
			t.Fatalf("seed %s: %v", seed, err)
		}
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}

	var items []*types.Item
	done := make(chan struct{})
	go func() {
		defer close(done)
		for it := range results {
			items = append(items, it)
		}
	}()

	eng.Wait()
	<-done

	if len(items) == 0 {
		t.Fatal("replay produced no items; the corpus or the pipeline is broken")
	}
	return items, sink.all()
}

func compareGolden(t *testing.T, name string, got any) {
	t.Helper()

	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	encoded = append(encoded, '\n')

	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("updated %s (%d bytes)", path, len(encoded))
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./tests/golden -update)", path, err)
	}
	if string(encoded) != string(want) {
		t.Errorf("%s differs from the frozen output.\n"+
			"If the change is intended, review the diff and re-run with -update.\n"+
			"first difference at byte %d", name, firstDiff(string(want), string(encoded)))
	}
}

func firstDiff(a, b string) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func tail(s string, n int) string {
	if len(s) <= n {
		return ""
	}
	return s[len(s)-n:]
}

// recordSink collects the provenance records written during a replay.
type recordSink struct {
	mu      sync.Mutex
	records []provenance.Record
}

func (s *recordSink) Write(r provenance.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
	return nil
}
func (s *recordSink) Stats() (int64, int64) { return int64(len(s.records)), 0 }
func (s *recordSink) Path() string          { return "" }
func (s *recordSink) Close() error          { return nil }

func (s *recordSink) all() []provenance.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provenance.Record, len(s.records))
	copy(out, s.records)
	return out
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
