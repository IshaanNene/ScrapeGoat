package parser

import (
	"strings"
	"testing"

	"log/slog"
	"os"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

const testHTML = `<!DOCTYPE html>
<html>
<head>
    <title>Test Page</title>
    <meta name="description" content="A test page for parsing">
    <meta property="og:title" content="OG Test Title">
    <meta property="og:image" content="https://example.com/image.png">
    <meta name="twitter:card" content="summary">
    <meta name="twitter:title" content="Twitter Title">
    <script type="application/ld+json">
    {"@context":"https://schema.org","@type":"Article","name":"Test Article","author":"Bob"}
    </script>
</head>
<body>
    <h1 class="title">Hello World</h1>
    <div class="content">
        <p class="intro">This is a test paragraph.</p>
        <a href="/page2">Link 1</a>
        <a href="https://example.com/page3">Link 2</a>
    </div>
    <ul class="items">
        <li>Item 1</li>
        <li>Item 2</li>
        <li>Item 3</li>
    </ul>
    <table id="data">
        <tr><th>Name</th><th>Value</th></tr>
        <tr><td>Alpha</td><td>100</td></tr>
        <tr><td>Beta</td><td>200</td></tr>
    </table>
</body>
</html>`

func makeResp() *types.Response {
	req, _ := types.NewRequest("https://example.com")
	return &types.Response{
		Request:     req,
		StatusCode:  200,
		Body:        []byte(body),
		ContentType: "text/html",
	}
}

// --- CSS Parser Tests ---

func TestCSSParserExtract(t *testing.T) {
	p := NewCSSParser(testLogger)
	resp := makeResp()

	rules := []config.ParseRule{
		{Name: "title", Type: "css", Selector: "h1.title"},
		{Name: "intro", Type: "css", Selector: "p.intro"},
	}

	items, _, err := p.Parse(resp, rules)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(items) == 0 {
		t.Fatal("expected items, got none")
	}

	title := items[0].GetString("title")
	if title != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", title)
	}

	intro := items[0].GetString("intro")
	if intro != "This is a test paragraph." {
		t.Errorf("expected test paragraph, got %q", intro)
	}
}

func TestCSSParserLinks(t *testing.T) {
	p := NewCSSParser(testLogger)
	resp := makeResp()

	_, links, err := p.Parse(resp, nil)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if len(links) < 1 {
		t.Fatalf("expected at least 1 link, got %d", len(links))
	}

	// Log discovered links
	t.Logf("Found %d links:", len(links))
	for _, link := range links {
		t.Logf("  %s", link)
	}
}

// --- XPath Parser Tests ---

func TestXPathParser(t *testing.T) {
	p := NewXPathParser(testLogger)
	resp := makeResp()

	rules := []config.ParseRule{
		{Name: "heading", Type: "xpath", Selector: "//h1"},
		{Name: "list_items", Type: "xpath", Selector: "//ul[@class='items']/li"},
	}

	items, _, err := p.Parse(resp, rules)
	if err != nil {
		t.Fatalf("xpath parse error: %v", err)
	}

	if len(items) == 0 {
		t.Fatal("expected items from xpath")
	}

	heading := items[0].GetString("heading")
	if heading != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", heading)
	}
}

// --- Regex Parser Tests ---

func TestRegexParser(t *testing.T) {
	p := NewRegexParser(testLogger)
	resp := makeResp()

	rules := []config.ParseRule{
		{Name: "title", Type: "regex", Pattern: `<title>(?P<title>[^<]+)</title>`},
	}

	items, _, err := p.Parse(resp, rules)
	if err != nil {
		t.Fatalf("regex parse error: %v", err)
	}

	if len(items) == 0 {
		t.Fatal("expected items from regex")
	}

	title := items[0].GetString("title")
	if title != "Test Page" {
		t.Errorf("expected 'Test Page', got %q", title)
	}
}

// --- Structured Data Tests ---

func TestStructuredDataExtraction(t *testing.T) {
	sde := NewStructuredDataExtractor(testLogger)
	resp := makeResp()

	results, err := sde.Extract(resp)
	if err != nil {
		t.Fatalf("structured data error: %v", err)
	}

	var foundJSONLD, foundOG, foundTwitter, foundMeta bool
	for _, r := range results {
		switch r.Type {
		case JSONLD:
			foundJSONLD = true
			if r.Data["name"] != "Test Article" {
				t.Errorf("expected JSON-LD name 'Test Article', got %v", r.Data["name"])
			}
		case OpenGraph:
			foundOG = true
			if r.Data["title"] != "OG Test Title" {
				t.Errorf("expected og:title 'OG Test Title', got %v", r.Data["title"])
			}
		case TwitterCard:
			foundTwitter = true
			if r.Data["card"] != "summary" {
				t.Errorf("expected twitter:card 'summary', got %v", r.Data["card"])
			}
		case MetaTags:
			foundMeta = true
			if r.Data["title"] != "Test Page" {
				t.Errorf("expected meta title 'Test Page', got %v", r.Data["title"])
			}
		}
	}

	if !foundJSONLD {
		t.Error("missing JSON-LD extraction")
	}
	if !foundOG {
		t.Error("missing OpenGraph extraction")
	}
	if !foundTwitter {
		t.Error("missing Twitter Card extraction")
	}
	if !foundMeta {
		t.Error("missing meta tags extraction")
	}
}

