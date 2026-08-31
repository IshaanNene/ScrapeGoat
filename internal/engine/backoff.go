package engine

import (
	"math"

	"sync"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/clock"
)

const (
	// maxBackoff caps the exponential growth. Beyond a minute the request is
	// almost certainly not coming back within this crawl.
	maxBackoff = 60 * time.Second

	// breakerDefaultCooldown is how long an open circuit stays open before a
	// single probe is allowed through.
	breakerDefaultCooldown = 30 * time.Second
)

// backoffFor returns the delay before retry number n (1-based), with full jitter.
//
// Retries previously went straight back onto the frontier with no delay at all,
// which turns a struggling server into a tight retry loop from every worker at
// once — the crawler's response to a site falling over was to hit it harder.
//
// The random source is injected rather than global so that a crawl's entropy is
// part of its identity: two runs with the same seed and the same responses back
// off identically, which replay requires.
//
// Jitter is full rather than fixed because the failures that trigger retries are
// usually correlated: a hundred workers hit the same 503 in the same second, and
// without jitter they all come back in the same later second too, reproducing the
// thundering herd at every step.
func backoffFor(rng randSource, base time.Duration, n int) time.Duration {
	if base <= 0 || n <= 0 {
		return 0
	}

	// 2^(n-1) * base, saturating rather than overflowing.
	shift := float64(n - 1)
	if shift > 20 {
		shift = 20
	}
	d := time.Duration(float64(base) * math.Pow(2, shift))
	if d > maxBackoff || d <= 0 {
		d = maxBackoff
	}

	// Full jitter: uniform in [0, d). AWS's "Exponential Backoff and Jitter"
	// measured this as beating both no jitter and equal jitter on contention.
	return time.Duration(rng.Int64N(int64(d) + 1))
}

// breakerState is a domain's circuit state.
type breakerState int

const (
	breakerClosed   breakerState = iota // normal
	breakerOpen                         // failing; requests rejected
	breakerHalfOpen                     // one probe permitted
)

func (s breakerState) String() string {
	switch s {
	case breakerOpen:
		return "open"
	case breakerHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}

// domainBreaker tracks one domain's health.
type domainBreaker struct {
	state       breakerState
	consecutive int
	openedAt    time.Time
	probing     bool
}

// CircuitBreaker stops a crawl from hammering a domain that is consistently
// failing.
//
// Without one, a site that goes down absorbs the full request budget: every URL
// queued for it is attempted, retried to exhaustion, and fails, while the crawler
// reports the wasted work as ordinary errors. The breaker turns that into a fast
// rejection and gives the site room to recover.
//
// Sized by the same LRU discipline as the throttler, since a broad crawl sees
// unboundedly many domains.
type CircuitBreaker struct {
	clock    clock.Clock
	mu       sync.Mutex
	domains  map[string]*domainBreaker
	order    []string // insertion order, for cheap bounded eviction
	maxSize  int
	failures int
	cooldown time.Duration
}

// NewCircuitBreaker builds a breaker. A threshold of zero or less disables it.
func NewCircuitBreaker(threshold int, cooldown time.Duration, maxSize int, clk clock.Clock) *CircuitBreaker {
	if cooldown <= 0 {
		cooldown = breakerDefaultCooldown
	}
	if maxSize <= 0 {
		maxSize = defaultMaxThrottleSlots
	}
	return &CircuitBreaker{
		clock:    clock.OrSystem(clk),
		domains:  make(map[string]*domainBreaker),
		maxSize:  maxSize,
		failures: threshold,
		cooldown: cooldown,
	}
}

// Enabled reports whether the breaker will reject anything.
func (cb *CircuitBreaker) Enabled() bool { return cb != nil && cb.failures > 0 }

func (cb *CircuitBreaker) get(domain string) *domainBreaker {
	if b, ok := cb.domains[domain]; ok {
		return b
	}
	for len(cb.order) >= cb.maxSize {
		oldest := cb.order[0]
		cb.order = cb.order[1:]
		delete(cb.domains, oldest)
	}
	b := &domainBreaker{}
	cb.domains[domain] = b
	cb.order = append(cb.order, domain)
	return b
}

// Allow reports whether a request to domain may be attempted.
//
// An open circuit lets exactly one probe through once the cooldown has elapsed.
// Letting several through would re-hammer a site that has not recovered, and the
// answer from one probe is as informative as the answer from fifty.
func (cb *CircuitBreaker) Allow(domain string) bool {
	if !cb.Enabled() {
		return true
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	b := cb.get(domain)
	switch b.state {
	case breakerClosed:
		return true

	case breakerOpen:
		if cb.clock.Since(b.openedAt) < cb.cooldown {
			return false
		}
		b.state = breakerHalfOpen
		b.probing = true
		return true

	case breakerHalfOpen:
		if b.probing {
			return false // a probe is already in flight
		}
		b.probing = true
		return true
	}
	return true
}

// RecordSuccess reports a successful fetch, closing the circuit.
func (cb *CircuitBreaker) RecordSuccess(domain string) {
	if !cb.Enabled() {
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	b := cb.get(domain)
	b.consecutive = 0
	b.probing = false
	b.state = breakerClosed
}

// RecordFailure reports a failed fetch. Returns the resulting state, so the caller
// can log the closed→open transition rather than every failure.
func (cb *CircuitBreaker) RecordFailure(domain string) breakerState {
	if !cb.Enabled() {
		return breakerClosed
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()

	b := cb.get(domain)
	b.consecutive++
	b.probing = false

	// A failed probe re-opens the circuit and restarts the cooldown, rather than
	// allowing another probe immediately.
	if b.state == breakerHalfOpen || b.consecutive >= cb.failures {
		b.state = breakerOpen
		b.openedAt = cb.clock.Now()
	}
	return b.state
}

// State returns a domain's current circuit state. For tests and diagnostics.
func (cb *CircuitBreaker) State(domain string) breakerState {
	if !cb.Enabled() {
		return breakerClosed
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.get(domain).state
}
