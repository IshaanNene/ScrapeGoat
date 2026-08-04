package storage

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// SortItems puts items into a total order that does not depend on how the crawl
// was scheduled.
//
// This is the third requirement in RFC 0001's definition of Tier 1 — reproducible
// output needs pure extraction, canonical serialisation, *and* a total order on
// output records — and it is the one that is easy to believe you have without
// having it. Items reach storage in whatever order concurrent workers finished,
// so two runs over identical bytes produce identical records in different
// sequences, and a byte comparison of the output files fails while nothing is
// actually wrong. It showed up here only under `-race`, where the altered timing
// perturbed the interleaving; without the race detector the two runs happened to
// schedule the same way and the difference stayed hidden.
//
// The key is (fetch time, URL, spider, canonical fields):
//
//   - Fetch time first, so the output reads in crawl order, which is what someone
//     scanning the file expects. It is the response's timestamp, not the moment
//     the parser ran, so a replay reproduces it exactly.
//   - URL and spider next, for items that share a timestamp.
//   - The canonical field encoding last. Without it two items from the same
//     response — one page, several parse rules — would compare equal and their
//     order would fall back to however they arrived, which is the scheduling
//     dependence this function exists to remove.
//
// Cost is one key per item at close. Both writers already hold every item in
// memory to write it, so this changes the memory profile not at all.
func SortItems(items []*types.Item) {
	keys := make(map[*types.Item]string, len(items))
	for _, it := range items {
		keys[it] = orderKey(it)
	}
	sort.Slice(items, func(i, j int) bool {
		return keys[items[i]] < keys[items[j]]
	})
}

// orderKey builds the total-order key for one item.
func orderKey(i *types.Item) string {
	if i == nil {
		return ""
	}

	var b strings.Builder
	// RFC3339 with nanoseconds sorts lexicographically in time order, provided the
	// offset is uniform — it is, since every timestamp comes from the same clock.
	b.WriteString(i.Timestamp.UTC().Format("2006-01-02T15:04:05.000000000Z"))
	b.WriteByte(0)
	b.WriteString(i.URL)
	b.WriteByte(0)
	b.WriteString(i.SpiderName)
	b.WriteByte(0)

	// Keys() is sorted, so this encoding does not inherit map iteration order.
	for _, k := range i.Keys() {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(canonicalValue(i.Fields[k]))
		b.WriteByte(0)
	}
	return b.String()
}

// canonicalValue renders a field value stably.
//
// json.Marshal sorts map keys, which is what makes it usable here. A value it
// cannot encode falls back to %v rather than failing: the key only has to be
// stable and comparable, and refusing to order the output because one field held
// a channel would be a worse outcome than an approximate tiebreaker.
func canonicalValue(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case nil:
		return ""
	}
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}
