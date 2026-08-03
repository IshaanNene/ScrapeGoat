package engine

import (
	"strings"
	"testing"
)

// FuzzCanonicalizeURL covers the function every dedup decision routes through.
//
// Two properties matter. It must not panic on anything url.Parse accepts, and it
// must be idempotent — canonicalising an already-canonical URL must be a no-op, or
// the same page is fetched twice under two different keys.
func FuzzCanonicalizeURL(f *testing.F) {
	for _, seed := range []string{
		"https://example.com",
		"https://example.com/",
		"https://example.com/a/b?z=1&a=2#frag",
		"HTTP://EXAMPLE.COM/PaTh",
		"https://example.com:443/x",
		"http://example.com:80/x",
		"https://example.com/a/../b",
		"https://example.com/a//b///c",
		"https://user:pass@example.com/x",
		"https://例え.テスト/パス",
		"https://example.com/%2e%2e%2f",
		"https://example.com/?utm_source=x&id=1",
		"//example.com/protocol-relative",
		"not a url",
		"",
		":",
		"http://",
		strings.Repeat("https://example.com/", 100),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		got := CanonicalizeURL(raw)

		// Idempotence. Without it, dedup keys drift and the same page is crawled
		// more than once — the exact failure dedup exists to prevent.
		if again := CanonicalizeURL(got); again != got {
			t.Fatalf("CanonicalizeURL is not idempotent:\n  input: %q\n  once:  %q\n  twice: %q",
				raw, got, again)
		}
	})
}

// FuzzDeduplicator checks that the dedup contract holds for arbitrary input:
// the first claim on a URL succeeds and every later claim on the same URL fails.
func FuzzDeduplicator(f *testing.F) {
	for _, seed := range []string{
		"https://example.com/a",
		"https://example.com/a?x=1",
		"",
		"garbage",
		"https://example.com/\x00",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		d := NewDeduplicator(4)

		if !d.MarkIfUnseen(raw) {
			t.Fatalf("first claim on %q was refused", raw)
		}
		if d.MarkIfUnseen(raw) {
			t.Fatalf("second claim on %q was granted", raw)
		}
		if !d.IsSeen(raw) {
			t.Fatalf("%q was claimed but does not read back as seen", raw)
		}
	})
}

// FuzzParseRobotsTxt covers robots.txt parsing. The file comes from the crawl
// target, so it is attacker-controlled, and a panic here takes down the crawl on a
// site the operator does not control.
func FuzzParseRobotsTxt(f *testing.F) {
	for _, seed := range []string{
		"User-agent: *\nDisallow: /private\nAllow: /public\n",
		"User-agent: ScrapeGoat\nCrawl-delay: 2.5\nSitemap: https://example.com/sitemap.xml\n",
		"User-agent: *\nDisallow:\n",
		"# only a comment\n",
		"Disallow: /orphan-rule-with-no-user-agent\n",
		"User-agent: *\nDisallow: /a$\nDisallow: /*.pdf$\nDisallow: /*/x/*\n",
		"User-agent: *\r\nDisallow: /crlf\r\n",
		"Crawl-delay: not-a-number\n",
		"User-agent: *\nDisallow: " + strings.Repeat("/a", 10000) + "\n",
		"",
		":::::",
		"\x00\xff",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, content string) {
		data := parseRobotsTxt(content)
		if data == nil {
			t.Fatal("parseRobotsTxt returned nil, which callers treat as 'fetch failed'")
		}

		// Pattern matching runs against every candidate URL, so it must survive
		// whatever the parse produced.
		for _, pattern := range data.disallowed {
			_ = matchRobotsPattern(pattern, "/some/path")
		}
		for _, pattern := range data.allowed {
			_ = matchRobotsPattern(pattern, "/some/path")
		}
	})
}

// FuzzMatchRobotsPattern fuzzes the wildcard matcher directly, since it does its
// own index arithmetic over the path and is the most likely place for an
// out-of-range panic.
func FuzzMatchRobotsPattern(f *testing.F) {
	f.Add("/a*", "/abc")
	f.Add("/*.pdf$", "/doc.pdf")
	f.Add("*", "/anything")
	f.Add("$", "")
	f.Add("/a*b*c", "/axxbxxc")
	f.Add("", "/x")
	f.Add("/**", "/")

	f.Fuzz(func(t *testing.T, pattern, path string) {
		_ = matchRobotsPattern(pattern, path)
	})
}
