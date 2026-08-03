package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

func TestThrottlerSpacesRequestsPerDomain(t *testing.T) {
	const delay = 50 * time.Millisecond
	thr := NewThrottler(delay, 100)

	// First request goes immediately.
	if !thr.Allow("example.com") {
		t.Fatal("first request to a fresh domain was throttled")
	}
	// Second must wait.
	if thr.Allow("example.com") {
		t.Fatal("second immediate request to the same domain was allowed")
	}

	time.Sleep(delay + 20*time.Millisecond)
	if !thr.Allow("example.com") {
		t.Error("request after the delay was still throttled")
	}
}

func TestThrottlerIsPerDomain(t *testing.T) {
	thr := NewThrottler(time.Second, 100)

	// A domain being throttled must say nothing about any other domain — that
	// coupling is what made one slow site stall the whole crawl.
	if !thr.Allow("a.test") {
		t.Fatal("first request to a.test throttled")
	}
	if !thr.Allow("b.test") {
		t.Fatal("b.test was throttled by a.test's delay")
	}
	if thr.Allow("a.test") {
		t.Error("a.test allowed a second immediate request")
	}
}

func TestThrottlerDisabledWhenDelayIsZero(t *testing.T) {
	thr := NewThrottler(0, 100)
	if thr.Enabled() {
		t.Error("a zero delay should disable throttling")
	}
	for i := 0; i < 100; i++ {
		if !thr.Allow("example.com") {
			t.Fatalf("request %d throttled with delay disabled", i)
		}
	}
}

// TestThrottlerEvictsOldDomains covers the unbounded map: one entry per domain
// was retained forever, so a broad crawl leaked memory monotonically.
func TestThrottlerEvictsOldDomains(t *testing.T) {
	const maxSlots = 32
	thr := NewThrottler(time.Second, maxSlots)

	for i := 0; i < maxSlots*4; i++ {
		thr.Allow(fmt.Sprintf("host-%d.test", i))
	}

	if got := thr.Len(); got > maxSlots {
		t.Errorf("throttler retained %d slots, cap is %d", got, maxSlots)
	}
}

func TestThrottlerReserveDoesNotConsume(t *testing.T) {
	thr := NewThrottler(time.Second, 100)

	// Reserve is used while scanning candidates the worker may not take, so it
	// must not spend the domain's allowance.
	for i := 0; i < 5; i++ {
		if d := thr.Reserve("example.com"); d != 0 {
			t.Fatalf("Reserve %d reported a delay of %v on an untouched domain", i, d)
		}
	}
	if !thr.Allow("example.com") {
		t.Error("Reserve consumed the token it was only supposed to inspect")
	}
}

func TestThrottlerWaitRespectsContext(t *testing.T) {
	thr := NewThrottler(time.Hour, 100)
	thr.Allow("example.com") // consume the only token

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := thr.Wait(ctx, "example.com"); err == nil {
		t.Fatal("Wait should have returned the context error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Wait ignored the context for %v", elapsed)
	}
}

