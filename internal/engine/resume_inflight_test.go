package engine

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"

	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// TestResumeDoesNotLoseInFlightRequests is the specification for the checkpoint.
//
// A checkpoint saves the frontier and the seen set. The frontier holds what is
// queued; the seen set holds everything ever claimed. A request that a worker has
// dequeued but not finished is in neither — it left the queue, and it is marked
// seen. So a resume re-enqueues nothing for it and the dedup set refuses to let it
// back in.
//
// With concurrency 100, up to 100 URLs disappear per resume. Nothing reports it:
// no error, no counter, and the crawl says it completed.
func TestResumeDoesNotLoseInFlightRequests(t *testing.T) {
	checkpointInDir(t)

	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false
	cfg.Engine.MaxDepth = 5

	first := New(cfg, concurrencyLogger)
	all := []string{
		"https://example.com/a",
		"https://example.com/b",
		"https://example.com/c",
		"https://example.com/d",
	}
	for _, u := range all {
		req, err := types.NewRequest(u)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		if err := first.AddRequest(req); err != nil {
			t.Fatalf("add %s: %v", u, err)
		}
	}

	// Two workers take a request each and are still holding them — the state a
	// crawl is in at essentially every instant.
	//
	// Which two is not fixed: the frontier is a heap and these share a priority,
	// so the test records what it actually dequeued rather than assuming an order.
	const held = 2
	taken := map[string]bool{}
	for i := 0; i < held; i++ {
		req := first.frontier.TryPop()
		if req == nil {
			t.Fatal("frontier gave nothing to dequeue")
		}
		taken[req.URLString()] = true
	}
	if len(taken) != held {
		t.Fatalf("dequeued %d distinct requests, want %d", len(taken), held)
	}

	// Whatever is left in the queue was never at risk; if it goes missing the
	// checkpoint is broken in a different and louder way.
	stillQueued := map[string]bool{}
	for _, req := range first.frontier.Snapshot() {
		if !taken[req.URLString()] {
			stillQueued[req.URLString()] = true
		}
	}

	if err := first.checkpoint.Save(first.frontier, first.dedup, first.stats); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}

	// A restarted process.
	second := New(cfg, concurrencyLogger)
	if err := second.ResumeFromCheckpoint(); err != nil {
		t.Fatalf("resume: %v", err)
	}

	restored := map[string]bool{}
	for _, req := range second.frontier.Snapshot() {
		restored[req.URLString()] = true
	}

	var lost []string
	for u := range taken {
		if !restored[u] {
			lost = append(lost, u)
		}
	}
	if len(lost) > 0 {
		t.Errorf("resume lost %d of %d in-flight requests: %v", len(lost), len(taken), lost)
	}

	for u := range stillQueued {
		if !restored[u] {
			t.Errorf("resume lost queued request %s", u)
		}
	}
}

// TestResumeAfterDoneDoesNotResurrect is the other half. A request that finished
// before the checkpoint must not come back — re-crawling completed work is the
// failure the seen set exists to prevent, and a fix that restored everything ever
// dequeued would trade one bug for its mirror image.
func TestResumeAfterDoneDoesNotResurrect(t *testing.T) {
	checkpointInDir(t)

	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false

	first := New(cfg, concurrencyLogger)
	req, err := types.NewRequest("https://example.com/finished")
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := first.AddRequest(req); err != nil {
		t.Fatalf("add: %v", err)
	}

	got := first.frontier.TryPop()
	if got == nil {
		t.Fatal("frontier gave nothing to dequeue")
	}
	first.frontier.Done(got) // the worker finished it

	if err := first.checkpoint.Save(first.frontier, first.dedup, first.stats); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}

	second := New(cfg, concurrencyLogger)
	if err := second.ResumeFromCheckpoint(); err != nil {
		t.Fatalf("resume: %v", err)
	}

	for _, r := range second.frontier.Snapshot() {
		if r.URLString() == "https://example.com/finished" {
			t.Error("resume re-queued a request that had already completed")
		}
	}
}

// TestCheckpointDuringLiveCrawlKeepsInFlight exercises the same property through a
// running crawl rather than by driving the frontier directly, so that the wiring
// between worker, frontier and checkpoint is covered and not just the data
// structure.
func TestCheckpointDuringLiveCrawlKeepsInFlight(t *testing.T) {
	checkpointInDir(t)

	release := make(chan struct{})
	var holding sync.WaitGroup
	holding.Add(1)
	var once sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The first request to arrive blocks, so a worker is provably mid-fetch
		// when the checkpoint is taken.
		once.Do(func() { holding.Done() })
		<-release
		_, _ = w.Write([]byte("<html><body><p>held</p></body></html>"))
	}))
	// Teardown order matters and t.Cleanup is LIFO, so these are registered in
	// reverse: the handler is released first, then the engine is stopped, and the
	// server is closed last. A plain `defer srv.Close()` deadlocks — deferred calls
	// run before cleanups, so Close waits on a handler nothing has released yet.
	t.Cleanup(srv.Close)

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

	if err := eng.AddSeed(srv.URL + "/held"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		eng.Stop()
		eng.Wait()
	})
	t.Cleanup(func() { close(release) })

	holding.Wait() // a worker now holds the seed and is blocked in the fetcher

	if err := eng.checkpoint.Save(eng.frontier, eng.dedup, eng.stats); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}

	second := New(cfg, concurrencyLogger)
	if err := second.ResumeFromCheckpoint(); err != nil {
		t.Fatalf("resume: %v", err)
	}

	var found bool
	for _, r := range second.frontier.Snapshot() {
		if r.URLString() == srv.URL+"/held" {
			found = true
		}
	}
	if !found {
		t.Errorf("a crawl checkpointed while fetching %s lost it on resume", srv.URL+"/held")
	}
}
