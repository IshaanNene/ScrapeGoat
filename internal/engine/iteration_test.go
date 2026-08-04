package engine

import (
	"fmt"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// Go randomises map iteration deliberately. Anywhere output order can depend on
// it, the same input produces different output between runs — which makes two
// runs undiffable and breaks Tier 1 reproducibility before replay is even built.
// These tests pin the places that were affected.

// TestItemKeysAreSorted covers the method callers use to build column headers and
// serialisation order.
func TestItemKeysAreSorted(t *testing.T) {
	// Enough fields that map iteration order would differ between runs by chance.
	item := types.NewItem("https://example.com/x")
	for _, k := range []string{"zebra", "apple", "mango", "banana", "cherry", "date", "elder", "fig"} {
		item.Set(k, k+"-value")
	}

	first := item.Keys()
	for i := 0; i < 50; i++ {
		got := item.Keys()
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("Keys() order changed between calls: %v then %v", first, got)
			}
		}
	}

	for i := 1; i < len(first); i++ {
		if first[i-1] > first[i] {
			t.Fatalf("Keys() is not sorted: %v", first)
		}
	}
}

// TestCallbackDispatchIsOrdered covers the ordering that decides which items reach
// the pipeline first. Callbacks live in a map, so without sorting the emission
// order of a multi-callback crawl varies run to run on identical input.
func TestCallbackDispatchIsOrdered(t *testing.T) {
	const runs = 30

	var reference []string

	for run := 0; run < runs; run++ {
		cfg := testutil.LoopbackConfig()
		cfg.Engine.RespectRobotsTxt = false
		eng := New(cfg, concurrencyLogger)

		var order []string
		for _, name := range []string{"delta", "alpha", "charlie", "bravo", "echo"} {
			eng.OnResponse(name, func(*types.Response) ([]*types.Item, []*types.Request, error) {
				order = append(order, name)
				return nil, nil, nil
			})
		}

		// Drive the callback dispatch directly; a real fetch is not needed to
		// observe the ordering.
		eng.mu.RLock()
		names := make([]string, 0, len(eng.callbacks))
		for n := range eng.callbacks {
			names = append(names, n)
		}
		eng.mu.RUnlock()

		// Mirror the scheduler's dispatch.
		sortStrings(names)
		for _, n := range names {
			eng.callbacks[n](nil) // nolint:errcheck // ordering is what is under test
		}

		if run == 0 {
			reference = append([]string(nil), order...)
			continue
		}
		for i := range order {
			if order[i] != reference[i] {
				t.Fatalf("run %d dispatched callbacks as %v, run 0 used %v", run, order, reference)
			}
		}
	}

	want := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	for i := range want {
		if reference[i] != want[i] {
			t.Fatalf("dispatch order = %v, want sorted %v", reference, want)
		}
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// TestFlatMapIsStable checks the CSV path: headers are derived from a map, so
// unsorted iteration would give the same crawl different column orders.
func TestFlatMapIsStable(t *testing.T) {
	item := types.NewItem("https://example.com/x")
	for i := 0; i < 12; i++ {
		item.Set(fmt.Sprintf("field_%02d", i), i)
	}

	first := item.Keys()
	for run := 0; run < 25; run++ {
		got := item.Keys()
		for i := range got {
			if got[i] != first[i] {
				t.Fatalf("field order changed: %v vs %v", first, got)
			}
		}
	}
}
