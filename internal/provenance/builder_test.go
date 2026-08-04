package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
)

func TestBuildFullRecord(t *testing.T) {
	doc := mustDoc(t, `<html lang="en-GB"><head>
		<link rel="canonical" href="https://example.com/canonical">
		<meta name="robots" content="noai">
	</head><body><p>text</p></body></html>`)

	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")

	src := Source{
		URL:             "https://example.com/page?utm_source=x",
		FinalURL:        "https://example.com/page",
		ContentHash:     "deadbeef",
		StatusCode:      200,
		Headers:         h,
		FetchedAt:       mustTime(),
		CrawlerIdentity: "ScrapeGoat/0.1",
		CrawlID:         "crawl-1",
		RobotsAllowed:   true,
		Robots:          ParseRobots("User-agent: CCBot\nDisallow: /\n"),
	}

	rec := Build(src, doc, Content{Text: "text", Title: "T", Confidence: 0.8})

	if rec.SchemaVersion != SchemaVersion {
		t.Errorf("schema version = %d", rec.SchemaVersion)
	}
	if rec.CanonicalURL != "https://example.com/canonical" {
		t.Errorf("canonical = %q", rec.CanonicalURL)
	}
	if rec.MIMEType != "text/html" {
		t.Errorf("mime = %q, want the media type without parameters", rec.MIMEType)
	}
	if rec.Language != "en-gb" {
		t.Errorf("language = %q", rec.Language)
	}
	if !rec.RobotsAllowed {
		t.Error("the permission the crawl operated under was lost")
	}
	if !rec.Signals.NoAI {
		t.Error("page signals were not merged in")
	}
	if rec.AIDirectives == nil || len(rec.AIDirectives.AgentsBlocked) != 1 {
		t.Errorf("AI directives = %+v", rec.AIDirectives)
	}
	if !rec.Complete() {
		t.Error("record is not complete")
	}
	if !rec.Restrictive() {
		t.Error("a page with noai on a site blocking CCBot is restrictive")
	}
}

// A record must not imply the site said something when no robots.txt was seen.
func TestBuildOmitsDirectivesWhenNoRobots(t *testing.T) {
	rec := Build(Source{URL: "https://example.com/", ContentHash: "x", FetchedAt: mustTime()}, nil, Content{})

	if rec.AIDirectives != nil {
		t.Errorf("a record with no robots.txt claimed directives: %+v", rec.AIDirectives)
	}
	if rec.Restrictive() {
		t.Error("absence of robots.txt is not a restriction")
	}
}

// An empty-but-served robots.txt is a statement, and must be recorded as one.
func TestBuildKeepsAnEmptyRobotsAsPresent(t *testing.T) {
	rec := Build(Source{
		URL: "https://example.com/", ContentHash: "x", FetchedAt: mustTime(),
		Robots: ParseRobots(""),
	}, nil, Content{})

	if rec.AIDirectives == nil {
		t.Fatal("an empty robots.txt that was served should still be recorded")
	}
	if !rec.AIDirectives.RobotsPresent {
		t.Error("RobotsPresent is false for a file that was served")
	}
	if len(rec.AIDirectives.AgentsBlocked) != 0 {
		t.Errorf("an empty file blocks nobody: %v", rec.AIDirectives.AgentsBlocked)
	}
}

// A non-HTML response has provenance too; it just has fewer places to state it.
func TestBuildWithNilDocument(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/pdf")
	h.Set("X-Robots-Tag", "noai")

	rec := Build(Source{
		URL: "https://example.com/doc.pdf", ContentHash: "x",
		FetchedAt: mustTime(), Headers: h,
	}, nil, Content{})

	if rec.MIMEType != "application/pdf" {
		t.Errorf("mime = %q", rec.MIMEType)
	}
	if !rec.Signals.NoAI {
		t.Error("header signals were dropped for a non-HTML response")
	}
}

// The hash must be the one the fetch log filed the body under. Recomputing would
// let the two drift, and joining a record to bytes is the field's whole purpose.
func TestBuildPrefersTheSuppliedHash(t *testing.T) {
	body := []byte("some bytes")
	rec := Build(Source{
		URL: "https://example.com/", ContentHash: "authoritative",
		Body: body, FetchedAt: mustTime(),
	}, nil, Content{})

	if rec.ContentHash != "authoritative" {
		t.Errorf("hash = %q; the supplied value must win", rec.ContentHash)
	}
}