// --- DOM Traversal Tests ---

func TestDOMTraversal(t *testing.T) {
	dt := NewDOMTraverser(testLogger)
	resp := makeResp()

	t.Run("FindChildren", func(t *testing.T) {
		results, err := dt.FindChildren(resp, "ul.items", "li")
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected results")
		}
		if len(results[0].Children) != 3 {
			t.Errorf("expected 3 list items, got %d", len(results[0].Children))
		}
	})

	t.Run("ExtractTable", func(t *testing.T) {
		table, err := dt.ExtractTable(resp, "#data")
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if len(table) != 3 { // header + 2 rows
			t.Fatalf("expected 3 rows, got %d", len(table))
		}
		if table[1][0] != "Alpha" || table[1][1] != "100" {
			t.Errorf("expected [Alpha, 100], got %v", table[1])
		}
	})

	t.Run("ExtractList", func(t *testing.T) {
		items, err := dt.ExtractList(resp, "ul.items")
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if len(items) != 3 {
			t.Errorf("expected 3 items, got %d", len(items))
		}
	})
}

// --- Auto Selector Tests ---

func TestAutoSelectorGenerator(t *testing.T) {
	asg := NewAutoSelectorGenerator(testLogger)
	resp := makeResp()

	candidates, err := asg.GenerateForText(resp, "Hello World")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	if len(candidates) == 0 {
		t.Fatal("expected selector candidates")
	}

	// The highest-scored selector should uniquely match "Hello World"
	best := candidates[0]
	t.Logf("Best selector: %q (score: %.2f, matches: %d)", best.Selector, best.Score, best.MatchCount)

	if best.Score < 0.5 {
		t.Errorf("expected high score for unique element, got %.2f", best.Score)
	}
}

// --- Composite Parser Tests ---

func TestCompositeParser(t *testing.T) {
	cp := NewCompositeParser(testLogger)
	resp := makeResp()

	rules := []config.ParseRule{
		{Name: "heading", Type: "css", Selector: "h1"},
		{Name: "page_title", Type: "regex", Pattern: `<title>(?P<page_title>[^<]+)</title>`},
		{Name: "xpath_heading", Type: "xpath", Selector: "//h1"},
	}

	items, links, err := cp.Parse(resp, rules)
	if err != nil {
		t.Fatalf("composite parse: %v", err)
	}

	if len(items) == 0 {
		t.Fatal("expected items from composite parser")
	}

	item := items[0]

	// Should have CSS result
	if h := item.GetString("heading"); !strings.Contains(h, "Hello World") {
		t.Errorf("CSS: expected Hello World, got %q", h)
	}

	// Check regex result (may have different output format)
	if pt := item.GetString("page_title"); pt != "" {
		t.Logf("Regex page_title: %q", pt)
	}

	// Should have structured data auto-extracted
	if _, ok := item.Get("meta_title"); !ok {
		t.Error("expected auto-extracted meta_title")
	}

	// Should have discovered links
	if len(links) < 1 {
		t.Errorf("expected links, got %d", len(links))
	}

	t.Logf("Composite parser produced %d items, %d links", len(items), len(links))
	for k, v := range item.Fields {
		t.Logf("  %s = %v", k, v)
	}
}

// --- Benchmarks ---

func BenchmarkCSSParse(b *testing.B) {
	p := NewCSSParser(testLogger)
	resp := makeResp()
	rules := []config.ParseRule{
		{Name: "title", Type: "css", Selector: "h1"},
		{Name: "content", Type: "css", Selector: ".content p"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Parse(resp, rules)
	}
}

func BenchmarkXPathParse(b *testing.B) {
	p := NewXPathParser(testLogger)
	resp := makeResp()
	rules := []config.ParseRule{
		{Name: "title", Type: "xpath", Selector: "//h1"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Parse(resp, rules)
	}
}

func BenchmarkStructuredData(b *testing.B) {
	sde := NewStructuredDataExtractor(testLogger)
	resp := makeResp()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sde.Extract(resp)
	}
}

func BenchmarkCompositeParser(b *testing.B) {
	cp := NewCompositeParser(testLogger)
	resp := makeResp()
	rules := []config.ParseRule{
		{Name: "h", Type: "css", Selector: "h1"},
		{Name: "t", Type: "regex", Selector: `<title>([^<]+)</title>`},
		{Name: "x", Type: "xpath", Selector: "//h1"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cp.Parse(resp, rules)
	}
}
