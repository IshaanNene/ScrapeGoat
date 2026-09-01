package engine

import (
	"net/url"
	"testing"
)

func urlParse(s string) (*url.URL, error) { return url.Parse(s) }

// TestCanonicalizeURLHostHandling covers the two ways removing a default port
// used to damage the host it was removing the port from.
func TestCanonicalizeURLHostHandling(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// The ordinary case, which always worked.
		{"named host", "http://example.com:80/a", "http://example.com/a"},
		{"https default", "https://example.com:443/a", "https://example.com/a"},
		{"non-default port kept", "http://example.com:8080/a", "http://example.com:8080/a"},

		// Hostname() strips the brackets from an IPv6 literal and String() does
		// not restore them, so this produced http://::1/a — not a URL.
		{"ipv6 default port", "http://[::1]:80/a", "http://[::1]/a"},
		{"ipv6 https default", "https://[2001:db8::1]:443/a", "https://[2001:db8::1]/a"},
		{"ipv6 non-default port", "http://[::1]:8080/a", "http://[::1]:8080/a"},
		{"ipv6 no port", "http://[::1]/a", "http://[::1]/a"},

		// Hostname() splits on the last colon, so this yielded the "hostname"
		// 0000:80 and stripping left something that still looked like host:port.
		{"malformed double port", "http://0000:80:80", "http://0000:80:80/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanonicalizeURL(tt.in); got != tt.want {
				t.Errorf("CanonicalizeURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestCanonicalizeURLIsIdempotent states the property the fuzz target checks, as
// a unit test over the cases that broke it.
//
// Idempotence is not a nicety here. A URL reaches the deduplicator already
// canonicalised in some paths and raw in others; if canonicalising twice differs
// from canonicalising once, the same page gets two keys and is crawled twice.
func TestCanonicalizeURLIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"http://0000:80:80",
		"http://[::1]:80/a",
		"https://[2001:db8::1]:443/a",
		"http://example.com:80/a",
		"http://example.com:80",
		"https://example.com:443/?b=2&a=1",
		"http://[::1]:8080/a/",
	} {
		t.Run(in, func(t *testing.T) {
			once := CanonicalizeURL(in)
			twice := CanonicalizeURL(once)
			if once != twice {
				t.Errorf("not idempotent:\n input: %q\n once:  %q\n twice: %q", in, once, twice)
			}
		})
	}
}

// TestCanonicalizedIPv6ReparsesToTheSameHost is the check that would have caught
// the bracket bug directly: a canonical URL has to survive being read back.
func TestCanonicalizedIPv6ReparsesToTheSameHost(t *testing.T) {
	got := CanonicalizeURL("http://[::1]:80/a")
	u, err := urlParse(got)
	if err != nil {
		t.Fatalf("canonical form %q does not parse: %v", got, err)
	}
	if u.Hostname() != "::1" {
		t.Errorf("canonical form %q parses back to host %q, want %q", got, u.Hostname(), "::1")
	}
}
