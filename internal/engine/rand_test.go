package engine

import (
	"math/rand/v2"
	"sync"
	"testing"
	"time"
)

// TestBackoffIsRaceFreeUnderConcurrency reproduces the condition that made this a
// bug in production rather than in theory: a domain returning errors hands every
// worker a retry at the same moment, and every one of them draws jitter from the
// engine's single random source.
//
// Run with -race. Without the lock this fails; without concurrency it never would,
// which is why it went unnoticed.
func TestBackoffIsRaceFreeUnderConcurrency(t *testing.T) {
	rng := newLockedRand(rand.New(rand.NewPCG(1, 2)))

	const workers = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			for j := 0; j < 50; j++ {
				backoffFor(rng, time.Second, n%8+1)
			}
		}(i)
	}
	close(start)
	wg.Wait()
}

// TestLockedRandStaysInRange checks the wrapper forwards rather than reinvents.
func TestLockedRandStaysInRange(t *testing.T) {
	rng := newLockedRand(rand.New(rand.NewPCG(3, 4)))
	for i := 0; i < 1000; i++ {
		if v := rng.Int64N(10); v < 0 || v >= 10 {
			t.Fatalf("Int64N(10) = %d, out of range", v)
		}
	}
}
