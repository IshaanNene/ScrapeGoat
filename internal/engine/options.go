package engine

import (
	"math/rand/v2"

	"github.com/IshaanNene/ScrapeGoat/internal/clock"
)

// Option configures an Engine at construction.
//
// Variadic options rather than extra parameters, so that callers who do not care
// about determinism — which is most of them — are not made to thread a clock and a
// random source through their own code to get a default crawler.
type Option func(*engineOptions)

type engineOptions struct {
	clock clock.Clock
	rand  *rand.Rand
}

// WithClock sets the engine's source of time. Nil means the system clock.
//
// Supplying a controlled clock is what makes a crawl replayable and what will let
// simulation testing advance an hour without waiting one.
func WithClock(c clock.Clock) Option {
	return func(o *engineOptions) { o.clock = c }
}

// WithRand sets the engine's source of randomness. Nil means a source seeded from
// the operating system.
//
// The engine uses randomness for retry-backoff jitter. Seeding it deliberately
// makes a crawl's entropy part of its identity: two runs with the same seed and
// the same responses back off identically, which is a precondition for replay.
func WithRand(r *rand.Rand) Option {
	return func(o *engineOptions) { o.rand = r }
}

// resolve applies defaults to whatever the caller supplied.
func resolve(opts []Option) engineOptions {
	var o engineOptions
	for _, apply := range opts {
		if apply != nil {
			apply(&o)
		}
	}

	o.clock = clock.OrSystem(o.clock)
	if o.rand == nil {
		// Seeded from the OS: unpredictable by default, which is what jitter needs
		// when nobody has asked for reproducibility. This is the one place in the
		// engine allowed to draw from global entropy — it is the seed source, not
		// a use of randomness in the crawl path.
		o.rand = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64())) //determinism:allow seed source
	}
	return o
}
