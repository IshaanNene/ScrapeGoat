package engine

import (
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

func TestDomainMatches(t *testing.T) {
	tests := []struct {
		rule string
		host string
		want bool
		note string
	}{
		// The case that motivated the change: exact matching made an
		// allowed_domains rule reject the site's own www host.
		{"example.com", "www.example.com", true, "subdomain of rule"},
		{"example.com", "example.com", true, "exact"},
		{"example.com", "a.b.c.example.com", true, "deep subdomain"},
		{"www.example.com", "www.example.com", true, "exact www rule"},

		// Label-boundary discipline: a suffix match on raw strings would let
		// notexample.com through a rule of example.com.
		{"example.com", "notexample.com", false, "no label boundary"},
		{"example.com", "example.com.evil.test", false, "rule appears mid-host"},
		{"example.com", "example.org", false, "different TLD"},
		{"www.example.com", "example.com", false, "parent is not under the rule"},

		// A rule that is itself a public suffix must not swallow everything
		// registered beneath it.
		{"com", "example.com", false, "bare TLD rule"},
		{"co.uk", "example.co.uk", false, "multi-label public suffix rule"},
		{"github.io", "someone.github.io", false, "private registry public suffix"},
		{"com", "com", true, "public suffix still matches itself exactly"},

		// Normalisation.
		{"EXAMPLE.COM", "www.Example.com", true, "case insensitive"},
		{"example.com.", "www.example.com", true, "trailing root dot in rule"},
		{"example.com", "www.example.com:8080", true, "port stripped from host"},
		{" example.com ", "www.example.com", true, "whitespace trimmed"},

		{"", "example.com", false, "empty rule"},
		{"example.com", "", false, "empty host"},
	}

	for _, tt := range tests {
		t.Run(tt.note, func(t *testing.T) {
			if got := domainMatches(tt.rule, tt.host); got != tt.want {
				t.Errorf("domainMatches(%q, %q) = %v, want %v", tt.rule, tt.host, got, tt.want)
			}
		})
	}
}

func TestIsDomainAllowed(t *testing.T) {
	tests := []struct {
		name     string
		allowed  []string
		disallow []string
		host     string
		want     bool
	}{
		{"no lists allows everything", nil, nil, "example.com", true},
		{"allowlist admits subdomains", []string{"example.com"}, nil, "www.example.com", true},
		{"allowlist excludes others", []string{"example.com"}, nil, "other.test", false},
		{"allowlist with several rules", []string{"a.test", "b.test"}, nil, "x.b.test", true},
		{"denylist blocks subdomains", nil, []string{"ads.test"}, "cdn.ads.test", false},
		{"denylist passes others", nil, []string{"ads.test"}, "example.com", true},
		// The allowlist takes precedence; a denylist entry is not consulted when an
		// allowlist is set, so this must not be read as "allowed but then denied".
		{"allowlist wins over denylist", []string{"example.com"}, []string{"example.com"}, "example.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testutil.LoopbackConfig()
			cfg.Engine.AllowedDomains = tt.allowed
			cfg.Engine.DisallowedDomains = tt.disallow

			eng := New(cfg, concurrencyLogger)
			if got := eng.isDomainAllowed(tt.host); got != tt.want {
				t.Errorf("isDomainAllowed(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

// TestAddRequestAcceptsWWWUnderAllowedDomain is the end-to-end version: the bug
// showed up as a crawl of example.com finding nothing, because the site's own
// www host was filtered out at the first link.
func TestAddRequestAcceptsWWWUnderAllowedDomain(t *testing.T) {
	cfg := testutil.LoopbackConfig()
	cfg.Engine.RespectRobotsTxt = false
	cfg.Engine.AllowedDomains = []string{"example.com"}

	eng := New(cfg, concurrencyLogger)

	req, err := types.NewRequest("https://www.example.com/page")
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := eng.AddRequest(req); err != nil {
		t.Fatalf("www.example.com was rejected under an example.com rule: %v", err)
	}
}

func FuzzDomainMatches(f *testing.F) {
	f.Add("example.com", "www.example.com")
	f.Add("com", "example.com")
	f.Add("co.uk", "example.co.uk")
	f.Add("", "")
	f.Add("...", "..")
	f.Add("example.com", "example.com.evil.test")

	f.Fuzz(func(t *testing.T, rule, host string) {
		// Must not panic, and must never match across a label boundary — the
		// property that keeps notexample.com out of an example.com crawl.
		if domainMatches(rule, host) {
			r, h := normaliseHost(rule), normaliseHost(host)
			if r != h && !hasSuffixAtLabel(h, r) {
				t.Fatalf("matched %q against rule %q without a label boundary", host, rule)
			}
		}
	})
}

func hasSuffixAtLabel(host, rule string) bool {
	return len(host) > len(rule) &&
		host[len(host)-len(rule)-1] == '.' &&
		host[len(host)-len(rule):] == rule
}
