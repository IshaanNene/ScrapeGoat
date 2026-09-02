package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// TestOutstandingCountsDequeuedRequests is the defect stated directly: between a
// request leaving the queue and its processing finishing, the crawl is not empty.
// The old completion check could not see this interval at all.
func TestOutstandingCountsDequeuedRequests(t *testing.T) {
	f := NewFrontier(nil)
	req, err := types.NewRequest("https://example.com/a")
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	f.Push(req)

	if got := f.Outstanding(); got != 1 {
		t.Fatalf("Outstanding() = %d before pop, want 1", got)
	}

	got := f.TryPop()
	if got == nil {
		t.Fatal("TryPop returned nil")
	}
	if l := f.Len(); l != 0 {
		t.Fatalf("Len() = %d after pop, want 0", l)
	}
	// The whole point: the queue is empty and the crawl is not finished.
	if o := f.Outstanding(); o != 1 {
		t.Fatalf("Outstanding() = %d while a request is in flight, want 1", o)
	}

	f.Done(got)
	if o := f.Outstanding(); o != 0 {
		t.Fatalf("Outstanding() = %d after Done, want 0", o)
	}
}

// TestOutstandingCountsEveryHeldRequest holds every request off-queue at once and
// checks the count reflects it. This is the state the old check was blind to: the
// queue is empty, every worker is busy, and the crawl is at its most active.
func TestOutstandingCountsEveryHeldRequest(t *testing.T) {
	f := NewFrontier(nil)
	const workers = 8
	for i := 0; i < workers; i++ {
		req, err := types.NewRequest("https://example.com/p")
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		f.Push(req)
	}

	holding := make(chan struct{}, workers)
	release := make(chan struct{})
	var done sync.WaitGroup

	for i := 0; i < workers; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			req := f.TryPop()
			if req == nil {
				return
			}
			holding <- struct{}{}
			<-release
			f.Done(req)
		}()
	}

	for i := 0; i < workers; i++ {
		<-holding
	}

	if l := f.Len(); l != 0 {
		t.Fatalf("Len() = %d with everything dequeued, want 0", l)
	}
	if o := f.Outstanding(); o != workers {
		t.Fatalf("Outstanding() = %d with %d requests held by workers, want %d", o, workers, workers)
	}

	close(release)
	done.Wait()

	if o := f.Outstanding(); o != 0 {
		t.Fatalf("Outstanding() = %d after every worker finished, want 0", o)
	}
}

// TestOutstandingRestoredWhenClaimFails covers PopReady's put-back path: a request
// returned to the queue because another worker took the domain's token must be
// counted once, not zero times and not twice.
func TestOutstandingRestoredWhenClaimFails(t *testing.T) {
	f := NewFrontier(nil)
	req, err := types.NewRequest("https://example.com/a")
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	f.Push(req)

	// A gate that says ready but refuses to be claimed, forcing the put-back
	// branch exactly once before allowing the take.
	gate := &flakyGate{}
	got := f.PopReady(context.Background(), gate)
	if got == nil {
		t.Fatal("PopReady returned nil")
	}
	if gate.claims < 2 {
		t.Fatalf("expected the put-back path to be exercised, got %d claims", gate.claims)
	}
	if o := f.Outstanding(); o != 1 {
		t.Fatalf("Outstanding() = %d after a put-back and re-take, want 1", o)
	}
	f.Done(got)
	if o := f.Outstanding(); o != 0 {
		t.Fatalf("Outstanding() = %d after Done, want 0", o)
	}
}

type flakyGate struct{ claims int }

func (g *flakyGate) Ready(string) bool { return true }
func (g *flakyGate) Claim(string) bool {
	g.claims++
	return g.claims > 1
}
func (g *flakyGate) Delay(string) time.Duration { return 0 }
