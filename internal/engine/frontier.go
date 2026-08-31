package engine

import (
	"container/heap"
	"context"

	"sync"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/clock"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// Frontier is a thread-safe priority queue of crawl requests.
//
// Waiting is event-driven: a blocked Pop parks on a channel and is woken by the
// Push that gave it work, or by Close. The previous implementation polled the heap
// every 50 ms, which put a ~25 ms median floor under every dequeue and burned two
// lock acquisitions per worker per second while idle.
type Frontier struct {
	clock  clock.Clock
	mu     sync.Mutex
	pq     priorityQueue
	closed bool

	// notify wakes one waiting Pop. Capacity 1 with a non-blocking send: if a
	// wakeup is already pending, another is redundant, since the woken Pop
	// re-checks the heap and any further waiters are covered by their own Push.
	notify chan struct{}

	// closedCh is closed exactly once by Close, which broadcasts to every waiter
	// at once — a buffered channel could only wake them one at a time.
	closedCh chan struct{}
}

// NewFrontier creates a new Frontier. A nil clock means the system clock.
func NewFrontier(clk clock.Clock) *Frontier {
	f := &Frontier{
		clock:    clock.OrSystem(clk),
		pq:       make(priorityQueue, 0, 1024),
		notify:   make(chan struct{}, 1),
		closedCh: make(chan struct{}),
	}
	heap.Init(&f.pq)
	return f
}

// wake signals one waiting Pop. Must be called with f.mu held or immediately after
// releasing it; the non-blocking send means it never stalls the pusher.
func (f *Frontier) wake() {
	select {
	case f.notify <- struct{}{}:
	default:
	}
}

// Push adds a request to the frontier.
func (f *Frontier) Push(req *types.Request) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	heap.Push(&f.pq, &pqItem{request: req, priority: req.Priority})
	f.mu.Unlock()

	f.wake()
}

// Pop removes and returns the highest-priority request, blocking until one is
// available, the frontier is closed, or ctx is cancelled. Returns nil in the
// latter two cases.
func (f *Frontier) Pop(ctx context.Context) *types.Request {
	for {
		f.mu.Lock()
		if f.pq.Len() > 0 {
			item := popPQ(&f.pq)
			remaining := f.pq.Len()
			f.mu.Unlock()

			// Push sends at most one wakeup, so a burst of N pushes wakes one
			// worker. Hand the baton on while work is left, or the other waiters
			// sleep through a full queue.
			if remaining > 0 {
				f.wake()
			}
			return item.request
		}
		closed := f.closed
		f.mu.Unlock()

		if closed {
			return nil
		}

		select {
		case <-ctx.Done():
			return nil
		case <-f.closedCh:
			// Drain anything pushed before the close before giving up.
			return f.TryPop()
		case <-f.notify:
		}
	}
}

// DomainGate decides whether a request to a given domain may be dispatched now.
// Throttler implements it; a nil gate means everything is always ready.
type DomainGate interface {
	// Ready reports whether the domain could be dispatched now, without
	// consuming anything. Used while scanning candidates, so that inspecting a
	// request the worker then declines does not spend its budget.
	Ready(domain string) bool

	// Claim consumes the domain's allowance and reports whether it succeeded.
	// Called only for the request actually being taken.
	Claim(domain string) bool

	// Delay reports how long until the domain is ready.
	Delay(domain string) time.Duration
}

// PopReady removes and returns the highest-priority request whose domain is ready
// to be fetched, blocking until one exists, the frontier closes, or ctx is done.
//
// This is what stops a throttled domain from parking a worker. The old scheduler
// dequeued first and *then* slept out the politeness delay while holding the
// domain's lock — so one slow domain occupied every worker slot in turn and every
// other domain starved. Here a worker skips past a throttled domain to whatever
// else is runnable, and only waits when nothing at all is.
func (f *Frontier) PopReady(ctx context.Context, gate DomainGate) *types.Request {
	if gate == nil {
		return f.Pop(ctx)
	}

	for {
		f.mu.Lock()

		// Highest-priority runnable candidate. The heap is ordered by priority,
		// not by domain, so this is a scan rather than a peek — but it stops at
		// the first ready entry, which for an unthrottled crawl is index 0.
		best := -1
		var soonest time.Duration
		haveSoonest := false

		for i, item := range f.pq {
			domain := item.request.RegistrableDomain()
			if gate.Ready(domain) {
				if best == -1 || f.pq[i].priority < f.pq[best].priority {
					best = i
				}
				continue
			}
			if d := gate.Delay(domain); !haveSoonest || d < soonest {
				soonest, haveSoonest = d, true
			}
		}

		if best != -1 {
			item := removePQ(&f.pq, best)
			remaining := f.pq.Len()
			f.mu.Unlock()

			// Claim can still fail if another worker took the same domain's
			// token between the scan and here; put the request back and retry.
			if !gate.Claim(item.request.RegistrableDomain()) {
				f.Push(item.request)
				continue
			}
			if remaining > 0 {
				f.wake()
			}
			return item.request
		}

		closed := f.closed
		f.mu.Unlock()

		if closed {
			// Shutting down. Hand back whatever remains without waiting out
			// politeness delays — the frontier is closed precisely because
			// dispatch is ending, and the wait could be hours. Mirrors Pop,
			// which also drains one item per caller on close so a final Push
			// racing with Close is not lost.
			return f.TryPop()
		}

		// Wait for whichever comes first: new work, the frontier closing, or the
		// earliest throttled domain becoming ready. The timer only exists when
		// something is actually throttled, so an idle crawl still costs nothing.
		// Stopped explicitly rather than deferred: this is a loop, and a deferred
		// Stop would pile up one timer per iteration until the function returns.
		var timer *clock.Timer
		var timerC <-chan time.Time
		if haveSoonest {
			timer = f.clock.NewTimer(soonest)
			timerC = timer.C
		}

		select {
		case <-ctx.Done():
			stopTimer(timer)
			return nil
		case <-f.closedCh:
			stopTimer(timer)
			// Loop round; the closed check at the top of the next iteration
			// drains and returns.
		case <-f.notify:
			stopTimer(timer)
		case <-timerC:
		}
	}
}