// The fallback must agree with fetchlog.Digest, or a record built outside the log
// would not join to one built inside it.
func TestBuildFallbackHashMatchesSHA256(t *testing.T) {
	body := []byte("some bytes")
	sum := sha256.Sum256(body)
	want := hex.EncodeToString(sum[:])

	rec := Build(Source{URL: "https://example.com/", Body: body, FetchedAt: mustTime()}, nil, Content{})

	if rec.ContentHash != want {
		t.Errorf("fallback hash = %q, want %q", rec.ContentHash, want)
	}
}

func TestMIMETypeParsing(t *testing.T) {
	cases := map[string]string{
		"text/html; charset=utf-8": "text/html",
		"TEXT/HTML":                "text/html",
		"application/pdf":          "application/pdf",
		"text/html;":               "text/html",
		"":                         "",
	}
	for in, want := range cases {
		h := http.Header{}
		if in != "" {
			h.Set("Content-Type", in)
		}
		if got := mimeType(h); got != want {
			t.Errorf("mimeType(%q) = %q, want %q", in, got, want)
		}
	}
	if got := mimeType(nil); got != "" {
		t.Errorf("mimeType(nil) = %q", got)
	}
}

// A detector looked at the text; <html lang> is often a template default nobody
// updated. The detector wins.
func TestLanguagePrefersDetection(t *testing.T) {
	doc := mustDoc(t, `<html lang="en"><body></body></html>`)
	h := http.Header{}
	h.Set("Content-Language", "de")

	if got := language(h, doc, "fr"); got != "fr" {
		t.Errorf("language = %q, want the detected value", got)
	}
	if got := language(h, doc, ""); got != "en" {
		t.Errorf("language = %q, want the document's own claim", got)
	}
	if got := language(h, nil, ""); got != "de" {
		t.Errorf("language = %q, want the header", got)
	}
	if got := language(nil, nil, ""); got != "" {
		t.Errorf("language = %q, want empty", got)
	}
}

func TestLanguageHeaderListTakesTheFirst(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Language", "en-GB, fr")

	if got := language(h, nil, ""); got != "en-gb" {
		t.Errorf("language = %q", got)
	}
}

func TestBuildRecordSerialises(t *testing.T) {
	rec := Build(Source{
		URL: "https://example.com/", ContentHash: "x", FetchedAt: mustTime(),
		Robots: ParseRobots("User-agent: GPTBot\nDisallow: /\n"),
	}, mustDoc(t, `<meta name="tdm-reservation" content="1">`), Content{Text: "t"})

	// A record has to survive the round trip it will make into a corpus file.
	data, err := marshalRecord(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := unmarshalRecord(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if back.URL != rec.URL || back.ContentHash != rec.ContentHash {
		t.Error("identity did not survive the round trip")
	}
	if back.Signals.TDMReservation == nil || *back.Signals.TDMReservation != 1 {
		t.Errorf("TDM reservation did not survive: %v", back.Signals.TDMReservation)
	}
	if back.AIDirectives == nil || len(back.AIDirectives.AgentsBlocked) != 1 {
		t.Error("AI directives did not survive the round trip")
	}
	if !back.FetchedAt.Equal(rec.FetchedAt) {
		t.Errorf("timestamp did not survive: %v vs %v", back.FetchedAt, rec.FetchedAt)
	}
}

// A silent TDM must stay absent through serialisation. If it came back as 0 the
// corpus would record permission the source never gave.
func TestSilentTDMStaysAbsentThroughSerialisation(t *testing.T) {
	rec := Build(Source{URL: "https://example.com/", ContentHash: "x", FetchedAt: mustTime()},
		mustDoc(t, `<html></html>`), Content{})

	data, err := marshalRecord(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := unmarshalRecord(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if back.Signals.TDMReservation != nil {
		t.Errorf("silence became %d after a round trip", *back.Signals.TDMReservation)
	}
}
