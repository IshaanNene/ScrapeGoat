package engine

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// TestStillFreshPageIsNeverRequested is the point of reading Cache-Control: not a
// cheaper request, no request.
//
// It also pins where the check happens. Filtering in the scheduler would still
// have occupied a worker and spent the domain's politeness token, so a refresh of
// 100,000 fresh pages would make no requests and still take 100,000 seconds.
// Declining to enqueue costs neither, which is why the URL must not reach the
// frontier.
func TestStillFreshPageIsNeverRequested(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("<html><body><p>should never be fetched</p></body></html>"))
	}))
	defer srv.Close()

	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false

	eng := New(cfg, concurrencyLogger)
	t.Cleanup(func() { eng.Stop(); eng.Wait() })

	sink := &condSink{}
	eng.SetCorpusWriter(sink, "fresh-test")
	eng.SetPriorCorpus(provenance.NewPriorCorpus([]provenance.Record{{
		URL:          srv.URL + "/p",
		ContentHash:  "prior-hash",
		Text:         "prior text",
		FetchedAt:    time.Now().Add(-time.Minute),
		CacheControl: "max-age=3600",
	}}))

	err := eng.AddSeed(srv.URL + "/p")
	if !errors.Is(err, types.ErrStillFresh) {
		t.Fatalf("AddSeed = %v, want ErrStillFresh", err)
	}
	if got := eng.frontier.Len(); got != 0 {
		t.Errorf("frontier holds %d requests; a fresh page must not be queued at all", got)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("server was contacted %d times for a page the server said was fresh", got)
	}

	// The page is still part of this run's corpus — skipped is not dropped.
	records := sink.all()
	if len(records) != 1 {
		t.Fatalf("carried forward %d records, want 1", len(records))
	}
	if records[0].ContentHash != "prior-hash" || records[0].Text != "prior text" {
		t.Errorf("carried-forward record lost its content: %+v", records[0])
	}
	if n, _ := eng.StatsSnapshot()["pages_fresh"].(int64); n != 1 {
		t.Errorf("pages_fresh = %v, want 1", n)
	}
}

// TestFreshnessNotMovingTheTimestamp is the distinction from a 304.
//
// A 304 is the server saying "still current" at that moment, so the moment is a
// new fact. Here nothing was asked and nothing answered, so stamping the record
// with now would claim a confirmation that never happened — and a corpus whose
// timestamps drift forward without contact cannot be reasoned about at all.
func TestFreshnessNotMovingTheTimestamp(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false

	eng := New(cfg, concurrencyLogger)
	t.Cleanup(func() { eng.Stop(); eng.Wait() })

	sink := &condSink{}
	eng.SetCorpusWriter(sink, "fresh-test")

	fetchedAt := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Second)
	eng.SetPriorCorpus(provenance.NewPriorCorpus([]provenance.Record{{
		URL: "https://example.com/p", ContentHash: "h",
		FetchedAt: fetchedAt, CacheControl: "max-age=3600",
	}}))

	if err := eng.AddSeed("https://example.com/p"); !errors.Is(err, types.ErrStillFresh) {
		t.Fatalf("AddSeed = %v, want ErrStillFresh", err)
	}

	records := sink.all()
	if len(records) != 1 {
		t.Fatalf("carried forward %d records, want 1", len(records))
	}
	if !records[0].FetchedAt.Equal(fetchedAt) {
		t.Errorf("FetchedAt moved to %v; nothing was fetched, so it must stay at %v",
			records[0].FetchedAt, fetchedAt)
	}
}

// TestStalePageIsStillFetched guards the other direction: an expired directive
// must not suppress the request.
func TestStalePageIsStillFetched(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><p>fetched because the directive expired</p></body></html>"))
	}))
	defer srv.Close()

	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false
	cfg.Engine.Concurrency = 1
	cfg.Engine.PolitenessDelay = 0

	eng := New(cfg, concurrencyLogger)
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, concurrencyLogger)
	if err != nil {
		t.Fatalf("fetcher: %v", err)
	}
	eng.SetFetcher("http", httpFetcher)
	eng.SetCorpusWriter(&condSink{}, "fresh-test")
	eng.SetPriorCorpus(provenance.NewPriorCorpus([]provenance.Record{{
		URL: srv.URL + "/p", ContentHash: "h",
		FetchedAt:    time.Now().Add(-2 * time.Hour), // long past
		CacheControl: "max-age=60",
	}}))

	if err := eng.AddSeed(srv.URL + "/p"); err != nil {
		t.Fatalf("a stale page was not queued: %v", err)
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	eng.Wait()

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server was contacted %d times for a stale page, want 1", got)
	}
}

// TestFreshSeedCountsAsSeeded is the regression for a wiring bug that only
// appeared when the command was actually run.
//
// AddSeed returns ErrStillFresh for a page the server said is current. The CLI's
// seed loop treated any error as a filtered seed, so a refresh whose only seed
// was fresh added nothing and failed outright with "all seeds were filtered or
// blocked" — a correct crawl reported as a broken one. The engine-level contract
// this rests on is that ErrStillFresh is distinguishable from every other reason
// a seed is refused.
func TestFreshSeedCountsAsSeeded(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false

	eng := New(cfg, concurrencyLogger)
	t.Cleanup(func() { eng.Stop(); eng.Wait() })
	eng.SetCorpusWriter(&condSink{}, "fresh-test")
	eng.SetPriorCorpus(provenance.NewPriorCorpus([]provenance.Record{{
		URL: "https://example.com/p", ContentHash: "h",
		FetchedAt: time.Now().Add(-time.Minute), CacheControl: "max-age=3600",
	}}))

	err := eng.AddSeed("https://example.com/p")
	if !errors.Is(err, types.ErrStillFresh) {
		t.Fatalf("AddSeed = %v, want ErrStillFresh", err)
	}
	// The distinctions a caller has to be able to make.
	for _, other := range []error{types.ErrDuplicate, types.ErrMaxDepth, types.ErrBlocked} {
		if errors.Is(err, other) {
			t.Errorf("ErrStillFresh is indistinguishable from %v", other)
		}
	}
}
