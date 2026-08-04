// Package clock is the engine's source of time.
//
// Every part of the crawl path that needs the current time, a timer, or a ticker
// takes it from a Clock rather than calling time.Now or time.NewTimer directly.
// The production implementation is a thin pass-through to the standard library and
// costs nothing measurable; the point is that a second implementation is possible.
//
// # Why
//
// A crawl is not currently reproducible: run it twice and you get different
// output. That costs four things — datasets cannot be audited, concurrency bugs
// cannot be reproduced, crawler policy cannot be evaluated, and incremental
// re-crawl has no foundation to build on. See docs/design/0001-deterministic-crawl.md.
//
// Time is one of three sources of nondeterminism in the engine, alongside
// randomness and map iteration order. It is the one that needs an abstraction,
// because a simulated clock has to control not just what Now returns but when
// timers fire — a test that wants to advance an hour cannot wait an hour.
//
// # The interface is shaped by the simulated implementation, not the real one
//
// A Clock that only exposed Now would be trivial and useless: the scheduler's idle
// monitor, the frontier's politeness wait, and the retry backoff are all driven by
// timers, so a simulated clock that cannot fire them controls nothing. Timer and
// Ticker are therefore part of the interface, even though the production versions
// are one-line wrappers.
//
// The simulated implementation lands with deterministic simulation testing; this
// package exists first so that the call sites are already in the right shape when
// it does.
package clock

import "time"

// Clock provides the time-related operations the engine needs.
//
// Implementations must be safe for concurrent use.
type Clock interface {
	// Now returns the current time.
	Now() time.Time

	// Since returns the time elapsed since t. Equivalent to Now().Sub(t), but
	// named to match the standard library so call sites read the same.
	Since(t time.Time) time.Duration

	// Sleep blocks for at least d.
	//
	// Present for completeness, but the crawl path should prefer NewTimer inside
	// a select: a bare Sleep cannot be cancelled, which is how the politeness
	// throttle used to hold a worker slot hostage for the length of the delay.
	Sleep(d time.Duration)

	// NewTimer returns a Timer that fires once after d.
	NewTimer(d time.Duration) *Timer

	// NewTicker returns a Ticker that fires every d. The caller must Stop it.
	NewTicker(d time.Duration) *Ticker

	// After is shorthand for NewTimer(d).C, for use in a select where the timer
	// does not need stopping. Note that under the real clock the underlying timer
	// is not garbage collected until it fires, so a long duration in a hot loop
	// should use NewTimer and Stop instead.
	After(d time.Duration) <-chan time.Time
}

// Timer fires once, on C.
//
// A struct rather than an interface: the channel has to be a field for `select`
// to read it, and a method returning a channel would allocate differently between
// implementations.
type Timer struct {
	// C receives the time when the timer fires.
	C <-chan time.Time

	stop func() bool
}

// Stop prevents the timer from firing, reporting whether it did so before the
// timer fired or was stopped.
func (t *Timer) Stop() bool {
	if t == nil || t.stop == nil {
		return false
	}
	return t.stop()
}

// Ticker fires repeatedly on C until stopped.
type Ticker struct {
	// C receives the time on each tick.
	C <-chan time.Time

	stop func()
}

// Stop halts the ticker. It does not close C.
func (t *Ticker) Stop() {
	if t == nil || t.stop == nil {
		return
	}
	t.stop()
}

// System returns a Clock backed by the standard library.
//
// This is the production clock and the default everywhere. It holds no state, so
// a single value can be shared.
func System() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time                  { return time.Now() }
func (systemClock) Since(t time.Time) time.Duration { return time.Since(t) }
func (systemClock) Sleep(d time.Duration)           { time.Sleep(d) }

func (systemClock) NewTimer(d time.Duration) *Timer {
	t := time.NewTimer(d)
	return &Timer{C: t.C, stop: t.Stop}
}

func (systemClock) NewTicker(d time.Duration) *Ticker {
	t := time.NewTicker(d)
	return &Ticker{C: t.C, stop: t.Stop}
}

func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// OrSystem returns c, or the system clock if c is nil.
//
// Lets every constructor accept a nil clock and mean "the real one", so callers
// that do not care about determinism are not made to thread one through.
func OrSystem(c Clock) Clock {
	if c == nil {
		return System()
	}
	return c
}
