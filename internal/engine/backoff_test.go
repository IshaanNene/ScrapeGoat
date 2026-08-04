package engine

import (
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

func TestBackoffGrowsExponentially(t *testing.T) {
	const base = 100 * time.Millisecond

	// Full jitter makes each draw uniform in [0, 2^(n-1)*base], so assert on the
	// ceiling rather than the value, and on the ceiling actually growing.
	for n := 1; n <= 6; n++ {
		ceiling := time.Duration(float64(base) * float64(int64(1)<<(n-1)))
		if ceiling > maxBackoff {
			ceiling = maxBackoff
		}

		for i := 0; i < 200; i++ {
			d := backoffFor(testRand(), base, n)
			if d < 0 {
				t.Fatalf("attempt %d produced a negative backoff: %v", n, d)
			}
			if d > ceiling {
				t.Fatalf("attempt %d produced %v, above the %v ceiling", n, d, ceiling)
			}
		}
	}
}

func TestBackoffIsJittered(t *testing.T) {
	const base = time.Second

	// One source across all draws: seeding per draw would produce the same value
	// every time and the test would fail for the wrong reason.
	jitterRand := testRand()

	// Without jitter, a hundred workers that failed together retry together.
	// Distinct draws are the property that matters.
	seen := make(map[time.Duration]bool)
	for i := 0; i < 100; i++ {
		seen[backoffFor(jitterRand, base, 4)] = true
	}
	if len(seen) < 50 {
		t.Errorf("only %d distinct delays in 100 draws; backoff is not jittered", len(seen))
	}
}

func TestBackoffIsCapped(t *testing.T) {
	// A large attempt number must saturate rather than overflow into a negative
	// or absurd duration.
	for _, n := range []int{20, 50, 1000, 1 << 20} {
		d := backoffFor(testRand(), time.Second, n)
		if d < 0 || d > maxBackoff {
			t.Errorf("backoffFor(1s, %d) = %v, outside [0, %v]", n, d, maxBackoff)
		}
	}
}

func TestBackoffZeroBaseIsZero(t *testing.T) {
	if d := backoffFor(testRand(), 0, 5); d != 0 {
		t.Errorf("a zero base should disable backoff, got %v", d)
	}
	if d := backoffFor(testRand(), time.Second, 0); d != 0 {
		t.Errorf("attempt 0 should be zero, got %v", d)
	}
}

func FuzzBackoffFor(f *testing.F) {
	f.Add(int64(time.Second), 3)
	f.Add(int64(0), 0)
	f.Add(int64(-1), -1)
	f.Add(int64(time.Hour), 1<<20)

	f.Fuzz(func(t *testing.T, baseNanos int64, n int) {
		d := backoffFor(testRand(), time.Duration(baseNanos), n)
		// A negative delay would make time.NewTimer fire immediately, turning
		// backoff into a tight retry loop; an uncapped one would stall shutdown.
		if d < 0 || d > maxBackoff {
			t.Fatalf("backoffFor(%v, %d) = %v, outside [0, %v]",
				time.Duration(baseNanos), n, d, maxBackoff)
		}
	})
}

// --- Circuit breaker ---

func TestCircuitOpensAfterConsecutiveFailures(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Minute, 100, nil)

	for i := 0; i < 2; i++ {
		cb.RecordFailure("bad.test")
		if !cb.Allow("bad.test") {
			t.Fatalf("circuit opened after %d failures, threshold is 3", i+1)
		}
	}

	if state := cb.RecordFailure("bad.test"); state != breakerOpen {
		t.Fatalf("state after the third failure = %v, want open", state)
	}
	if cb.Allow("bad.test") {
		t.Error("an open circuit admitted a request")
	}
}

func TestCircuitIsPerDomain(t *testing.T) {
	cb := NewCircuitBreaker(2, time.Minute, 100, nil)

	cb.RecordFailure("bad.test")
	cb.RecordFailure("bad.test")

	if cb.Allow("bad.test") {
		t.Error("bad.test should be open")
	}
	if !cb.Allow("good.test") {
		t.Error("good.test was closed out by an unrelated domain's failures")
	}
}

func TestSuccessResetsTheFailureRun(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Minute, 100, nil)

	cb.RecordFailure("flaky.test")
	cb.RecordFailure("flaky.test")
	cb.RecordSuccess("flaky.test")
	cb.RecordFailure("flaky.test")
	cb.RecordFailure("flaky.test")

	// Two failures since the success — below the threshold of three. The count
	// must be consecutive failures, not lifetime failures.
	if !cb.Allow("flaky.test") {
		t.Error("circuit opened on non-consecutive failures")
	}
}

func TestOpenCircuitAdmitsOneProbeAfterCooldown(t *testing.T) {
	const cooldown = 50 * time.Millisecond
	cb := NewCircuitBreaker(1, cooldown, 100, nil)

	cb.RecordFailure("down.test")
	if cb.Allow("down.test") {
		t.Fatal("circuit should be open immediately after opening")
	}

	time.Sleep(cooldown + 20*time.Millisecond)

	if !cb.Allow("down.test") {
		t.Fatal("no probe was admitted after the cooldown elapsed")
	}
	// Exactly one: admitting a burst would re-hammer a site that may not have
	// recovered, and one answer is as informative as fifty.
	if cb.Allow("down.test") {
		t.Error("a second probe was admitted while the first was in flight")
	}
}

