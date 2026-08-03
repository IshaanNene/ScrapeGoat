package engine

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

var concurrencyLogger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

// TestMarkIfUnseenIsAtomic covers the check-then-act race that IsSeen+MarkSeen had:
// N goroutines racing on one URL must produce exactly one winner.
func TestMarkIfUnseenIsAtomic(t *testing.T) {
	const goroutines = 64

	for trial := 0; trial < 50; trial++ {
		d := NewDeduplicator(16)
		url := fmt.Sprintf("https://example.com/page-%d", trial)

		var wg sync.WaitGroup
		var mu sync.Mutex
		winners := 0
		start := make(chan struct{})

		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start // maximise the overlap
				if d.MarkIfUnseen(url) {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			}()
		}

		close(start)
		wg.Wait()

		if winners != 1 {
			t.Fatalf("trial %d: %d goroutines claimed the same URL, want exactly 1", trial, winners)
		}
	}
}

// TestConcurrentAddRequestEnqueuesOnce is the same property at the engine level,
// which is where the bug actually bit: workers call AddRequest during link
// extraction, so two pages linking to the same URL raced.
func TestConcurrentAddRequestEnqueuesOnce(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false
	cfg.Engine.MaxDepth = 5

	eng := New(cfg, concurrencyLogger)

	const goroutines = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := types.NewRequest("https://example.com/contested")
			if err != nil {
				return
			}
			<-start
			if err := eng.AddRequest(req); err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}

	close(start)
	wg.Wait()

	if accepted != 1 {
		t.Errorf("AddRequest accepted the same URL %d times, want 1", accepted)
	}
	if n := eng.frontier.Len(); n != 1 {
		t.Errorf("frontier holds %d copies of the URL, want 1", n)
	}
}

// TestRejectedURLsAreNotMarkedSeen pins the ordering of the dedup claim against the
// other filters: a URL turned away by the domain allowlist must not be recorded, or
// it can never be enqueued later under a different configuration.
func TestRejectedURLsAreNotMarkedSeen(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false
	cfg.Engine.AllowedDomains = []string{"allowed.example"}

	eng := New(cfg, concurrencyLogger)

	req, err := types.NewRequest("https://blocked.example/page")
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := eng.AddRequest(req); err == nil {
		t.Fatal("expected the domain filter to reject the request")
	}

	if eng.dedup.IsSeen("https://blocked.example/page") {
		t.Error("a URL rejected by the domain filter was marked as seen")
	}
}

// TestPauseResumeIsRaceFree exercises the channel swap that used to be an
// unsynchronised write racing with the workers' select. Run under -race.
func TestPauseResumeIsRaceFree(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	cfg.Engine.Concurrency = 8
	eng := New(cfg, concurrencyLogger)
	s := eng.scheduler

	var wg sync.WaitGroup

	// Readers stand in for workers checking the gate.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				if s.paused.Load() {
					select {
					case <-s.resumeGate():
					case <-time.After(50 * time.Millisecond):
					}
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			s.Pause()
			s.Resume()
		}
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("pause/resume deadlocked — a worker is parked on a stale gate channel")
	}
}

// TestResumeReleasesParkedWaiter is the missed-wakeup regression: reassigning
// resumeCh after closing it meant a worker that read the new channel waited forever.
func TestResumeReleasesParkedWaiter(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	eng := New(cfg, concurrencyLogger)
	s := eng.scheduler

	for i := 0; i < 100; i++ {
		s.Pause()

		released := make(chan struct{})
		go func() {
			<-s.resumeGate()
			close(released)
		}()

		// Let the waiter park before the resume lands.
		time.Sleep(time.Millisecond)
		s.Resume()

		select {
		case <-released:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: waiter was not released by Resume", i)
		}
	}
}

// TestWaitIsIdempotent covers the double-close panic: Wait closed itemChan with no
// guard, so calling it twice killed the process.
func TestWaitIsIdempotent(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	cfg.Engine.Concurrency = 2
	eng := New(cfg, concurrencyLogger)

	if err := eng.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	eng.Stop()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Any of these panicking is the bug.
		eng.Wait()
		eng.Wait()
		eng.Wait()
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("repeated Wait calls deadlocked")
	}
}

