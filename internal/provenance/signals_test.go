package provenance

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
)

func mustDoc(t *testing.T, html string) *goquery.Document {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	return doc
}

func TestDocumentSignals(t *testing.T) {
	doc := mustDoc(t, `<html><head>
		<meta name="robots" content="noindex, noai">
		<meta name="tdm-reservation" content="1">
		<meta name="tdm-policy" content="https://example.com/tdm.json">
		<link rel="license" href="https://creativecommons.org/licenses/by/4.0/">
		<link rel="canonical" href="https://example.com/page">
	</head><body></body></html>`)

	s := FromDocument(doc)

	if !s.NoIndex || !s.NoAI {
		t.Errorf("robots directives lost: %+v", s)
	}
	if s.TDMReservation == nil || *s.TDMReservation != 1 {
		t.Errorf("TDM reservation = %v, want 1", s.TDMReservation)
	}
	if s.TDMPolicy != "https://example.com/tdm.json" {
		t.Errorf("TDM policy = %q", s.TDMPolicy)
	}
	if s.Licence == "" || s.LicenceSource != "link" {
		t.Errorf("licence = %q from %q", s.Licence, s.LicenceSource)
	}
	if s.Canonical != "https://example.com/page" {
		t.Errorf("canonical = %q", s.Canonical)
	}
	if !s.Restrictive() {
		t.Error("a page with noai and a TDM reservation is not restrictive?")
	}
}

// X-Robots-Tag carries the same vocabulary as the meta tag and is easy to forget.
// A page with no meta tag and a restrictive header has still said no.
func TestHeaderSignals(t *testing.T) {
	h := http.Header{}
	h.Add("X-Robots-Tag", "noai")
	h.Add("X-Robots-Tag", "googlebot: noindex")
	h.Set("TDM-Reservation", "1")
	h.Set("Link", `<https://example.com/licence>; rel="license"`)

	s := FromHeaders(h)

	if !s.NoAI {
		t.Error("noai from X-Robots-Tag was lost")
	}
	if !s.NoIndex {
		t.Error("an agent-scoped X-Robots-Tag was dropped; it is still evidence of intent")
	}
	if s.TDMReservation == nil || *s.TDMReservation != 1 {
		t.Errorf("TDM reservation = %v", s.TDMReservation)
	}
	if s.Licence != "https://example.com/licence" || s.LicenceSource != "link-header" {
		t.Errorf("licence = %q from %q", s.Licence, s.LicenceSource)
	}
}

// Silence is not permission. A page that says nothing about TDM must not be
// recorded as having said 0, because in some jurisdictions those differ.
func TestTDMSilenceIsNotZero(t *testing.T) {
	s := FromDocument(mustDoc(t, `<html><head></head><body></body></html>`))

	if s.TDMReservation != nil {
		t.Errorf("a silent page reported a reservation of %d", *s.TDMReservation)
	}
	if s.Restrictive() {
		t.Error("a silent page is not restrictive")
	}
}

func TestTDMZeroIsRecorded(t *testing.T) {
	s := FromDocument(mustDoc(t, `<meta name="tdm-reservation" content="0">`))

	if s.TDMReservation == nil {
		t.Fatal("an explicit reservation of 0 was dropped")
	}
	if *s.TDMReservation != 0 {
		t.Errorf("reservation = %d, want 0", *s.TDMReservation)
	}
	if s.Restrictive() {
		t.Error("reservation 0 means mining is permitted, not restricted")
	}
}

// An out-of-range value is not a licence to guess.
func TestTDMGarbageIsTreatedAsUnsaid(t *testing.T) {
	for _, v := range []string{"yes", "2", "-1", "true", ""} {
		s := FromDocument(mustDoc(t, `<meta name="tdm-reservation" content="`+v+`">`))
		if s.TDMReservation != nil {
			t.Errorf("content=%q was parsed as %d", v, *s.TDMReservation)
		}
	}
}

