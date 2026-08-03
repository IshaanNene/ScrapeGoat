package engine

import (
	"context"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// The number that matters for the scheduler is *wake latency*: a worker is parked
// on an empty frontier, a request arrives, and the question is how long until the
// worker has it.
//
// A naive throughput benchmark does not measure this. If the producer runs ahead
// of the consumer, the queue is never empty, the polling loop's TryPop always hits
// on the first try, and the 50ms sleep never executes — so the old and new
// implementations benchmark within noise of each other while behaving completely
// differently in a real crawl, where a worker finishing a fetch routinely arrives
// at an empty frontier.
//
// These two benchmarks force the parked state before every measurement.

// parkDelay gives the consumer goroutine time to actually block before the push.
const parkDelay = 2 * time.Millisecond

// BenchmarkWakeLatencyEventDriven measures the current implementation: the worker
// parks in Pop and is woken by the Push.
func BenchmarkWakeLatencyEventDriven(b *testing.B) {
	req, err := types.NewRequest("https://example.com/x")
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()

	var total time.Duration
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		f := NewFrontier(nil)

		got := make(chan time.Time, 1)
		go func() {
			r := f.Pop(ctx)
			if r == nil {
				got <- time.Time{}
				return
			}
			got <- time.Now()
		}()

		time.Sleep(parkDelay) // let the consumer block

		pushed := time.Now()
		f.Push(req)

		returned := <-got
		if returned.IsZero() {
			b.Fatal("Pop returned nil")
		}
		total += returned.Sub(pushed)
	}

	b.StopTimer()
	b.ReportMetric(float64(total.Nanoseconds())/float64(b.N), "ns/wake")
}

// BenchmarkWakeLatencyPolling reproduces the pre-fix worker loop — TryPop with a
// 50ms sleep on a miss — under the same conditions, so the comparison is against
// the code that actually shipped rather than an estimate of it.
func BenchmarkWakeLatencyPolling(b *testing.B) {
	req, err := types.NewRequest("https://example.com/x")
	if err != nil {
		b.Fatal(err)
	}

	var total time.Duration
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		f := NewFrontier(nil)

		got := make(chan time.Time, 1)
		go func() {
			for {
				if r := f.TryPop(); r != nil {
					got <- time.Now()
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
		}()

		// Let the consumer miss once and enter its sleep, which is the state a
		// worker is in whenever the frontier drains.
		time.Sleep(parkDelay)

		pushed := time.Now()
		f.Push(req)

		total += (<-got).Sub(pushed)
	}

	b.StopTimer()
	b.ReportMetric(float64(total.Nanoseconds())/float64(b.N), "ns/wake")
}