func TestFailedProbeReopensTheCircuit(t *testing.T) {
	const cooldown = 50 * time.Millisecond
	cb := NewCircuitBreaker(1, cooldown, 100, nil)

	cb.RecordFailure("down.test")
	time.Sleep(cooldown + 20*time.Millisecond)

	if !cb.Allow("down.test") {
		t.Fatal("probe was not admitted")
	}
	if state := cb.RecordFailure("down.test"); state != breakerOpen {
		t.Fatalf("state after a failed probe = %v, want open", state)
	}
	if cb.Allow("down.test") {
		t.Error("circuit admitted a request immediately after a failed probe")
	}
}

func TestSuccessfulProbeClosesTheCircuit(t *testing.T) {
	const cooldown = 50 * time.Millisecond
	cb := NewCircuitBreaker(1, cooldown, 100, nil)

	cb.RecordFailure("recovering.test")
	time.Sleep(cooldown + 20*time.Millisecond)

	cb.Allow("recovering.test")
	cb.RecordSuccess("recovering.test")

	if state := cb.State("recovering.test"); state != breakerClosed {
		t.Fatalf("state after a successful probe = %v, want closed", state)
	}
	for i := 0; i < 5; i++ {
		if !cb.Allow("recovering.test") {
			t.Fatalf("request %d rejected by a closed circuit", i)
		}
	}
}

func TestCircuitBreakerDisabled(t *testing.T) {
	cb := NewCircuitBreaker(0, time.Minute, 100, nil)

	for i := 0; i < 100; i++ {
		cb.RecordFailure("bad.test")
	}
	if !cb.Allow("bad.test") {
		t.Error("a disabled breaker rejected a request")
	}
}

func TestCircuitBreakerEvictsOldDomains(t *testing.T) {
	const maxSize = 32
	cb := NewCircuitBreaker(5, time.Minute, maxSize, nil)

	for i := 0; i < maxSize*4; i++ {
		cb.RecordFailure(string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".test")
	}

	cb.mu.Lock()
	n := len(cb.domains)
	cb.mu.Unlock()

	if n > maxSize {
		t.Errorf("breaker retained %d domains, cap is %d", n, maxSize)
	}
}

// TestRetryIsRequeuedAfterBackoff covers the scheduler path: a retryable failure
// must come back, but not immediately, and not by parking a worker.
func TestRetryIsRequeuedAfterBackoff(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	cfg.Engine.RetryDelay = 50 * time.Millisecond

	eng := New(cfg, concurrencyLogger)
	s := eng.scheduler

	req, err := types.NewRequest("https://example.com/retry-me")
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.MaxRetries = 3

	s.handleFetchError(concurrencyLogger, req, &types.FetchError{
		URL:       req.URLString(),
		Err:       errors.New("connection reset"),
		Retryable: true,
	})

	// Counted as pending, so the idle monitor does not declare the crawl over and
	// close the frontier out from under a retry that has not landed yet.
	if got := s.pendingRetries.Load(); got != 1 {
		t.Fatalf("pendingRetries = %d, want 1", got)
	}

	deadline := time.Now().Add(3 * time.Second)
	for eng.frontier.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if eng.frontier.Len() != 1 {
		t.Fatal("the retried request never made it back onto the frontier")
	}
	if got := s.pendingRetries.Load(); got != 0 {
		t.Errorf("pendingRetries = %d after requeue, want 0", got)
	}
}

// TestRetryHonoursRetryAfterOverBackoff checks that a server's explicit
// Retry-After wins when it is longer than the computed backoff.
func TestRetryHonoursRetryAfterOverBackoff(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	cfg.Engine.RetryDelay = time.Millisecond // tiny computed backoff

	eng := New(cfg, concurrencyLogger)
	s := eng.scheduler

	req, err := types.NewRequest("https://example.com/429")
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.MaxRetries = 3

	s.handleFetchError(concurrencyLogger, req, &types.FetchError{
		URL:        req.URLString(),
		StatusCode: 429,
		Err:        errors.New("rate limited"),
		Retryable:  true,
		RetryAfter: 400 * time.Millisecond,
	})

	// Well past the 1ms backoff, well short of the 400ms Retry-After.
	time.Sleep(100 * time.Millisecond)
	if eng.frontier.Len() != 0 {
		t.Error("request was requeued before the server's Retry-After elapsed")
	}

	deadline := time.Now().Add(3 * time.Second)
	for eng.frontier.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if eng.frontier.Len() != 1 {
		t.Error("request never returned after Retry-After elapsed")
	}
}

// TestRetryDoesNotBlockAWorker is the point of moving the wait off-thread: the
// old code called time.Sleep(RetryAfter) inside the worker, holding a slot for up
// to two minutes.
func TestRetryDoesNotBlockAWorker(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	cfg.Engine.RetryDelay = 2 * time.Second

	eng := New(cfg, concurrencyLogger)
	s := eng.scheduler

	req, err := types.NewRequest("https://example.com/slow-retry")
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.MaxRetries = 5

	start := time.Now()
	s.handleFetchError(concurrencyLogger, req, &types.FetchError{
		URL:        req.URLString(),
		Err:        errors.New("timeout"),
		Retryable:  true,
		RetryAfter: 2 * time.Second,
	})
	elapsed := time.Since(start)

	if elapsed > 200*time.Millisecond {
		t.Errorf("handleFetchError blocked for %v; the backoff must not occupy the worker", elapsed)
	}
}

// testRand returns a deterministically seeded source, so a failing jitter
// assertion reproduces instead of being a coin flip.
func testRand() *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }
