package parser

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

var update = flag.Bool("update", false, "rewrite golden files from current parser output")

// goldenResult is the parser output in a stable, diffable shape.
//
// Map iteration order and slice ordering are both unstable across runs, so
// everything is sorted before it is written. A golden file that reorders itself
// between runs is a golden file nobody will trust for long.
type goldenResult struct {
	Items []map[string]any `json:"items"`
	Links []string         `json:"links"`
	Error string           `json:"error,omitempty"`
}

// corpusRules is the rule set applied to every page. Fixed across the corpus so a
// golden diff reflects a parser change, not a per-page rule change.
var corpusRules = []config.ParseRule{
	{Name: "title", Selector: "title", Type: "css"},
	{Name: "h1", Selector: "h1", Type: "css"},
	{Name: "h2", Selector: "h2", Type: "css"},
	{Name: "canonical", Selector: `link[rel="canonical"]`, Type: "css", Attribute: "href"},
	{Name: "images", Selector: "img", Type: "css", Attribute: "src"},
	{Name: "absent", Selector: ".definitely-not-present", Type: "css"},
}

// TestGoldenParserOutput runs every page in testdata/pages through the composite
// parser and compares against testdata/golden.
//
// The parser was previously tested only against inline string literals — i.e.
// well-formed HTML written by the same person who wrote the parser. Real pages are
// malformed, and handling malformation is the problem domain.
//
// These files pin current behaviour rather than assert correct behaviour. Some of
// what they record is arguably wrong; that is the point. A change detector cannot
// tell you what output *should* be, only that it moved.
func TestGoldenParserOutput(t *testing.T) {
	pages, err := filepath.Glob(filepath.Join("testdata", "pages", "*.html"))
	if err != nil {
		t.Fatalf("glob corpus: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("no pages in testdata/pages — the corpus is missing")
	}

	p := NewCompositeParser(fuzzLogger)

	for _, page := range pages {
		name := strings.TrimSuffix(filepath.Base(page), ".html")

		t.Run(name, func(t *testing.T) {
			body, err := os.ReadFile(page)
			if err != nil {
				t.Fatalf("read %s: %v", page, err)
			}

			items, links, parseErr := p.Parse(mkResponse(t, body), corpusRules)

			got := goldenResult{
				Items: normaliseItems(items),
				Links: normaliseLinks(links),
			}
			if parseErr != nil {
				got.Error = parseErr.Error()
			}

			encoded, err := json.MarshalIndent(got, "", "  ")
			if err != nil {
				t.Fatalf("encode result: %v", err)
			}
			encoded = append(encoded, '\n')

			goldenPath := filepath.Join("testdata", "golden", name+".json")

			if *update {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatalf("create golden dir: %v", err)
				}
				if err := os.WriteFile(goldenPath, encoded, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				t.Logf("updated %s", goldenPath)
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s: %v\nrun `go test ./internal/parser -run TestGolden -update` "+
					"to create it, then review the output before committing", goldenPath, err)
			}

			if string(encoded) != string(want) {
				t.Errorf("parser output changed for %s.\n--- want ---\n%s\n--- got ---\n%s\n"+
					"If this change is intentional, regenerate with -update and read the diff.",
					name, want, encoded)
			}
		})
	}
}

// normaliseItems converts items to plain maps with deterministic ordering, and
// drops fields that vary between runs.
func normaliseItems(items []*types.Item) []map[string]any {
	out := make([]map[string]any, 0, len(items))

	for _, item := range items {
		if item == nil {
			continue
		}
		m := make(map[string]any, len(item.Fields))
		for k, v := range item.Fields {
			// Timestamps and per-run identifiers would make every golden file
			// stale on the next run.
			if k == "_timestamp" || k == "_id" {
				continue
			}
			m[k] = v
		}
		out = append(out, m)
	}

	// Item order across extractors is not guaranteed; sort by a stable rendering.
	sort.Slice(out, func(i, j int) bool {
		return renderMap(out[i]) < renderMap(out[j])
	})
	return out
}

func renderMap(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		if v, err := json.Marshal(m[k]); err == nil {
			b.Write(v)
		}
		b.WriteByte(';')
	}
	return b.String()
}

func normaliseLinks(links []string) []string {
	out := append([]string(nil), links...)
	sort.Strings(out)
	return out
}

// TestBaseTagResolution is the focused test for the bug the corpus surfaced:
// <base href> was ignored, so every relative link on a page that uses one
// resolved against the document URL instead of the base.
//
// On a site serving content from a CDN path that means crawling URLs that do not
// exist while never reaching the ones that do — and it fails quietly, as a crawl
// that finds nothing rather than an error.
func TestBaseTagResolution(t *testing.T) {
	tests := []struct {
		name string
		html string
		want []string
	}{
		{
			name: "absolute base rewrites relative links",
			html: `<html><head><base href="https://cdn.example/assets/"></head>
			       <body><a href="rel.html">x</a></body></html>`,
			want: []string{"https://cdn.example/assets/rel.html"},
		},
		{
			name: "absolute base applies to root-relative links",
			html: `<html><head><base href="https://cdn.example/assets/"></head>
			       <body><a href="/root">x</a></body></html>`,
			want: []string{"https://cdn.example/root"},
		},
		{
			name: "base does not affect absolute links",
			html: `<html><head><base href="https://cdn.example/assets/"></head>
			       <body><a href="https://other.example/y">x</a></body></html>`,
			want: []string{"https://other.example/y"},
		},
		{
			name: "relative base resolves against the document url",
			html: `<html><head><base href="/sub/"></head>
			       <body><a href="rel.html">x</a></body></html>`,
			want: []string{"https://example.com/sub/rel.html"},
		},
		{
			// Per the HTML spec only the first base element counts.
			name: "only the first base is honoured",
			html: `<html><head><base href="https://first.example/"><base href="https://second.example/"></head>
			       <body><a href="rel.html">x</a></body></html>`,
			want: []string{"https://first.example/rel.html"},
		},
		{
			name: "no base falls back to the document url",
			html: `<html><body><a href="rel.html">x</a></body></html>`,
			want: []string{"https://example.com/rel.html"},
		},
		{
			name: "malformed base is ignored rather than fatal",
			html: `<html><head><base href="://not a url"></head>
			       <body><a href="rel.html">x</a></body></html>`,
			want: []string{"https://example.com/rel.html"},
		},
	}

	p := NewCSSParser(fuzzLogger)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, links, err := p.Parse(mkResponse(t, []byte(tt.html)), nil)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			got := normaliseLinks(links)
			want := normaliseLinks(tt.want)

			if len(got) != len(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Errorf("link %d = %q, want %q", i, got[i], want[i])
				}
			}
		})
	}
}
