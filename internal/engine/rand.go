package engine

import (
	"math/rand/v2"
	"sync"
)

// randSource is the slice of randomness the crawl path actually uses. Narrow on
// purpose: anything reaching for more should say so by widening this.
type randSource interface {
	Int64N(n int64) int64
}

// lockedRand serialises draws from the engine's injected *rand.Rand.
//
// math/rand/v2's *Rand is not safe for concurrent use — only the package-level
// functions are, and those are exactly what the determinism boundary forbids. The
// engine's single *Rand was being drawn from by every worker at once, on the retry
// path, so a site returning errors to a hundred workers produced a hundred
// concurrent unsynchronised mutations of one PCG state. The race detector catches it
// only when two retries land in the same instant, which is why it surfaced as an
// occasional CI failure rather than a reproducible one.
//
// The lock removes the race. It does not make retry delays reproducible under
// concurrency: the values drawn are still handed out in whatever order the workers
// arrive, so replaying a crawl with the same seed reproduces the same multiset of
// delays but not the same assignment of delays to requests. Deriving each delay from
// the request's own identity would fix that, and is the real fix; it is a design
// change rather than a bug fix and is noted in ROADMAP.md.
type lockedRand struct {
	mu sync.Mutex
	r  *rand.Rand
}

func newLockedRand(r *rand.Rand) *lockedRand { return &lockedRand{r: r} }

func (l *lockedRand) Int64N(n int64) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.Int64N(n)
}
