package engine

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
)

const conditionalETag = `W/"abc123"`

// conditionalSite serves a page with an ETag and answers a matching
// If-None-Match with 304, the way any ordinary web server does. It counts full
// responses separately from confirmations, which is the number the feature exists
// to reduce.
func conditionalSite(t *testing.T, full, notModified *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", conditionalETag)
		w.Header().Set("Last-Modified", "Wed, 08 Feb 2023 21:02:32 GMT")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if r.Header.Get("If-None-Match") == conditionalETag {
			atomic.AddInt32(notModified, 1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		atomic.AddInt32(full, 1)
		_, _ = w.Write([]byte(`<html><head><title>P</title></head><body>` +
			`<p>Body text long enough to be extracted as content by the density scorer.</p>` +
			`</body></html>`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func conditionalConfig() *config.Config {
	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false
	cfg.Engine.Concurrency = 1
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.MaxDepth = 0
	return cfg
}

// runCrawlOnce crawls one URL, optionally against a prior corpus, and returns the
// records written.
func runCrawlOnce(t *testing.T, cfg *config.Config, url string, prior *provenance.PriorCorpus) ([]provenance.Record, map[string]any) {
	t.Helper()

	eng := New(cfg, concurrencyLogger)
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, concurrencyLogger)
	if err != nil {
		t.Fatalf("fetcher: %v", err)
	}
	eng.SetFetcher("http", httpFetcher)

	sink := &condSink{}
	eng.SetCorpusWriter(sink, "cond-test")
	if prior != nil {
		eng.SetPriorCorpus(prior)
	}

	if err := eng.AddSeed(url); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	eng.Wait()

	return sink.all(), eng.StatsSnapshot()
}

// TestRecrawlWithPriorCorpusSendsConditionalRequest is the point of the feature:
// a page that has not changed costs a header exchange instead of a download.
func TestRecrawlWithPriorCorpusSendsConditionalRequest(t *testing.T) {
	var full, notModified int32
	srv := conditionalSite(t, &full, &notModified)
	cfg := conditionalConfig()

	// First crawl: nothing known, so the page comes down in full.
	first, _ := runCrawlOnce(t, cfg, srv.URL+"/p", nil)
	if len(first) != 1 {
		t.Fatalf("first crawl wrote %d records, want 1", len(first))
	}
	if first[0].ETag != conditionalETag {
		t.Fatalf("first record did not capture the ETag: %q", first[0].ETag)
	}
	if got := atomic.LoadInt32(&full); got != 1 {
		t.Fatalf("first crawl made %d full fetches, want 1", got)
	}

	// Second crawl, told what the first one saw.
	second, stats := runCrawlOnce(t, cfg, srv.URL+"/p", provenance.NewPriorCorpus(first))

	if got := atomic.LoadInt32(&notModified); got != 1 {
		t.Errorf("server answered %d conditional requests, want 1 — the validator was not sent", got)
	}
	if got := atomic.LoadInt32(&full); got != 1 {
		t.Errorf("server sent %d full responses across both crawls, want 1 — the page was downloaded again", got)
	}
	if n, _ := stats["pages_unchanged"].(int64); n != 1 {
		t.Errorf("pages_unchanged = %v, want 1", n)
	}

	// The renewed record must be the old one with a later timestamp, not a record
	// of the empty 304 body.
	if len(second) != 1 {
		t.Fatalf("second crawl wrote %d records, want 1", len(second))
	}
	got := second[0]
	if got.ContentHash != first[0].ContentHash {
		t.Errorf("renewed record hash %q differs from %q — the empty 304 body was hashed",
			got.ContentHash, first[0].ContentHash)
	}
	if got.Text != first[0].Text {
		t.Errorf("renewed record lost its text: %q", got.Text)
	}
	if got.Text == "" {
		t.Error("renewed record has no text at all; the corpus entry was destroyed rather than renewed")
	}
	if !got.FetchedAt.After(first[0].FetchedAt) {
		t.Errorf("renewed record kept the old timestamp %v; the confirmation is the one new fact",
			got.FetchedAt)
	}
}

// TestChangedPageIsRefetched is the other half. A conditional request must not
// make a changed page look unchanged.
func TestChangedPageIsRefetched(t *testing.T) {
	var served int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("ETag", `W/"version-2"`) // the page moved on
		atomic.AddInt32(&served, 1)
		_, _ = w.Write([]byte(`<html><body><p>` +
			`The second version of this page, long enough to be extracted as content.` +
			`</p></body></html>`))
	}))
	t.Cleanup(srv.Close)

	cfg := conditionalConfig()
	stale := provenance.NewPriorCorpus([]provenance.Record{{
		URL:         srv.URL + "/p",
		ContentHash: "hash-of-version-1",
		Text:        "the first version",
		ETag:        `W/"version-1"`,
		FetchedAt:   time.Now().Add(-time.Hour),
	}})

	records, stats := runCrawlOnce(t, cfg, srv.URL+"/p", stale)

	if n, _ := stats["pages_unchanged"].(int64); n != 0 {
		t.Errorf("pages_unchanged = %v for a page that changed, want 0", n)
	}
	if len(records) != 1 {
		t.Fatalf("wrote %d records, want 1", len(records))
	}
	if records[0].ContentHash == "hash-of-version-1" {
		t.Error("a changed page kept the stale record")
	}
	if records[0].Text == "the first version" {
		t.Error("a changed page kept the stale text")
	}
}

// TestConditionalHeadersAreEchoedVerbatim guards the detail that makes the whole
// exchange work: an ETag is opaque, and a Last-Modified is the server's own
// rendering of a date.
func TestConditionalHeadersAreEchoedVerbatim(t *testing.T) {
	prior := provenance.NewPriorCorpus([]provenance.Record{{
		URL:          "https://example.com/p",
		ETag:         `W/"weak-marker-included"`,
		LastModified: "Wed, 08 Feb 2023 21:02:32 GMT",
	}})

	h := http.Header{}
	if !prior.ConditionalHeaders("https://example.com/p", h) {
		t.Fatal("no conditional headers were set for a known URL")
	}
	if got := h.Get("If-None-Match"); got != `W/"weak-marker-included"` {
		t.Errorf("If-None-Match = %q, want the ETag unaltered", got)
	}
	if got := h.Get("If-Modified-Since"); got != "Wed, 08 Feb 2023 21:02:32 GMT" {
		t.Errorf("If-Modified-Since = %q, want the server's own formatting", got)
	}

	// A URL the prior corpus does not know must not get headers, or the server is
	// asked about a version we never held.
	empty := http.Header{}
	if prior.ConditionalHeaders("https://example.com/unknown", empty) {
		t.Error("set conditional headers for a URL with no prior record")
	}
	if len(empty) != 0 {
		t.Errorf("headers were set anyway: %v", empty)
	}
}

type condSink struct {
	mu      sync.Mutex
	records []provenance.Record
}

func (s *condSink) Write(r provenance.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
	return nil
}
func (s *condSink) Stats() (int64, int64) { return int64(len(s.records)), 0 }
func (s *condSink) Path() string          { return "" }
func (s *condSink) Close() error          { return nil }
func (s *condSink) all() []provenance.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provenance.Record, len(s.records))
	copy(out, s.records)
	return out
}
