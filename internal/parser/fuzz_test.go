package parser

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

var fuzzLogger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

// mkResponse wraps a fuzzed body in the Response shape the parsers expect.
func mkResponse(tb testing.TB, body []byte) *types.Response {
	tb.Helper()

	req, err := types.NewRequest("https://example.com/page")
	if err != nil {
		tb.Fatalf("new request: %v", err)
	}

	u, _ := url.Parse("https://example.com/page")
	httpResp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Request:    &http.Request{URL: u},
	}

	return types.NewResponse(req, httpResp, body, time.Millisecond)
}

// seedCorpus is the set of shapes worth starting from: well-formed pages, and the
// malformations that real sites actually serve.
var seedCorpus = [][]byte{
	[]byte(`<html><body><a href="/x">y</a></body></html>`),
	[]byte(`<html><head><title>T</title><meta name="description" content="d"></head><body><h1>H</h1></body></html>`),
	// Unclosed tags — extremely common in the wild.
	[]byte(`<html><body><div><p>unclosed<div><span>`),
	// Attribute soup.
	[]byte(`<a href=/rel class='x" data-y=z>link</a>`),
	// JSON-LD, which the auto-extractor parses as a nested format.
	[]byte(`<script type="application/ld+json">{"@type":"Product","name":"x"}</script>`),
	// Malformed JSON-LD: valid HTML wrapping invalid JSON.
	[]byte(`<script type="application/ld+json">{"@type":</script>`),
	// OpenGraph.
	[]byte(`<meta property="og:title" content="t"><meta property="og:image" content="i">`),
	// A table, which the auto-extractor turns into rows.
	[]byte(`<table><tr><th>a</th></tr><tr><td>1</td><td>2</td></tr></table>`),
	// Deep nesting — a stack-depth probe.
	[]byte(`<div><div><div><div><div><div><div><div>deep</div></div></div></div></div></div></div></div>`),
	// Entities and encoding edges.
	[]byte(`<p>&amp;&lt;&#x27;&nbsp;&notanentity;</p>`),
	[]byte("<html><body>\x00\xff\xfe binary in markup</body></html>"),
	// Empty and near-empty.
	[]byte(``),
	[]byte(`<`),
	[]byte(`<!--`),
	// A base tag, which changes how relative links resolve.
	[]byte(`<base href="https://other.example/sub/"><a href="rel">x</a>`),
	// Protocol-relative and odd link targets, which feed straight into URL handling.
	[]byte(`<a href="//evil.example/x">a</a><a href="javascript:alert(1)">b</a><a href="">c</a>`),
}

// FuzzCompositeParse drives the parser the crawler actually uses. Every page the
// crawler ingests is attacker-influenced, so "must not panic" is the baseline
// contract and the one nothing was checking.
func FuzzCompositeParse(f *testing.F) {
	for _, seed := range seedCorpus {
		f.Add(seed)
	}

	p := NewCompositeParser(fuzzLogger)

	f.Fuzz(func(t *testing.T, body []byte) {
		items, links, err := p.Parse(mkResponse(t, body), nil)
		_ = err // a parse error is a fine outcome; a panic is not

		// Whatever comes back must be usable without further validation, since the
		// scheduler feeds links straight back into the frontier.
		for _, link := range links {
			if link == "" {
				t.Fatal("parser emitted an empty link")
			}
		}
		for _, item := range items {
			if item == nil {
				t.Fatal("parser emitted a nil item")
			}
		}
	})
}

// FuzzCSSParseWithRules fuzzes the body while holding a realistic rule set fixed,
// which is the configuration most users run.
func FuzzCSSParseWithRules(f *testing.F) {
	for _, seed := range seedCorpus {
		f.Add(seed)
	}

	p := NewCSSParser(fuzzLogger)
	rules := []config.ParseRule{
		{Name: "title", Selector: "title", Type: "css"},
		{Name: "headings", Selector: "h1, h2", Type: "css"},
		{Name: "links", Selector: "a", Type: "css", Attribute: "href"},
		{Name: "missing", Selector: ".not-present", Type: "css"},
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		_, _, _ = p.Parse(mkResponse(t, body), rules)
	})
}

// FuzzCSSSelector fuzzes the selector rather than the document: selectors can come
// from a config file or an API request body, and a malformed one must be an error
// rather than a panic.
func FuzzCSSSelector(f *testing.F) {
	for _, seed := range []string{
		"h1", "a[href]", ".cls", "#id", "div > p", "", "[[[", "a:nth-child(2n+1)",
		":not(", "*", "a,,b", "div::before",
	} {
		f.Add(seed)
	}

	p := NewCSSParser(fuzzLogger)
	doc := []byte(`<html><body><h1>x</h1><a href="/y">z</a><div><p>w</p></div></body></html>`)

	f.Fuzz(func(t *testing.T, selector string) {
		rules := []config.ParseRule{{Name: "f", Selector: selector, Type: "css"}}
		_, _, _ = p.Parse(mkResponse(t, doc), rules)
	})
}

// FuzzRegexParse fuzzes the user-supplied pattern. A pattern that fails to compile
// must be reported, not panic — and must not be able to wedge the parser.
func FuzzRegexParse(f *testing.F) {
	for _, seed := range []string{
		`\d+`, `[a-z]+`, `(`, `[`, `a{2,1}`, `(?P<n>x)`, `\`, `.*`, `(?i)HELLO`,
	} {
		f.Add(seed)
	}

	p := NewRegexParser(fuzzLogger)
	doc := []byte(`<html><body>abc 123 DEF</body></html>`)

	f.Fuzz(func(t *testing.T, pattern string) {
		rules := []config.ParseRule{{Name: "f", Pattern: pattern, Type: "regex"}}
		_, _, _ = p.Parse(mkResponse(t, doc), rules)
	})
}
