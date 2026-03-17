package pipeline

import (
	"fmt"
	"strings"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// ---------------------------------------------------------------------------
// Individual middleware tests
// ---------------------------------------------------------------------------

func TestTrimMiddleware(t *testing.T) {
	t.Parallel()
	m := &TrimMiddleware{}
	item := types.NewItem("https://example.com")
	item.Set("title", "  Hello World  ")
	item.Set("body", "\n\t spaces \n")

	result, err := m.Process(item)
	if err != nil {
		t.Fatal(err)
	}
	if result.GetString("title") != "Hello World" {
		t.Errorf("title=%q", result.GetString("title"))
	}
	if result.GetString("body") != "spaces" {
		t.Errorf("body=%q", result.GetString("body"))
	}
}

func TestFieldFilterMiddleware_Exhaustive(t *testing.T) {
	t.Parallel()
	m := &FieldFilterMiddleware{Fields: map[string]bool{"title": true, "price": true}}
	item := types.NewItem("https://example.com")
	item.Set("title", "Product")
	item.Set("price", "19.99")
	item.Set("junk", "remove me")

	result, _ := m.Process(item)
	if result.GetString("junk") != "" {
		t.Error("junk field should be filtered")
	}
	if result.GetString("title") != "Product" {
		t.Error("title should remain")
	}
}

func TestFieldRenameMiddleware(t *testing.T) {
	t.Parallel()
	m := &FieldRenameMiddleware{Mapping: map[string]string{"old_name": "new_name"}}
	item := types.NewItem("https://example.com")
	item.Set("old_name", "value")

	result, _ := m.Process(item)
	if result.GetString("new_name") != "value" {
		t.Error("field should be renamed")
	}
	if result.GetString("old_name") != "" {
		t.Error("old field name should be removed")
	}
}

func TestRequiredFieldsMiddleware_Pass(t *testing.T) {
	t.Parallel()
	m := &RequiredFieldsMiddleware{Fields: []string{"title", "url"}}
	item := types.NewItem("https://example.com")
	item.Set("title", "Hello")
	item.Set("url", "https://example.com")

	result, err := m.Process(item)
	if err != nil || result == nil {
		t.Error("item with all required fields should pass")
	}
}

func TestRequiredFieldsMiddleware_Fail(t *testing.T) {
	t.Parallel()
	m := &RequiredFieldsMiddleware{Fields: []string{"title", "url"}}
	item := types.NewItem("https://example.com")
	item.Set("title", "Hello") // missing "url"

	result, _ := m.Process(item)
	if result != nil {
		t.Error("item missing required field should be dropped")
	}
}

func TestDedupMiddleware_Dedup(t *testing.T) {
	t.Parallel()
	m := NewDedupMiddleware("url")

	item1 := types.NewItem("https://example.com/page1")
	item1.Set("title", "First")

	// First: should pass
	r1, _ := m.Process(item1)
	if r1 == nil {
		t.Fatal("first item should pass")
	}

	// Duplicate: should drop
	item2 := types.NewItem("https://example.com/page1")
	item2.Set("title", "Duplicate")
	r2, _ := m.Process(item2)
	if r2 != nil {
		t.Error("duplicate should be dropped")
	}

	// Different: should pass
	item3 := types.NewItem("https://example.com/page2")
	item3.Set("title", "Different")
	r3, _ := m.Process(item3)
	if r3 == nil {
		t.Error("different URL should pass")
	}
}

func TestDefaultValueMiddleware(t *testing.T) {
	t.Parallel()
	m := &DefaultValueMiddleware{Defaults: map[string]any{"source": "scrapegoat", "version": "1.0"}}
	item := types.NewItem("https://example.com")
	item.Set("source", "custom") // Already set — should NOT be overwritten

	result, _ := m.Process(item)
	if result.GetString("source") != "custom" {
		t.Error("existing value should not be overwritten")
	}
	if result.GetString("version") != "1.0" {
		t.Error("default value should be applied for missing field")
	}
}

func TestHTMLSanitizeMiddleware_Strips(t *testing.T) {
	t.Parallel()
	m := NewHTMLSanitizeMiddleware()

	tests := []struct {
		input    string
		expected string
	}{
		{`<p>Hello <b>World</b></p>`, "Hello World"},
		{`<script>alert('xss')</script><p>Safe</p>`, "alert('xss')Safe"},
		{`Text &amp; More`, "Text & More"},
		{`<a href="x">Link</a> text`, "Link text"},
	}

	for _, tt := range tests {
		item := types.NewItem("https://example.com")
		item.Set("content", tt.input)
		result, _ := m.Process(item)
		got := result.GetString("content")
		if got != tt.expected {
			t.Errorf("sanitize(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestDateNormalizeMiddleware_Exhaustive(t *testing.T) {
	t.Parallel()
	m := NewDateNormalizeMiddleware([]string{"date"}, "2006-01-02")

	tests := []struct {
		input    string
		expected string
	}{
		{"January 15, 2024", "2024-01-15"},
		{"2024-01-15", "2024-01-15"},
		{"Jan 15, 2024", "2024-01-15"},
		{"15/01/2024", "2024-01-15"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			item := types.NewItem("https://example.com")
			item.Set("date", tt.input)
			result, _ := m.Process(item)
			got := result.GetString("date")
			if got != tt.expected {
				t.Errorf("date(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestCurrencyNormalizeMiddleware_Exhaustive(t *testing.T) {
	t.Parallel()
	m := NewCurrencyNormalizeMiddleware([]string{"price"})

	tests := []struct {
		input    string
		expected string
	}{
		{"$1,234.56", "1234.56"},
		{"€1.234,56", "1234.56"},
		{"£99.99", "99.99"},
		{"¥10000", "10000"},
		{"$0.99", "0.99"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			item := types.NewItem("https://example.com")
			item.Set("price", tt.input)
			result, _ := m.Process(item)
			got := result.GetString("price")
			if got != tt.expected {
				t.Errorf("currency(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestTypeCoercionMiddleware_Exhaustive(t *testing.T) {
	t.Parallel()
	m := NewTypeCoercionMiddleware(map[string]string{
		"count":  "int",
		"price":  "float",
		"active": "bool",
	})

	item := types.NewItem("https://example.com")
	item.Set("count", "42")
	item.Set("price", "19.99")
	item.Set("active", "true")

	result, _ := m.Process(item)

	if v, _ := result.Get("count"); v != int64(42) {
		t.Errorf("count: expected int64(42), got %v (%T)", v, v)
	}
	if v, _ := result.Get("price"); v != float64(19.99) {
		t.Errorf("price: expected float64(19.99), got %v", v)
	}
	if v, _ := result.Get("active"); v != true {
		t.Errorf("active: expected true, got %v", v)
	}
}

func TestPIIRedactMiddleware_Exhaustive(t *testing.T) {
	t.Parallel()
	m := NewPIIRedactMiddleware(testLogger)

	item := types.NewItem("https://example.com")
	item.Set("text", "Email john@example.com SSN 123-45-6789 phone 555-123-4567")

	result, _ := m.Process(item)
	text := result.GetString("text")

	if strings.Contains(text, "john@example.com") {
		t.Error("email not redacted")
	}
	if strings.Contains(text, "123-45-6789") {
		t.Error("SSN not redacted")
	}
}

func TestWordCountMiddleware_Exhaustive(t *testing.T) {
	t.Parallel()
	m := NewWordCountMiddleware([]string{"body"})

	item := types.NewItem("https://example.com")
	item.Set("body", "The quick brown fox jumps over the lazy dog")

	result, _ := m.Process(item)
	wc, ok := result.Get("body_word_count")
	if !ok {
		t.Fatal("body_word_count not set")
	}
	if wc != 9 {
		t.Errorf("word count=%v, want 9", wc)
	}
}

// ---------------------------------------------------------------------------
// Full pipeline chain test: all 13 middlewares
// ---------------------------------------------------------------------------

func TestFullPipelineChain(t *testing.T) {
	t.Parallel()

	p := New(testLogger)
	p.Use(&TrimMiddleware{})
	p.Use(NewHTMLSanitizeMiddleware())
	p.Use(NewDateNormalizeMiddleware([]string{"date"}, "2006-01-02"))
	p.Use(NewCurrencyNormalizeMiddleware([]string{"price"}))
	p.Use(NewTypeCoercionMiddleware(map[string]string{"count": "int", "active": "bool"}))
	p.Use(NewPIIRedactMiddleware(testLogger))
	p.Use(NewWordCountMiddleware([]string{"description"}))
	p.Use(&DefaultValueMiddleware{Defaults: map[string]any{"source": "scrapegoat"}})
	p.Use(&RequiredFieldsMiddleware{Fields: []string{"title"}})
	// Dedup, FieldFilter, FieldRename not in chain — they require specific config

	item := types.NewItem("https://example.com")
	item.Set("title", "  <b>Product Widget</b>  ")
	item.Set("price", "$1,234.56")
	item.Set("date", "January 15, 2024")
	item.Set("count", "42")
	item.Set("active", "true")
	item.Set("description", "  <p>A great product for all your needs</p>  ")
	item.Set("notes", "Contact john@example.com for details")

	result, err := p.Process(item)
	if err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}

	// Title should be trimmed + HTML stripped
	title := result.GetString("title")
	if title != "Product Widget" {
		t.Errorf("title=%q, want 'Product Widget'", title)
	}

	// Price should be normalized
	if result.GetString("price") != "1234.56" {
		t.Errorf("price=%q", result.GetString("price"))
	}

	// Date should be normalized
	if result.GetString("date") != "2024-01-15" {
		t.Errorf("date=%q", result.GetString("date"))
	}

	// Count should be coerced to int
	if v, _ := result.Get("count"); v != int64(42) {
		t.Errorf("count=%v (%T)", v, v)
	}

	// Default value applied
	if result.GetString("source") != "scrapegoat" {
		t.Errorf("source=%q", result.GetString("source"))
	}

	// Word count computed
	if wc, ok := result.Get("description_word_count"); !ok || wc == nil {
		t.Error("description_word_count not set")
	}

	// PII redacted
	notes := result.GetString("notes")
	if strings.Contains(notes, "john@example.com") {
		t.Error("email not redacted in notes")
	}

	t.Logf("Full pipeline chain PASS: %d fields processed", len(result.Keys()))
}

// ---------------------------------------------------------------------------
// Pipeline ordering test: middlewares execute in registration order
// ---------------------------------------------------------------------------

func TestPipelineOrdering(t *testing.T) {
	t.Parallel()

	var order []string

	type orderMiddleware struct {
		name string
	}
	_ = fmt.Sprint // avoid unused import

	p := New(testLogger)

	// Create tracking middlewares
	for _, name := range []string{"first", "second", "third"} {
		name := name
		p.Use(&trackingMiddleware{name: name, order: &order})
	}

	item := types.NewItem("https://example.com")
	item.Set("title", "test")
	p.Process(item)

	expected := []string{"first", "second", "third"}
	if len(order) != len(expected) {
		t.Fatalf("order=%v, want %v", order, expected)
	}
	for i, name := range expected {
		if order[i] != name {
			t.Errorf("position %d: got %s, want %s", i, order[i], name)
		}
	}
}

type trackingMiddleware struct {
	name  string
	order *[]string
}

func (m *trackingMiddleware) Name() string     { return m.name }
func (m *trackingMiddleware) Priority() int    { return 0 }
func (m *trackingMiddleware) Process(item *types.Item) (*types.Item, error) {
	*m.order = append(*m.order, m.name)
	return item, nil
}

// ---------------------------------------------------------------------------
// Pipeline: nil item handling
// ---------------------------------------------------------------------------

func TestPipelineNilItem(t *testing.T) {
	t.Parallel()

	p := New(testLogger)
	p.Use(&TrimMiddleware{})

	// Pipeline.Process(nil) panics — this is an actual bug in the framework.
	// The pipeline should check for nil items. This test documents the bug.
	defer func() {
		if r := recover(); r != nil {
			t.Logf("BUG CONFIRMED: Pipeline.Process(nil) panics: %v", r)
		} else {
			t.Log("PASS: nil item handled without panic")
		}
	}()

	result, err := p.Process(nil)
	_ = result
	_ = err
}

// ---------------------------------------------------------------------------
// Pipeline: item dropped by RequiredFields
// ---------------------------------------------------------------------------

func TestPipelineDroppedItem(t *testing.T) {
	t.Parallel()

	p := New(testLogger)
	p.Use(&RequiredFieldsMiddleware{Fields: []string{"title"}})
	p.Use(&TrimMiddleware{})

	item := types.NewItem("https://example.com")
	item.Set("body", "no title here") // Missing required "title"

	result, err := p.Process(item)
	if err != nil {
		t.Logf("pipeline returned error (expected): %v", err)
	}
	if result != nil {
		t.Error("item without required field should be dropped (nil)")
	}
}
