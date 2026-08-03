package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

func mustRequest(t testing.TB, raw string) *types.Request {
	t.Helper()
	req, err := types.NewRequest(raw)
	if err != nil {
		t.Fatalf("new request %q: %v", raw, err)
	}
	return req
}

// TestPopWakesPromptlyOnPush pins the property the 50 ms poll loop lacked: a Pop
// blocked on an empty frontier is woken by the Push that supplies its work, not by
// the next tick of a timer.
func TestPopWakesPromptlyOnPush(t *testing.T) {
	f := NewFrontier()
	req := mustRequest(t, "https://example.com/a")

	// The goroutine reports when Pop returned; the latency is measured against the
	// moment of the Push, not the start of the goroutine — otherwise the sleep that
	// parks the worker would be counted as wakeup time.
	popped := make(chan time.Time, 1)
	ready := make(chan struct{})

	go func() {
		close(ready)
		got := f.Pop(context.Background())
		if got == nil {
			popped <- time.Time{}
			return
		}
		popped <- time.Now()
	}()

	<-ready
	// Give the goroutine a moment to actually park in Pop, so we are timing the
	// wakeup rather than a lucky first-pass heap check.
	time.Sleep(20 * time.Millisecond)

	pushedAt := time.Now()
	f.Push(req)

	select {
	case returnedAt := <-popped:
		if returnedAt.IsZero() {
			t.Fatal("Pop returned nil instead of the pushed request")
		}
		d := returnedAt.Sub(pushedAt)
		// The old implementation could not beat 50 ms here. 10 ms leaves ample room
		// for scheduler noise on a loaded CI box while still failing a poll loop.
		if d > 10*time.Millisecond {
			t.Errorf("Pop took %v to see a Push; expected a prompt wakeup, not a poll", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Pop never returned after Push")
	}
}

// TestPopUnblocksOnClose ensures a parked worker is released when the crawl ends,
// rather than waiting out a timer.
func TestPopUnblocksOnClose(t *testing.T) {
	f := NewFrontier()

	done := make(chan *types.Request, 1)
	go func() { done <- f.Pop(context.Background()) }()

	time.Sleep(20 * time.Millisecond)
	f.Close()

	select {
	case req := <-done:
		if req != nil {
			t.Errorf("expected nil from a closed empty frontier, got %v", req.URLString())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Pop did not unblock on Close")
	}
}

// TestPopUnblocksOnContextCancel covers shutdown via context rather than Close.
func TestPopUnblocksOnContextCancel(t *testing.T) {
	f := NewFrontier()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan *types.Request, 1)
	go func() { done <- f.Pop(ctx) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case req := <-done:
		if req != nil {
			t.Errorf("expected nil after cancel, got %v", req.URLString())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Pop did not unblock on context cancel")
	}
}

// TestCloseDrainsPendingWork guards the race between a final Push and Close: work
// already in the queue must still come out.
func TestCloseDrainsPendingWork(t *testing.T) {
	f := NewFrontier()
	f.Push(mustRequest(t, "https://example.com/pending"))
	f.Close()

	req := f.Pop(context.Background())
	if req == nil {
		t.Fatal("a request queued before Close was lost")
	}
	if next := f.Pop(context.Background()); next != nil {
		t.Errorf("expected nil after draining, got %v", next.URLString())
	}
}

// TestConcurrentPopsSeeEveryPush is the test for the wakeup-baton logic: Push
// signals a single waiter, so a woken Pop must pass the signal on while work
// remains, or a burst of pushes strands most of the pool.
func TestConcurrentPopsSeeEveryPush(t *testing.T) {
	const (
		workers = 8
		items   = 200
	)

	f := NewFrontier()
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := 0

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				req := f.Pop(context.Background())
				if req == nil {
					return
				}
				mu.Lock()
				seen++
				mu.Unlock()
			}
		}()
	}

	for i := 0; i < items; i++ {
		f.Push(mustRequest(t, "https://example.com/"+string(rune('a'+i%26))+string(rune('a'+i/26))))
	}

	// Wait for the queue to drain, then release the workers.
	deadline := time.Now().Add(5 * time.Second)
	for f.Len() > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	f.Close()
	wg.Wait()

	if seen != items {
		t.Errorf("workers consumed %d of %d pushed requests", seen, items)
	}
}

// BenchmarkFrontierPopWaiting measures the dequeue path a worker actually takes:
// park on an empty frontier, be woken by a Push. This is the number the "50 ms
// poll" comment in the old scheduler was hiding.
func BenchmarkFrontierPopWaiting(b *testing.B) {
	f := NewFrontier()
	req := mustRequest(b, "https://example.com/x")
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < b.N; i++ {
			if f.Pop(ctx) == nil {
				return
			}
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.Push(req)
	}
	<-done
	b.StopTimer()
}

// BenchmarkFrontierPopReady measures the uncontended fast path, where an item is
// already queued.
func BenchmarkFrontierPopReady(b *testing.B) {
	f := NewFrontier()
	req := mustRequest(b, "https://example.com/x")
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.Push(req)
		if f.Pop(ctx) == nil {
			b.Fatal("Pop returned nil with an item queued")
		}
	}
}
