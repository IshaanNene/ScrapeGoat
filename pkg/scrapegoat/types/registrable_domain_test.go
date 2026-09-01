package types

import "testing"

func TestRegistrableDomain(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		// The case the throttler was getting wrong: every subdomain of a site must
		// resolve to one key, or one crawl multiplies the configured rate by the
		// number of subdomains it happens to touch.
		{"subdomain", "https://a.example.com/p", "example.com"},
		{"deep subdomain", "https://x.y.z.example.com/p", "example.com"},
		{"apex", "https://example.com/p", "example.com"},
		{"www", "https://www.example.com/p", "example.com"},

		// A multi-label public suffix must not be mistaken for a registrable
		// domain, or "a.bbc.co.uk" and "b.bbc.co.uk" collapse to "co.uk" and every
		// British site shares one budget.
		{"multi-label suffix", "https://a.bbc.co.uk/p", "bbc.co.uk"},
		{"private suffix", "https://user.github.io/repo", "user.github.io"},

		// No registrable domain exists; the hostname is the most conservative key.
		{"ipv4 literal", "http://192.0.2.1/p", "192.0.2.1"},
		{"ipv6 literal", "http://[2001:db8::1]/p", "2001:db8::1"},
		{"no public suffix", "http://localhost:8080/p", "localhost"},
		{"bare suffix", "http://com/p", "com"},

		// Normalisation: the FQDN root dot and case must not split one site's key.
		{"trailing dot", "https://a.example.com./p", "example.com"},
		{"uppercase", "https://A.EXAMPLE.COM/p", "example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := NewRequest(tt.url)
			if err != nil {
				t.Fatalf("NewRequest(%q): %v", tt.url, err)
			}
			if got := req.RegistrableDomain(); got != tt.want {
				t.Errorf("RegistrableDomain() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRegistrableDomainSharedAcrossSubdomains states the politeness invariant
// directly: these are one site, so they are one budget.
func TestRegistrableDomainSharedAcrossSubdomains(t *testing.T) {
	var keys []string
	for _, raw := range []string{
		"https://a.example.com/", "https://b.example.com/",
		"https://c.d.example.com/", "https://example.com/",
	} {
		req, err := NewRequest(raw)
		if err != nil {
			t.Fatalf("NewRequest(%q): %v", raw, err)
		}
		keys = append(keys, req.RegistrableDomain())
	}
	for _, k := range keys {
		if k != keys[0] {
			t.Fatalf("subdomains of one site produced different politeness keys: %v", keys)
		}
	}
}

// TestRegistrableDomainSeparatesDistinctSites guards the other direction: a key
// that merged unrelated sites would throttle them against each other.
func TestRegistrableDomainSeparatesDistinctSites(t *testing.T) {
	a, _ := NewRequest("https://a.bbc.co.uk/")
	b, _ := NewRequest("https://a.itv.co.uk/")
	if a.RegistrableDomain() == b.RegistrableDomain() {
		t.Fatalf("distinct sites share a key %q", a.RegistrableDomain())
	}
}

// TestRegistrableDomainSurvivesClone: a retry is a clone, and a retry that lost
// the politeness key would be throttled under "".
func TestRegistrableDomainSurvivesClone(t *testing.T) {
	req, err := NewRequest("https://a.example.com/p")
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if got := req.Clone().RegistrableDomain(); got != "example.com" {
		t.Errorf("clone RegistrableDomain() = %q, want %q", got, "example.com")
	}
}

// TestRegistrableDomainOnStructLiteral covers a Request that did not come from
// NewRequest and so has no cached value.
func TestRegistrableDomainOnStructLiteral(t *testing.T) {
	req, err := NewRequest("https://a.example.com/p")
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	bare := &Request{URL: req.URL}
	if got := bare.RegistrableDomain(); got != "example.com" {
		t.Errorf("uncached RegistrableDomain() = %q, want %q", got, "example.com")
	}
	if got := (&Request{}).RegistrableDomain(); got != "" {
		t.Errorf("nil-URL RegistrableDomain() = %q, want empty", got)
	}
}