// TestConcurrentWaitIsSafe covers two callers racing into shutdown.
func TestConcurrentWaitIsSafe(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	cfg.Engine.Concurrency = 2
	eng := New(cfg, concurrencyLogger)

	if err := eng.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	eng.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); eng.Wait() }()
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent Wait deadlocked")
	}
}

// countingStorage records everything handed to it, so a test can prove a
// ResultsChan subscriber did not steal from storage.
type countingStorage struct {
	mu    sync.Mutex
	items []*types.Item
}

func (c *countingStorage) Store(items []*types.Item) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = append(c.items, items...)
	return nil
}

func (c *countingStorage) Close() error { return nil }

func (c *countingStorage) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// TestResultsChanDoesNotStealFromStorage is the regression test for ResultsChan
// handing back the very channel storeResults was draining. Every item must reach
// both consumers, not be split between them at random.
func TestResultsChanDoesNotStealFromStorage(t *testing.T) {
	const items = 50

	cfg := testutil.LoopbackConfig()
	cfg.Engine.Concurrency = 2
	cfg.Storage.BatchSize = 1

	eng := New(cfg, concurrencyLogger)
	store := &countingStorage{}
	eng.SetStorage(store)

	results := eng.ResultsChan()

	var received int
	consumed := make(chan struct{})
	go func() {
		defer close(consumed)
		for range results {
			received++
		}
	}()

	if err := eng.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	for i := 0; i < items; i++ {
		item := types.NewItem(fmt.Sprintf("https://example.com/%d", i))
		eng.itemChan <- item
	}

	eng.Stop()
	eng.Wait()
	<-consumed

	if store.Len() != items {
		t.Errorf("storage received %d items, want %d — the subscriber stole from it",
			store.Len(), items)
	}
	if received != items {
		t.Errorf("subscriber received %d items, want %d", received, items)
	}
}

// TestMultipleResultsChanSubscribers checks the fan-out serves more than one reader.
func TestMultipleResultsChanSubscribers(t *testing.T) {
	const items = 20

	cfg := testutil.LoopbackConfig()
	cfg.Engine.Concurrency = 2
	cfg.Storage.BatchSize = 1

	eng := New(cfg, concurrencyLogger)
	eng.SetStorage(&countingStorage{})

	a, b := eng.ResultsChan(), eng.ResultsChan()

	var countA, countB int
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range a {
			countA++
		}
	}()
	go func() {
		defer wg.Done()
		for range b {
			countB++
		}
	}()

	if err := eng.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	for i := 0; i < items; i++ {
		eng.itemChan <- types.NewItem(fmt.Sprintf("https://example.com/%d", i))
	}
	eng.Stop()
	eng.Wait()
	wg.Wait()

	if countA != items || countB != items {
		t.Errorf("subscribers saw %d and %d items, both should see %d", countA, countB, items)
	}
}

// TestRetryUsesErrorsAs covers the type assertion that broke retries for any
// wrapped error.
func TestRetryUsesErrorsAs(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	eng := New(cfg, concurrencyLogger)
	s := eng.scheduler

	req, err := types.NewRequest("https://example.com/retry-me")
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.MaxRetries = 3

	// A retryable fetch error, wrapped the way a middleware would wrap it. The old
	// `err.(*types.FetchError)` assertion returns ok=false here and the request is
	// dropped instead of retried.
	wrapped := fmt.Errorf("middleware context: %w", &types.FetchError{
		URL:       req.URLString(),
		Err:       errors.New("connection reset"),
		Retryable: true,
	})

	before := eng.frontier.Len()
	s.handleFetchError(concurrencyLogger, req, wrapped)

	if eng.frontier.Len() != before+1 {
		t.Error("a wrapped retryable error was not requeued — errors.As is not being used")
	}
	if req.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", req.RetryCount)
	}
}