// TestPopReadySkipsThrottledDomains is the core regression test.
//
// Before, a worker dequeued whatever was at the head of the heap and then slept
// out that domain's politeness delay — occupying a worker slot and blocking work
// for every other domain. A frontier holding one throttled request and one ready
// request must hand back the ready one immediately.
func TestPopReadySkipsThrottledDomains(t *testing.T) {
	thr := NewThrottler(time.Hour, 100)
	f := NewFrontier()

	// Spend slow.test's allowance, so anything queued for it is throttled.
	if !thr.Allow("slow.test") {
		t.Fatal("could not prime the throttled domain")
	}

	slow := mustRequest(t, "https://slow.test/a")
	slow.Priority = types.PriorityHighest // ahead of the ready one in the heap
	f.Push(slow)
	f.Push(mustRequest(t, "https://fast.test/b"))

	done := make(chan *types.Request, 1)
	go func() { done <- f.PopReady(context.Background(), thr) }()

	select {
	case req := <-done:
		if req == nil {
			t.Fatal("PopReady returned nil with runnable work queued")
		}
		if req.Domain() != "fast.test" {
			t.Fatalf("PopReady returned %q; it waited on the throttled domain instead of "+
				"taking the ready one", req.Domain())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PopReady blocked on a throttled domain while other work was ready")
	}
}

// TestPopReadyWaitsWhenNothingIsRunnable checks the other half: when every queued
// domain is throttled, the worker parks until one becomes ready rather than
// spinning or returning nil.
func TestPopReadyWaitsWhenNothingIsRunnable(t *testing.T) {
	const delay = 100 * time.Millisecond
	thr := NewThrottler(delay, 100)
	f := NewFrontier()

	thr.Allow("only.test")
	f.Push(mustRequest(t, "https://only.test/a"))

	start := time.Now()
	req := f.PopReady(context.Background(), thr)
	elapsed := time.Since(start)

	if req == nil {
		t.Fatal("PopReady gave up instead of waiting out the delay")
	}
	if elapsed < delay/2 {
		t.Errorf("PopReady returned after %v, ignoring the %v politeness delay", elapsed, delay)
	}
}

func TestPopReadyUnblocksOnClose(t *testing.T) {
	thr := NewThrottler(time.Hour, 100)
	f := NewFrontier()

	thr.Allow("only.test")
	f.Push(mustRequest(t, "https://only.test/a"))

	done := make(chan *types.Request, 1)
	go func() { done <- f.PopReady(context.Background(), thr) }()

	time.Sleep(30 * time.Millisecond)
	f.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("PopReady did not unblock on Close")
	}
}

func TestPopReadyNilGateFallsBackToPop(t *testing.T) {
	f := NewFrontier()
	f.Push(mustRequest(t, "https://example.com/a"))

	if req := f.PopReady(context.Background(), nil); req == nil {
		t.Fatal("a nil gate should behave like an unthrottled Pop")
	}
}

// TestConcurrentPopReadyClaimsExactlyOnce checks that the scan-then-claim split
// does not let two workers dispatch the same domain within one delay window.
func TestConcurrentPopReadyClaimsExactlyOnce(t *testing.T) {
	const workers = 16
	thr := NewThrottler(time.Hour, 100)
	f := NewFrontier()

	for i := 0; i < workers; i++ {
		f.Push(mustRequest(t, fmt.Sprintf("https://one.test/%d", i)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	var mu sync.Mutex
	dispatched := 0

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if req := f.PopReady(ctx, thr); req != nil {
				mu.Lock()
				dispatched++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// One domain, a one-hour delay, and a burst of 1: exactly one request may go.
	if dispatched != 1 {
		t.Errorf("%d requests dispatched to a single throttled domain, want 1", dispatched)
	}
}

// TestThrottledDomainDoesNotStarveOthers is the end-to-end statement of the bug:
// with a slow domain and a fast one both queued, the fast domain's work must
// complete without waiting on the slow one.
func TestThrottledDomainDoesNotStarveOthers(t *testing.T) {
	const (
		slowDelay = 2 * time.Second
		fastCount = 20
	)

	thr := NewThrottler(slowDelay, 100)
	f := NewFrontier()

	// Prime the slow domain and queue work for it at the highest priority, so a
	// naive scheduler takes it first and stalls.
	thr.Allow("slow.test")
	slow := mustRequest(t, "https://slow.test/blocked")
	slow.Priority = types.PriorityHighest
	f.Push(slow)

	// The fast domain shares the same delay, so only one of its requests can go
	// per window either — what matters is that the *slow* domain does not hold
	// the worker while fast work is available.
	for i := 0; i < fastCount; i++ {
		f.Push(mustRequest(t, fmt.Sprintf("https://fast-%d.test/x", i)))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	got := 0
	for {
		req := f.PopReady(ctx, thr)
		if req == nil {
			break
		}
		got++
		if req.Domain() == "slow.test" {
			t.Fatal("dispatched the throttled domain before its delay elapsed")
		}
	}

	if got != fastCount {
		t.Errorf("dispatched %d of %d ready requests within the slow domain's delay window; "+
			"a throttled domain is still holding up the pool", got, fastCount)
	}
}
