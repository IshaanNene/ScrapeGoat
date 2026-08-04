package storage

import (
	"math/rand"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

func item(url string, ts time.Time, fields map[string]any) *types.Item {
	it := types.NewItem(url)
	it.Timestamp = ts
	for k, v := range fields {
		it.Set(k, v)
	}
	return it
}

// The property that matters: whatever order the items arrive in, they leave in
// the same one. Concurrent workers deliver them in a different sequence on every
// run, so a sort that depended on arrival order would produce a different file
// each time from identical data.
func TestSortItemsIsIndependentOfInputOrder(t *testing.T) {
	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	build := func() []*types.Item {
		return []*types.Item{
			item("https://example.com/b", base.Add(2*time.Millisecond), map[string]any{"title": "B"}),
			item("https://example.com/a", base.Add(1*time.Millisecond), map[string]any{"title": "A"}),
			item("https://example.com/c", base.Add(3*time.Millisecond), map[string]any{"title": "C"}),
			// same URL and timestamp, different content: the field encoding is the
			// only thing that can separate these two.
			item("https://example.com/d", base.Add(4*time.Millisecond), map[string]any{"title": "D1"}),
			item("https://example.com/d", base.Add(4*time.Millisecond), map[string]any{"title": "D2"}),
		}
	}

	want := build()
	SortItems(want)

	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 50; trial++ {
		got := build()
		rng.Shuffle(len(got), func(i, j int) { got[i], got[j] = got[j], got[i] })
		SortItems(got)

		if len(got) != len(want) {
			t.Fatalf("trial %d: sort changed the item count", trial)
		}
		for i := range got {
			if orderKey(got[i]) != orderKey(want[i]) {
				t.Fatalf("trial %d: position %d holds %s/%v, want %s/%v",
					trial, i, got[i].URL, got[i].Fields, want[i].URL, want[i].Fields)
			}
		}
	}
}

// Crawl order is what someone scanning the file expects to see, so time leads the
// key rather than URL.
func TestSortItemsOrdersByFetchTime(t *testing.T) {
	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	items := []*types.Item{
		item("https://example.com/zebra", base.Add(1*time.Second), nil),
		item("https://example.com/apple", base.Add(2*time.Second), nil),
	}
	SortItems(items)

	if items[0].URL != "https://example.com/zebra" {
		t.Errorf("sorted by URL, not by fetch time: got %s first", items[0].URL)
	}
}

// Items from one response share a timestamp and a URL. If the key stopped there
// they would compare equal and their order would fall back to arrival order,
// which is exactly the scheduling dependence being removed.
func TestSortItemsSeparatesItemsFromTheSameResponse(t *testing.T) {
	ts := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	a := item("https://example.com/p", ts, map[string]any{"rule": "css", "value": 1})
	b := item("https://example.com/p", ts, map[string]any{"rule": "xpath", "value": 2})

	if orderKey(a) == orderKey(b) {
		t.Fatal("two items from the same response produced identical order keys")
	}
}

// Field order inside an item must not leak into the key: Go map iteration is
// randomised, so a key built from unsorted keys would differ between runs on the
// same data — the precise failure this whole mechanism exists to prevent.
func TestOrderKeyIgnoresMapIterationOrder(t *testing.T) {
	ts := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	fields := map[string]any{"z": 1, "a": 2, "m": 3, "b": 4, "q": 5}
	first := orderKey(item("https://example.com/p", ts, fields))

	for i := 0; i < 100; i++ {
		if got := orderKey(item("https://example.com/p", ts, fields)); got != first {
			t.Fatalf("order key changed between runs on identical data:\n %q\n %q", first, got)
		}
	}
}

func TestSortItemsHandlesNilAndEmpty(t *testing.T) {
	SortItems(nil)
	SortItems([]*types.Item{})

	items := []*types.Item{nil, item("https://example.com/", time.Now(), nil)}
	SortItems(items) // must not panic
}
