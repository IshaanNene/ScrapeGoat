package engine

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/clock"
	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
)

// The seam is only worth having if it actually removes nondeterminism. These tests
// assert the property rather than the plumbing: same seed, same decisions.

// TestBackoffIsReproducibleWithASeed is the property replay depends on. Two engines
// with the same seed must produce the same retry delays, or a replayed crawl
// diverges from the recorded one the first time anything fails.
func TestBackoffIsReproducibleWithASeed(t *testing.T) {
	const attempts = 50

	draw := func() []time.Duration {
		rng := rand.New(rand.NewPCG(42, 1024))
		out := make([]time.Duration, attempts)
		for i := range out {
			out[i] = backoffFor(rng, time.Second, i%8+1)
		}
		return out
	}

	first, second := draw(), draw()
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("draw %d differs between runs: %v vs %v — backoff is not seed-reproducible",
				i, first[i], second[i])
		}
	}

	// And a different seed must actually produce different delays, or the "seed"
	// is decorative and the test above proves nothing.
	other := rand.New(rand.NewPCG(7, 7))
	differs := false
	for i := 0; i < attempts; i++ {
		if backoffFor(other, time.Second, i%8+1) != first[i] {
			differs = true
			break
		}
	}
	if !differs {
		t.Error("a different seed produced identical delays; the seed is not being used")
	}
}

// TestEngineAcceptsInjectedSources checks the wiring: an engine built with
// WithClock and WithRand uses them rather than quietly falling back to the real
// ones, which is the failure mode that makes replay silently stop working.
func TestEngineAcceptsInjectedSources(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	clk := clock.System()

	eng := New(testutil.LoopbackConfig(), concurrencyLogger, WithClock(clk), WithRand(rng))

	if eng.rand != rng {
		t.Error("WithRand was ignored")
	}
	if eng.clock != clk {
		t.Error("WithClock was ignored")
	}

	// The clock must reach the components that schedule, not just sit on the
	// engine — those are what a simulated clock has to control.
	if eng.frontier.clock != clk {
		t.Error("frontier did not receive the engine's clock")
	}
	if eng.scheduler.throttler.clock != clk {
		t.Error("throttler did not receive the engine's clock")
	}
	if eng.scheduler.breaker.clock != clk {
		t.Error("circuit breaker did not receive the engine's clock")
	}
	if eng.robots.clock != clk {
		t.Error("robots manager did not receive the engine's clock")
	}
	if eng.checkpoint.clock != clk {
		t.Error("checkpoint manager did not receive the engine's clock")
	}
}

// TestDefaultsAreTheRealThing keeps the seam invisible to callers who do not want
// it: no options must mean a working engine on the wall clock.
func TestDefaultsAreTheRealThing(t *testing.T) {
	eng := New(testutil.LoopbackConfig(), concurrencyLogger)

	if eng.clock == nil {
		t.Fatal("engine built with no options has a nil clock")
	}
	if eng.rand == nil {
		t.Fatal("engine built with no options has a nil random source")
	}

	before := eng.clock.Now()
	time.Sleep(time.Millisecond)
	if !eng.clock.Now().After(before) {
		t.Error("default clock does not advance")
	}
}

// TestNilOptionIsIgnored guards the variadic path against a nil in the slice,
// which is easy to produce from a conditional option list.
func TestNilOptionIsIgnored(t *testing.T) {
	eng := New(testutil.LoopbackConfig(), concurrencyLogger, nil, WithRand(nil), nil)
	if eng.rand == nil {
		t.Error("a nil option or nil source should fall back to a default, not nil")
	}
}
