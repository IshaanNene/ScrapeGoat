package provenance

import (
	"net/http"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

// Everything fuzzed here consumes bytes chosen by the site being crawled. A panic
// in any of it is a crawl killed by a page that wanted to kill it, and these
// parsers run on every response — so "survives hostile input" is a correctness
// property rather than a nicety.
//
// The invariants asserted are the ones a corpus depends on. It is not enough that
// these functions do not crash: a garbled robots.txt must not be reported as
// permission, and a malformed page must not come back claiming a reservation it
// never made. Silence and consent are different answers, and fuzzing is where
// that distinction gets tested against inputs nobody thought to write down.

func FuzzParseRobots(f *testing.F) {
	f.Add("User-agent: *\nDisallow: /\n")
	f.Add("User-agent: GPTBot\nDisallow: /\n\nUser-agent: *\nAllow: /\n")
	f.Add("# comment only\n")
	f.Add("")
	f.Add("Disallow: /orphan\n")
	f.Add("User-agent:\nDisallow:\n")
	f.Add("User-agent: *\nUser-agent: CCBot\nDisallow: /\n")
	f.Add("Sitemap: https://example.com/s.xml\n")
	f.Add(strings.Repeat("User-agent: a\n", 500))
	f.Add("user-AGENT: GPTBOT\nDISALLOW: /\n")

	f.Fuzz(func(t *testing.T, content string) {
		report := ParseRobots(content)

		// Parsing something always means a robots.txt was seen.
		if !report.Present {
			t.Fatal("ParseRobots returned Present false")
		}

		// Every reported blocked agent must be one this file actually named, and
		// must be an AI agent. Reporting a block that was never written would put
		// a restriction in the site's mouth.
		addressed := map[string]bool{}
		for _, g := range report.Groups {
			for _, a := range g.Agents {
				addressed[a] = true
			}
		}
		for _, blocked := range report.AIAgentsBlocked {
			if !addressed[blocked] {
				t.Fatalf("reported %q as blocked, but no group named it", blocked)
			}
			if !isAIAgentToken(blocked) {
				t.Fatalf("reported non-AI agent %q under AIAgentsBlocked", blocked)
			}
		}

		// Blocked must be a subset of addressed: you cannot be turned away by a
		// file that never mentions you.
		inAddressed := map[string]bool{}
		for _, a := range report.AIAgentsAddressed {
			inAddressed[a] = true
		}
		for _, b := range report.AIAgentsBlocked {
			if !inAddressed[b] {
				t.Fatalf("%q is blocked but not addressed", b)
			}
		}

		// A group with an Allow is never blanket, whatever else it holds.
		for _, g := range report.Groups {
			if g.BlanketDisallow && len(g.Allow) > 0 {
				t.Fatalf("group %v is blanket despite %d Allow rules", g.Agents, len(g.Allow))
			}
		}

		// Sorted output is part of the contract: a corpus written twice from the
		// same crawl must not differ in field order.
		assertSorted(t, "AIAgentsAddressed", report.AIAgentsAddressed)
		assertSorted(t, "AIAgentsBlocked", report.AIAgentsBlocked)

		// Re-parsing must give the same answer; parsing is pure.
		again := ParseRobots(content)
		if len(again.AIAgentsBlocked) != len(report.AIAgentsBlocked) {
			t.Fatal("ParseRobots is not deterministic")
		}
	})
}

func FuzzFromDocument(f *testing.F) {
	f.Add(`<meta name="robots" content="noai">`)
	f.Add(`<meta name="tdm-reservation" content="1">`)
	f.Add(`<meta name="tdm-reservation" content="not-a-number">`)
	f.Add(`<link rel="license" href="https://example.com/l">`)
	f.Add(`<link rel="canonical" href="">`)
	f.Add(`<html><body><p>nothing</p></body></html>`)
	f.Add(`<meta name="robots">`)
	f.Add(`<meta name="gptbot" content="noindex">`)
	f.Add(strings.Repeat(`<meta name="robots" content="noai">`, 200))
	f.Add(`<<<>>><meta name="robots" content="noindex,,,,noai">`)

	f.Fuzz(func(t *testing.T, html string) {
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
		if err != nil {
			return // not our concern; the parser rejected it
		}

		s := FromDocument(doc)

		// A reservation is only ever 0 or 1. Anything else means a garbled value
		// was guessed at, and the corpus would carry a number the page never wrote.
		if s.TDMReservation != nil {
			if v := *s.TDMReservation; v != 0 && v != 1 {
				t.Fatalf("TDM reservation = %d, which the protocol does not define", v)
			}
		}

		// Restrictive is a claim about what the page said. It must never be true on
		// the strength of noindex alone, which is a statement about search engines.
		if s.Restrictive() && !s.NoAI && !s.NoImageAI {
			if s.TDMReservation == nil || *s.TDMReservation != 1 {
				t.Fatalf("Restrictive true with no restriction behind it: %+v", s)
			}
		}

		// A licence source without a licence is a record that claims provenance it
		// does not have.
		if s.LicenceSource != "" && s.Licence == "" {
			t.Fatalf("licence source %q with no licence", s.LicenceSource)
		}

		// Merging with an empty other side must not invent a restriction.
		if m := Merge(PageSignals{}, s); m.Restrictive() != s.Restrictive() {
			t.Fatalf("merging with nothing changed restrictiveness: %+v vs %+v", m, s)
		}
	})
}

func FuzzFromHeaders(f *testing.F) {
	f.Add("noai", "1", `<https://e/l>; rel="license"`)
	f.Add("noindex, nofollow", "0", "")
	f.Add("googlebot: noindex", "banana", "malformed")
	f.Add("", "", "")
	f.Add("none", "1", `<>; rel="license"`)
	f.Add(strings.Repeat("noai,", 500), "1", strings.Repeat(`<https://e>; rel="license",`, 200))

	f.Fuzz(func(t *testing.T, robotsTag, tdm, link string) {
		h := http.Header{}
		if robotsTag != "" {
			h.Set("X-Robots-Tag", robotsTag)
		}
		if tdm != "" {
			h.Set("TDM-Reservation", tdm)
		}
		if link != "" {
			h.Set("Link", link)
		}

		s := FromHeaders(h)

		if s.TDMReservation != nil {
			if v := *s.TDMReservation; v != 0 && v != 1 {
				t.Fatalf("TDM reservation = %d from header %q", v, tdm)
			}
			// Only an exact "0" or "1" may produce a value. Anything else must be
			// read as the header having said nothing.
			if trimmed := strings.TrimSpace(tdm); trimmed != "0" && trimmed != "1" {
				t.Fatalf("header %q produced a reservation of %d", tdm, *s.TDMReservation)
			}
		}

		if s.LicenceSource != "" && s.Licence == "" {
			t.Fatalf("licence source %q with no licence", s.LicenceSource)
		}
	})
}

// Build runs on every response, over bytes the site chose.
func FuzzBuild(f *testing.F) {
	f.Add("https://example.com/", `<meta name="robots" content="noai">`, "User-agent: GPTBot\nDisallow: /\n")
	f.Add("", "", "")
	f.Add("::not a url::", "<html>", "User-agent:")
	f.Add("https://e/", strings.Repeat("<div>", 200), strings.Repeat("Disallow: /\n", 200))

	f.Fuzz(func(t *testing.T, rawURL, html, robots string) {
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
		if err != nil {
			doc = nil
		}

		rec := Build(Source{
			URL:       rawURL,
			Body:      []byte(html),
			FetchedAt: mustTime(),
			Robots:    ParseRobots(robots),
		}, doc, Content{})

		// Complete is what the corpus writer gates on. It must never be true
		// without the three things it names, or an unusable record reaches a file
		// that promises every row can be traced.
		if rec.Complete() && (rec.URL == "" || rec.ContentHash == "" || rec.FetchedAt.IsZero()) {
			t.Fatalf("Complete true on %+v", rec)
		}

		// A body always yields a hash, so a record is only incomplete for want of
		// a URL here.
		if rec.ContentHash == "" {
			t.Fatal("a record built from a body carries no content hash")
		}

		// Directives are attached only when a robots.txt was seen — and ParseRobots
		// always reports Present, so they must always be attached here.
		if rec.AIDirectives == nil {
			t.Fatal("directives missing despite a parsed robots.txt")
		}
	})
}

func assertSorted(t *testing.T, name string, vals []string) {
	t.Helper()
	for i := 1; i < len(vals); i++ {
		if vals[i-1] > vals[i] {
			t.Fatalf("%s is not sorted: %v", name, vals)
		}
	}
}
