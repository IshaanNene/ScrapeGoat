package clock

import (
	"testing"
	"time"
)

func TestSystemClockAdvances(t *testing.T) {
	c := System()

	before := c.Now()
	time.Sleep(2 * time.Millisecond)
	after := c.Now()

	if !after.After(before) {
		t.Errorf("Now() did not advance: %v then %v", before, after)
	}
	if d := c.Since(before); d <= 0 {
		t.Errorf("Since() = %v, want positive", d)
	}
}

func TestSystemTimerFires(t *testing.T) {
	timer := System().NewTimer(5 * time.Millisecond)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-time.After(2 * time.Second):
		t.Fatal("timer never fired")
	}
}

func TestSystemTimerStopPreventsFiring(t *testing.T) {
	timer := System().NewTimer(50 * time.Millisecond)

	if !timer.Stop() {
		t.Fatal("Stop() reported the timer had already fired")
	}

	select {
	case <-timer.C:
		t.Error("timer fired after Stop")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSystemTickerRepeats(t *testing.T) {
	ticker := System().NewTicker(2 * time.Millisecond)
	defer ticker.Stop()

	for i := 0; i < 3; i++ {
		select {
		case <-ticker.C:
		case <-time.After(2 * time.Second):
			t.Fatalf("ticker did not produce tick %d", i+1)
		}
	}
}

func TestSystemAfter(t *testing.T) {
	select {
	case <-System().After(5 * time.Millisecond):
	case <-time.After(2 * time.Second):
		t.Fatal("After() never fired")
	}
}

// TestNilTimerAndTickerAreSafe covers the zero value. A Timer obtained from a
// future simulated clock that declines to schedule anything should not panic on
// Stop, and neither should a struct literal in a test.
func TestNilTimerAndTickerAreSafe(t *testing.T) {
	var timer *Timer
	if timer.Stop() {
		t.Error("nil timer reported a successful Stop")
	}
	(&Timer{}).Stop()

	var ticker *Ticker
	ticker.Stop()
	(&Ticker{}).Stop()
}

func TestOrSystem(t *testing.T) {
	if OrSystem(nil) == nil {
		t.Fatal("OrSystem(nil) returned nil")
	}

	// A non-nil clock must be passed through unchanged, or a caller that
	// deliberately supplied a simulated clock would silently get the real one —
	// which is the failure mode that makes replay quietly stop working.
	custom := System()
	if got := OrSystem(custom); got != custom {
		t.Error("OrSystem replaced a non-nil clock")
	}
}

// TestSystemClockIsShareable pins that the production clock holds no state, so
// constructors can take the same value without coordinating.
func TestSystemClockIsShareable(t *testing.T) {
	if System() != System() {
		t.Error("System() returns distinct values; it is expected to be stateless")
	}
}