func stopTimer(t *clock.Timer) {
	if t != nil {
		t.Stop()
	}
}

// TryPop attempts a non-blocking dequeue. Returns nil if empty.
func (f *Frontier) TryPop() *types.Request {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.pq.Len() == 0 {
		return nil
	}

	item := popPQ(&f.pq)
	return item.request
}

// Len returns the number of requests in the frontier.
func (f *Frontier) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pq.Len()
}

// IsEmpty returns true if the frontier is empty.
func (f *Frontier) IsEmpty() bool {
	return f.Len() == 0
}

// Close closes the frontier, unblocking every waiting Pop. Safe to call more than
// once — the idle monitor and an explicit Stop can both reach it.
func (f *Frontier) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed {
		return
	}
	f.closed = true
	close(f.closedCh)
}

// IsClosed returns true if the frontier has been closed.
func (f *Frontier) IsClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// Snapshot returns a copy of all queued requests without removing them.
// Safe for use during checkpointing while the crawl is running.
func (f *Frontier) Snapshot() []*types.Request {
	f.mu.Lock()
	defer f.mu.Unlock()

	requests := make([]*types.Request, f.pq.Len())
	for i, item := range f.pq {
		requests[i] = item.request
	}
	return requests
}

// Drain returns all remaining requests, removing them from the queue.
func (f *Frontier) Drain() []*types.Request {
	f.mu.Lock()
	defer f.mu.Unlock()

	requests := make([]*types.Request, 0, f.pq.Len())
	for f.pq.Len() > 0 {
		item := popPQ(&f.pq)
		requests = append(requests, item.request)
	}
	return requests
}

// RestoreAll adds multiple requests back (for checkpoint restore).
func (f *Frontier) RestoreAll(reqs []*types.Request) {
	f.mu.Lock()
	for _, req := range reqs {
		heap.Push(&f.pq, &pqItem{request: req, priority: req.Priority})
	}
	n := f.pq.Len()
	f.mu.Unlock()

	if n > 0 {
		f.wake()
	}
}

// --- Priority Queue Implementation ---

type pqItem struct {
	request  *types.Request
	priority int
	index    int
}

type priorityQueue []*pqItem

func (pq priorityQueue) Len() int { return len(pq) }

func (pq priorityQueue) Less(i, j int) bool {
	// Lower priority value = higher priority
	return pq[i].priority < pq[j].priority
}

func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *priorityQueue) Push(x any) {
	n := len(*pq)
	// heap.Interface hands back `any`; see popPQ for why this is a hard assertion.
	item, ok := x.(*pqItem)
	if !ok {
		panic("frontier: priorityQueue given a non-*pqItem")
	}
	item.index = n
	*pq = append(*pq, item)
}

func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // GC
	item.index = -1
	*pq = old[:n-1]
	return item
}

// popPQ and removePQ narrow container/heap's `any` back to the only type the
// queue ever holds.
//
// The assertion cannot fail: pq is private and every Push goes through this file.
// Written as a helper rather than five inline assertions so the invariant is
// stated once, and left as a hard assertion rather than the comma-ok form because
// a violation would be a bug in this file — a panic names it, a zero value hides
// it until something downstream misbehaves for unrelated-looking reasons.
func popPQ(pq *priorityQueue) *pqItem {
	item, ok := heap.Pop(pq).(*pqItem)
	if !ok {
		panic("frontier: priorityQueue held a non-*pqItem")
	}
	return item
}

func removePQ(pq *priorityQueue, i int) *pqItem {
	item, ok := heap.Remove(pq, i).(*pqItem)
	if !ok {
		panic("frontier: priorityQueue held a non-*pqItem")
	}
	return item
}
