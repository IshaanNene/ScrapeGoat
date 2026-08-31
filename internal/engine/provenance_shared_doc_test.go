package engine

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// pageWithBoilerplate has its links in exactly the elements the extractor strips:
// nav, header, aside and footer. A crawler that lost those would still find the
// one link in the article body, so the test would pass while the crawl silently
// stopped discovering most of a site.
const pageWithBoilerplate = `<html><head>
<title>Doc</title>
<meta name="robots" content="noai">
</head><body>
<header><a href="/from-header">header link</a></header>
<nav><a href="/from-nav">nav link</a></nav>
<aside><a href="/from-aside">aside link</a></aside>
<article>
<p>This is the main content of the page, long enough that the density scorer has
something substantial to select as the article body rather than rejecting it.</p>
<p>A second paragraph, also of a length that contributes real text density to the
container that holds it, so extraction has a clear winner to choose.</p>
<a href="/from-article">article link</a>
</article>
<footer><a href="/from-footer">footer link</a></footer>
</body></html>`

// TestRecordProvenanceDoesNotStripTheSharedDocument is the guard on sharing one
// parsed document between the corpus writer and the parser.
//
// extract.FromDocument removes script, style, nav, aside, footer and header from
// the document it is given. Sharing the response's cached document with it
// directly — dropping the clone — would delete most of a site's navigation before
// link discovery ran, with no error and no counter: just fewer URLs.
func TestRecordProvenanceDoesNotStripTheSharedDocument(t *testing.T) {
	eng := New(testutil.LoopbackConfig(), concurrencyLogger)
	t.Cleanup(func() { eng.Stop(); eng.Wait() })

	sink := &recordSink{}
	eng.SetCorpusWriter(sink, "test-crawl")

	resp := htmlResponse(t, "https://example.com/page", pageWithBoilerplate)

	// Populate the cache first, exactly as the parser would, then confirm
	// recordProvenance leaves that same tree intact.
	doc, err := resp.Document()
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	before := doc.Find("a[href]").Length()
	if before != 5 {
		t.Fatalf("fixture has %d links, want 5 — the test is not testing what it thinks", before)
	}

	eng.scheduler.recordProvenance(context.Background(), resp)

	after := doc.Find("a[href]").Length()
	if after != before {
		t.Errorf("recording provenance removed %d of %d links from the shared document; "+
			"link discovery runs on this tree afterwards", before-after, before)
	}
	for _, want := range []string{"/from-header", "/from-nav", "/from-aside", "/from-footer", "/from-article"} {
		if doc.Find(`a[href="`+want+`"]`).Length() == 0 {
			t.Errorf("link %s was removed from the shared document", want)
		}
	}

	// The record still has to be produced, or the clone bought nothing.
	rec, ok := sink.only()
	if !ok {
		t.Fatal("no provenance record was written")
	}
	if rec.Text == "" {
		t.Error("record has no extracted text; extraction did not run on the clone")
	}
	if strings.Contains(rec.Text, "nav link") || strings.Contains(rec.Text, "footer link") {
		t.Errorf("extracted text contains boilerplate: %q", rec.Text)
	}
	// Read from <meta> on the pristine document, not the stripped clone.
	if rec.AIDirectives == nil && rec.Signals.NoAI == false {
		t.Error("meta directives were not read from the document")
	}
}

// TestResponseDocumentParsedOnce pins the other half: the corpus path must reuse
// the response's cached tree rather than building its own from the same bytes.
func TestResponseDocumentParsedOnce(t *testing.T) {
	eng := New(testutil.LoopbackConfig(), concurrencyLogger)
	t.Cleanup(func() { eng.Stop(); eng.Wait() })
	eng.SetCorpusWriter(&recordSink{}, "test-crawl")

	resp := htmlResponse(t, "https://example.com/page", pageWithBoilerplate)
	if resp.Doc != nil {
		t.Fatal("response arrived with a cached document")
	}

	eng.scheduler.recordProvenance(context.Background(), resp)

	if resp.Doc == nil {
		t.Fatal("recordProvenance did not populate the response's document cache, " +
			"so the parser will parse the same bytes a second time")
	}
	doc, err := resp.Document()
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if doc != resp.Doc {
		t.Error("Document() did not return the cached tree")
	}
}

func htmlResponse(t *testing.T, rawURL, body string) *types.Response {
	t.Helper()
	req, err := types.NewRequest(rawURL)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp := &types.Response{
		Request:    req,
		StatusCode: 200,
		Body:       []byte(body),
		FinalURL:   rawURL,
		Meta:       map[string]any{},
	}
	resp.Headers = map[string][]string{"Content-Type": {"text/html; charset=utf-8"}}
	return resp
}

// recordSink collects records written during a test.
type recordSink struct {
	mu      sync.Mutex
	records []provenance.Record
}

func (s *recordSink) Write(r provenance.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
	return nil
}
func (s *recordSink) Stats() (int64, int64) { return int64(len(s.records)), 0 }
func (s *recordSink) Path() string          { return "" }
func (s *recordSink) Close() error          { return nil }

func (s *recordSink) only() (provenance.Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) != 1 {
		return provenance.Record{}, false
	}
	return s.records[0], true
}
