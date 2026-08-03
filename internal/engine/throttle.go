package engine

import (
	"container/list"
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// defaultMaxThrottleSlots bounds how many per-domain limiters are kept.
//
// The previous implementation kept one entry per domain forever. A broad crawl
// across millions of hosts leaked memory monotonically, with no eviction anywhere.
const defaultMaxThrottleSlots = 10_000

// Throttler enforces politeness per registrable domain.
//
// It replaces a `time.Sleep` performed while holding the domain's mutex, after the
// worker had already dequeued a request. That arrangement had two costs. The sleep
// occupied a worker slot, so with `politeness_delay: 1s` and 100 workers on one
// domain the hundredth worker blocked for ~100 seconds doing nothing. And because
// the delay was applied after dequeue rather than at scheduling time, a single slow
// domain parked the entire pool and starved every *other* domain — effective
// concurrency on a single-domain crawl collapsed to one.
//
// Two things change here. Waiting is done by a rate.Limiter rather than a sleep
// under a lock, so the domain's own mutex is never held across the wait and
// concurrent callers for different domains never contend. And Allow lets a worker
// ask whether a domain is ready *before* committing to it, so the scheduler can
// skip a throttled domain instead of parking on it.
type Throttler struct {
	mu    sync.Mutex
	slots map[string]*list.Element // domain -> element in lru
	lru   *list.List               // front = most recently used

	limit   rate.Limit
	burst   int
	maxSize int
}

// throttleSlot is the per-domain state held in the LRU.
type throttleSlot struct {
	domain  string
	limiter *rate.Limiter
}

// NewThrottler builds a throttler enforcing one request per delay per domain.
// A delay of zero or less disables throttling entirely.
func NewThrottler(delay time.Duration, maxSlots int) *Throttler {
	if maxSlots <= 0 {
		maxSlots = defaultMaxThrottleSlots
	}

	t := &Throttler{
		slots:   make(map[string]*list.Element),
		lru:     list.New(),
		burst:   1,
		maxSize: maxSlots,
	}

	if delay > 0 {
		t.limit = rate.Every(delay)
	} else {
		t.limit = rate.Inf
	}
	return t
}

// Enabled reports whether the throttler will delay anything.
func (t *Throttler) Enabled() bool { return t.limit != rate.Inf }

// limiterFor returns the domain's limiter, creating it if absent and evicting the
// least recently used slot when the map is full.
func (t *Throttler) limiterFor(domain string) *rate.Limiter {
	t.mu.Lock()
	defer t.mu.Unlock()

	if el, ok := t.slots[domain]; ok {
		t.lru.MoveToFront(el)
		return el.Value.(*throttleSlot).limiter
	}

	// Evicting the least recently used domain loses its rate history, which means
	// the next request to it may go out sooner than the delay implies. That is the
	// deliberate trade against unbounded memory on a broad crawl: the slot has not
	// been touched in maxSize distinct domains' worth of traffic, so the delay has
	// almost certainly elapsed anyway.
	for t.lru.Len() >= t.maxSize {
		oldest := t.lru.Back()
		if oldest == nil {
			break
		}
		t.lru.Remove(oldest)
		delete(t.slots, oldest.Value.(*throttleSlot).domain)
	}

	limiter := rate.NewLimiter(t.limit, t.burst)
	t.slots[domain] = t.lru.PushFront(&throttleSlot{domain: domain, limiter: limiter})
	return limiter
}

// Allow reports whether a request to domain may proceed immediately, consuming a
// token if so. It never blocks, so a worker can use it to decide whether to take a
// request or leave it for later without ever parking on a slow domain.
func (t *Throttler) Allow(domain string) bool {
	if !t.Enabled() {
		return true
	}
	return t.limiterFor(domain).Allow()
}

// Ready reports whether domain could be dispatched now, without consuming its
// allowance. The scan in Frontier.PopReady inspects many candidates and takes at
// most one, so checking must not spend anything.
func (t *Throttler) Ready(domain string) bool {
	return t.Delay(domain) == 0
}

// Claim consumes domain's allowance for the request actually being taken.
func (t *Throttler) Claim(domain string) bool { return t.Allow(domain) }

// Delay reports how long until domain is ready. It satisfies DomainGate.
func (t *Throttler) Delay(domain string) time.Duration { return t.Reserve(domain) }

// Wait blocks until a request to domain may proceed, or ctx is done.
//
// This is the fallback for when the scheduler has already committed to a request —
// the frontier held nothing else runnable — so someone has to wait. Unlike the old
// sleep, it holds no lock while waiting, so other domains are unaffected.
func (t *Throttler) Wait(ctx context.Context, domain string) error {
	if !t.Enabled() {
		return nil
	}
	return t.limiterFor(domain).Wait(ctx)
}

// Reserve returns how long a request to domain must wait, without consuming a
// token. Used to decide how long to park when nothing is runnable, and to scan
// candidates the worker may not end up taking.
//
// Deliberately not built on ReserveN+Cancel: rate.Reservation.Cancel restores the
// token only when the reservation's act time is still in the future, so cancelling
// a zero-delay reservation is a no-op and the token stays spent. That made the
// "non-consuming" probe consume one token per candidate inspected.
func (t *Throttler) Reserve(domain string) time.Duration {
	if !t.Enabled() {
		return 0
	}

	limiter := t.limiterFor(domain)
	tokens := limiter.TokensAt(time.Now())
	if tokens >= 1 {
		return 0
	}

	// rate.Limit is events per second, so the shortfall converts directly.
	limit := float64(limiter.Limit())
	if limit <= 0 {
		return 0
	}
	return time.Duration((1 - tokens) / limit * float64(time.Second))
}

// Len returns the number of tracked domains. Used by tests to assert eviction.
func (t *Throttler) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lru.Len()
}
