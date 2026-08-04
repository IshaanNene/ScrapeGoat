package pipeline

import (
	"fmt"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// TestFieldRenameIsDeterministic covers a case where map iteration was not merely
// an ordering nuisance but changed the result.
//
// A chained mapping (a->b together with b->c) produces a different item depending
// on which rename runs first. Iterating the map directly meant the same input
// produced different output between runs.
func TestFieldRenameIsDeterministic(t *testing.T) {
	const runs = 50

	build := func() *types.Item {
		item := types.NewItem("https://example.com/x")
		item.Set("a", "value-a")
		item.Set("b", "value-b")
		return item
	}

	m := &FieldRenameMiddleware{Mapping: map[string]string{
		"a": "b",
		"b": "c",
	}}

	first, err := m.Process(build())
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	reference := snapshot(first)

	for i := 0; i < runs; i++ {
		got, err := m.Process(build())
		if err != nil {
			t.Fatalf("process: %v", err)
		}
		if s := snapshot(got); s != reference {
			t.Fatalf("run %d produced %q, run 0 produced %q — chained renames are "+
				"still order-dependent", i, s, reference)
		}
	}

	t.Logf("chained rename result: %s", reference)
}

// TestFieldRenameCollisionIsDeterministic covers the other order-sensitive shape:
// two fields renamed onto the same target.
func TestFieldRenameCollisionIsDeterministic(t *testing.T) {
	m := &FieldRenameMiddleware{Mapping: map[string]string{
		"first":  "merged",
		"second": "merged",
	}}

	var reference string
	for i := 0; i < 50; i++ {
		item := types.NewItem("https://example.com/x")
		item.Set("first", 1)
		item.Set("second", 2)

		got, err := m.Process(item)
		if err != nil {
			t.Fatalf("process: %v", err)
		}

		s := snapshot(got)
		if i == 0 {
			reference = s
			continue
		}
		if s != reference {
			t.Fatalf("collision resolved differently: %q vs %q", s, reference)
		}
	}
}

func TestFieldRenameSimpleCase(t *testing.T) {
	m := &FieldRenameMiddleware{Mapping: map[string]string{"old": "new"}}

	item := types.NewItem("https://example.com/x")
	item.Set("old", "v")
	item.Set("untouched", "u")

	got, err := m.Process(item)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	if v, ok := got.Get("new"); !ok || v != "v" {
		t.Errorf("rename did not move the value: %v", got.Fields)
	}
	if _, ok := got.Get("old"); ok {
		t.Error("old key survived the rename")
	}
	if v, ok := got.Get("untouched"); !ok || v != "u" {
		t.Error("an unmapped field was disturbed")
	}
}

// snapshot renders an item's fields in a stable form for comparison.
func snapshot(item *types.Item) string {
	out := ""
	for _, k := range item.Keys() {
		v, _ := item.Get(k)
		out += k + "=" + toString(v) + ";"
	}
	return out
}

func toString(v any) string {
	return fmt.Sprintf("%v", v)
}
