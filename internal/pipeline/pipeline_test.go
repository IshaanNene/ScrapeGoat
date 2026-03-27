package pipeline

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func TestPipelineBasic(t *testing.T) {
	p := New(testLogger)
	p.Use(&TrimMiddleware{})

	item := types.NewItem("https://example.com")
	item.Set("title", "  Hello World  ")
	item.Set("extra", " spaces ")

	result, err := p.Process(item)
	if err != nil {
		t.Fatalf("pipeline error: %v", err)
	}
	if result.GetString("title") != "Hello World" {
		t.Errorf("expected trimmed title, got %q", result.GetString("title"))
	}
	if result.GetString("extra") != "spaces" {
		t.Errorf("expected trimmed extra, got %q", result.GetString("extra"))
	}
}

func TestRequiredFieldsMiddleware(t *testing.T) {
	m := &RequiredFieldsMiddleware{Fields: []string{"title"}}

	// Should pass — has required field
	item1 := types.NewItem("https://example.com")
	item1.Set("title", "Hello")
	result, err := m.Process(item1)
	if err != nil || result == nil {
		t.Error("item with required field should pass")
	}

	// Should drop — missing required field (returns nil, nil)
	item2 := types.NewItem("https://example.com")
	item2.Set("body", "no title")
	result, err = m.Process(item2)
	if result != nil {
		t.Error("item missing required field should be dropped (nil)")
	}
}

func TestHTMLSanitizeMiddleware(t *testing.T) {
	m := NewHTMLSanitizeMiddleware()
	item := types.NewItem("https://example.com")
	item.Set("content", `<p>Hello <b>World</b></p> &amp; <a href="x">link</a>`)

	result, err := m.Process(item)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	cleaned := result.GetString("content")
	if cleaned != "Hello World & link" {
		t.Errorf("expected 'Hello World & link', got %q", cleaned)
	}
}

func TestDateNormalizeMiddleware(t *testing.T) {
	m := NewDateNormalizeMiddleware([]string{"date"}, "2006-01-02")

	tests := []struct {
		input    string
		expected string
	}{
		{"January 15, 2024", "2024-01-15"},
		{"2024-01-15", "2024-01-15"},
		{"Jan 15, 2024", "2024-01-15"},
	}

	for _, tt := range tests {
		item := types.NewItem("https://example.com")
		item.Set("date", tt.input)

		result, _ := m.Process(item)
		got := result.GetString("date")
		if got != tt.expected {
			t.Errorf("date %q: expected %q, got %q", tt.input, tt.expected, got)
		}
	}
}

func TestCurrencyNormalizeMiddleware(t *testing.T) {
	m := NewCurrencyNormalizeMiddleware([]string{"price"})

	tests := []struct {
		input    string
		expected string
	}{
		{"$1,234.56", "1234.56"},
		{"€1.234,56", "1234.56"},
		{"£99.99", "99.99"},
		{"¥10000", "10000"},
	}

	for _, tt := range tests {
		item := types.NewItem("https://example.com")
		item.Set("price", tt.input)

		result, _ := m.Process(item)
		got := result.GetString("price")
		if got != tt.expected {
			t.Errorf("currency %q: expected %q, got %q", tt.input, tt.expected, got)
		}
	}
}

func TestPIIRedactMiddleware(t *testing.T) {
	m := NewPIIRedactMiddleware(testLogger)

	item := types.NewItem("https://example.com")
	item.Set("text", "Contact john@example.com or call 555-123-4567. SSN: 123-45-6789")

	result, err := m.Process(item)
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	text := result.GetString("text")
	t.Logf("Redacted: %s", text)

	if strings.Contains(text, "john@example.com") {
		t.Error("email should be redacted")
	}
	if strings.Contains(text, "123-45-6789") {
		t.Error("SSN should be redacted")
	}
	if !strings.Contains(text, "[REDACTED_EMAIL]") {
		t.Error("expected [REDACTED_EMAIL] placeholder")
	}
	if !strings.Contains(text, "[REDACTED_SSN]") {
		t.Error("expected [REDACTED_SSN] placeholder")
	}
}

func TestDedupMiddleware(t *testing.T) {
	m := NewDedupMiddleware("url")

	item1 := types.NewItem("https://example.com/page1")
	item1.Set("title", "Hello")

	// First time — should pass
	result, err := m.Process(item1)
	if err != nil || result == nil {
		t.Fatal("first item should pass dedup")
	}

	// Same URL — should be dropped (returns nil, nil)
	item2 := types.NewItem("https://example.com/page1")
	item2.Set("title", "Hello Again")

	result, _ = m.Process(item2)
	if result != nil {
		t.Error("duplicate item should be dropped (nil result)")
	}

	// Different URL — should pass
	item3 := types.NewItem("https://example.com/page2")
	item3.Set("title", "Different")

	result, err = m.Process(item3)
	if err != nil || result == nil {
		t.Fatal("different URL should pass dedup")
	}
}

func TestTypeCoercionMiddleware(t *testing.T) {
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
		t.Errorf("expected int64(42), got %v (%T)", v, v)
	}
	if v, _ := result.Get("price"); v != float64(19.99) {
		t.Errorf("expected float64(19.99), got %v", v)
	}
	if v, _ := result.Get("active"); v != true {
		t.Errorf("expected true, got %v", v)
	}
}

func TestWordCountMiddleware(t *testing.T) {
	m := NewWordCountMiddleware([]string{"body"})

	item := types.NewItem("https://example.com")
	item.Set("body", "The quick brown fox jumps over the lazy dog")

	result, _ := m.Process(item)

	wc, ok := result.Get("body_word_count")
	if !ok {
		t.Fatal("expected body_word_count field")
	}
	if wc != 9 {
		t.Errorf("expected 9 words, got %v", wc)
	}
}

// --- Benchmarks ---

func BenchmarkPipeline(b *testing.B) {
	p := New(testLogger)
	p.Use(&TrimMiddleware{})
	p.Use(NewHTMLSanitizeMiddleware())
	p.Use(NewDateNormalizeMiddleware([]string{"date"}, "2006-01-02"))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		item := types.NewItem("https://example.com")
		item.Set("title", "  Hello <b>World</b>  ")
		item.Set("body", "  <p>Content</p>  ")
		item.Set("date", "January 15, 2024")
		_, _ = p.Process(item)
	}
}
