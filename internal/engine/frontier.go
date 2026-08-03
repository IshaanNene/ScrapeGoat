package engine

import (
	"container/heap"
	"context"
	"sync"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// Frontier is a thread-safe priority queue of crawl requests.
//
// Waiting is event-driven: a blocked Pop parks on a channel and is woken by the
// Push that gave it work, or by Close. The previous implementation polled the heap
// every 50 ms, which put a ~25 ms median floor under every dequeue and burned two
// lock acquisitions per worker per second while idle.
type Frontier struct {
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

// NewFrontier creates a new Frontier.
func NewFrontier() *Frontier {
	f := &Frontier{
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
			item := heap.Pop(&f.pq).(*pqItem)
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

// TryPop attempts a non-blocking dequeue. Returns nil if empty.
func (f *Frontier) TryPop() *types.Request {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.pq.Len() == 0 {
		return nil
	}

	item := heap.Pop(&f.pq).(*pqItem)
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
		item := heap.Pop(&f.pq).(*pqItem)
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
	item := x.(*pqItem)
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