// noindex is a statement about search engines. Reading it as an AI opt-out puts
// words in the source's mouth.
func TestNoIndexAloneIsNotRestrictive(t *testing.T) {
	s := FromDocument(mustDoc(t, `<meta name="robots" content="noindex, nofollow">`))

	if !s.NoIndex || !s.NoFollow {
		t.Fatalf("directives lost: %+v", s)
	}
	if s.Restrictive() {
		t.Error("noindex was treated as an AI opt-out")
	}
}

func TestRobotsNoneExpands(t *testing.T) {
	s := FromDocument(mustDoc(t, `<meta name="robots" content="none">`))

	if !s.NoIndex || !s.NoFollow {
		t.Errorf(`"none" did not expand to noindex+nofollow: %+v`, s)
	}
}

// The merge must never lose a restriction, whichever side carried it.
func TestMergeTakesTheMoreRestrictiveReading(t *testing.T) {
	one, zero := 1, 0

	header := PageSignals{NoAI: true, TDMReservation: &zero}
	doc := PageSignals{NoIndex: true, TDMReservation: &one, Licence: "doc-licence", LicenceSource: "link"}

	m := Merge(header, doc)

	if !m.NoAI {
		t.Error("noai from the header was lost in the merge")
	}
	if !m.NoIndex {
		t.Error("noindex from the document was lost in the merge")
	}
	if m.TDMReservation == nil || *m.TDMReservation != 1 {
		t.Errorf("merge chose reservation %v; 1 must win over 0", m.TDMReservation)
	}
	if m.Licence != "doc-licence" {
		t.Errorf("licence = %q; the document's own declaration should win", m.Licence)
	}
}

func TestMergeKeepsHeaderLicenceWhenDocumentIsSilent(t *testing.T) {
	m := Merge(PageSignals{Licence: "hdr", LicenceSource: "link-header"}, PageSignals{})

	if m.Licence != "hdr" || m.LicenceSource != "link-header" {
		t.Errorf("licence = %q from %q", m.Licence, m.LicenceSource)
	}
}

func TestLinkHeaderParsing(t *testing.T) {
	cases := map[string]string{
		`<https://a/l>; rel="license"`:                            "https://a/l",
		`<https://a/p>; rel="prev", <https://a/l>; rel="license"`: "https://a/l",
		`<https://a/l>; rel=license`:                              "https://a/l",
		`<https://a/l>; rel="alternate license"`:                  "https://a/l",
		`<https://a/p>; rel="prev"`:                               "",
		`https://a/l`:                                             "",
	}
	for header, want := range cases {
		if got := licenceFromLinkHeader([]string{header}); got != want {
			t.Errorf("licenceFromLinkHeader(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestNilInputsAreSafe(t *testing.T) {
	if FromHeaders(nil).Restrictive() {
		t.Error("nil headers produced a restrictive result")
	}
	if FromDocument(nil).Restrictive() {
		t.Error("a nil document produced a restrictive result")
	}
}

func TestRecordRestrictiveCoversSiteWideDirectives(t *testing.T) {
	// A page carrying no signals at all, on a site whose robots.txt turns every AI
	// crawler away. Reading only the page would miss the clearest statement made.
	r := Record{
		URL:          "https://example.com/",
		ContentHash:  "abc",
		AIDirectives: SummariseDirectives(ParseRobots("User-agent: GPTBot\nDisallow: /\n")),
	}

	if !r.Restrictive() {
		t.Error("a site-wide AI block did not make the record restrictive")
	}
}

func TestRecordComplete(t *testing.T) {
	full := Record{URL: "https://example.com/", ContentHash: "abc"}
	if full.Complete() {
		t.Error("a record with no fetch time is not complete")
	}

	full.FetchedAt = mustTime()
	if !full.Complete() {
		t.Error("a record with url, hash, and time is complete")
	}

	for _, r := range []Record{
		{ContentHash: "abc", FetchedAt: mustTime()},
		{URL: "https://example.com/", FetchedAt: mustTime()},
	} {
		if r.Complete() {
			t.Errorf("incomplete record reported complete: %+v", r)
		}
	}
}

func mustTime() time.Time {
	return time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
}
