package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
)

// corpusSite serves pages carrying different reuse signals, and a robots.txt that
// permits this crawler while turning AI crawlers away — the case that matters,
// because the crawl is fully permitted and the corpus must still record that the
// site said no to somebody.
func corpusSite(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()

	pages := map[string]string{
		"/": `<html lang="en"><head><title>Index</title>
			<link rel="license" href="https://creativecommons.org/licenses/by/4.0/">
			</head><body><h1>Index</h1>
			<p>The root page carries a licence and enough prose to be extracted from.</p>
			<a href="/opted-out">one</a> <a href="/plain">two</a></body></html>`,
		"/opted-out": `<html lang="en"><head><title>Opted out</title>
			<meta name="robots" content="noai">
			<meta name="tdm-reservation" content="1">
			</head><body><h1>Opted out</h1>
			<p>This page asks not to be used for training, in two different ways.</p>
			</body></html>`,
		"/plain": `<html lang="en"><head><title>Plain</title></head><body><h1>Plain</h1>
			<p>This page says nothing at all about reuse, which is itself a fact.</p>
			</body></html>`,
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)

		if r.URL.Path == "/robots.txt" {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "User-agent: *\nDisallow:\n\nUser-agent: GPTBot\nDisallow: /\n\nUser-agent: CCBot\nDisallow: /\n")
			return
		}
		body, ok := pages[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}))
}

func TestCrawlWritesProvenanceCorpus(t *testing.T) {
	resetGlobals(t)

	var hits int32
	srv := corpusSite(t, &hits)
	defer srv.Close()

	work := t.TempDir()
	corpusFile := filepath.Join(work, "corpus.jsonl")

	cfgFile = loopbackConfigFile(t, filepath.Join(work, "out"))
	corpusPath = corpusFile
	t.Cleanup(func() { corpusPath = "" })

	if err := runCrawl(nil, []string{srv.URL + "/"}); err != nil {
		t.Fatalf("crawl: %v", err)
	}

	records, err := provenance.ReadCorpus(corpusFile)
	if err != nil {
		t.Fatalf("ReadCorpus: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3: %+v", len(records), records)
	}

	byPath := map[string]provenance.Record{}
	for _, r := range records {
		if !r.Complete() {
			t.Errorf("incomplete record reached the corpus: %+v", r)
		}
		if r.SchemaVersion != provenance.SchemaVersion {
			t.Errorf("schema version = %d", r.SchemaVersion)
		}
		// Every page here was permitted; the AI block does not apply to us.
		if !r.RobotsAllowed {
			t.Errorf("%s recorded as disallowed, but the crawl fetched it", r.URL)
		}
		byPath[pathOf(t, r.URL)] = r
	}

	// Every record must carry the site-wide AI directives, including the page that
	// says nothing itself — that is the whole point of recording them per record.
	for p, r := range byPath {
		if r.AIDirectives == nil {
			t.Errorf("%s carries no AI directives", p)
			continue
		}
		if len(r.AIDirectives.AgentsBlocked) != 2 {
			t.Errorf("%s blocked agents = %v, want two", p, r.AIDirectives.AgentsBlocked)
		}
		if !r.Restrictive() {
			t.Errorf("%s is not restrictive, but the site blocks AI crawlers", p)
		}
	}

	root := byPath["/"]
	if root.Signals.Licence != "https://creativecommons.org/licenses/by/4.0/" {
		t.Errorf("licence = %q", root.Signals.Licence)
	}
	if root.Language != "en" {
		t.Errorf("language = %q", root.Language)
	}
	if root.MIMEType != "text/html" {
		t.Errorf("mime = %q", root.MIMEType)
	}
	if root.Text == "" {
		t.Error("no text was extracted from the root page")
	}

	opted := byPath["/opted-out"]
	if !opted.Signals.NoAI {
		t.Error("the noai meta tag was not recorded")
	}
	if opted.Signals.TDMReservation == nil || *opted.Signals.TDMReservation != 1 {
		t.Errorf("TDM reservation = %v", opted.Signals.TDMReservation)
	}

	// The page with no signals of its own must still be recorded, not dropped.
	if _, ok := byPath["/plain"]; !ok {
		t.Error("the page carrying no signals is missing from the corpus")
	}

	s := provenance.Summarise(records)
	if s.Restrictive != 3 {
		t.Errorf("restrictive = %d, want 3 — the site-wide block covers every page", s.Restrictive)
	}
	if s.RobotsDisallowed != 0 {
		t.Errorf("robots disallowed = %d, want 0", s.RobotsDisallowed)
	}
	if s.Licensed != 1 {
		t.Errorf("licensed = %d, want 1", s.Licensed)
	}
}

func pathOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Path
}
