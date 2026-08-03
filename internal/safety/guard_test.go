package safety

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestValidateURLSchemes(t *testing.T) {
	g := Default()

	tests := []struct {
		name string
		url  string
		want error
	}{
		{"http allowed", "http://example.com/a", nil},
		{"https allowed", "https://example.com/a", nil},
		{"uppercase scheme allowed", "HTTPS://example.com/a", nil},
		{"file blocked", "file:///etc/passwd", ErrSchemeNotAllowed},
		{"gopher blocked", "gopher://example.com/1", ErrSchemeNotAllowed},
		{"ftp blocked", "ftp://example.com/x", ErrSchemeNotAllowed},
		{"data blocked", "data:text/html,<h1>x</h1>", ErrSchemeNotAllowed},
		{"jar blocked", "jar:http://example.com/a!/b", ErrSchemeNotAllowed},
		// url.Parse accepts these as relative references with no scheme and no host,
		// which is exactly how a bare string sneaks past a naive check.
		{"bare word rejected", "garbage", ErrSchemeNotAllowed},
		{"empty rejected", "", ErrSchemeNotAllowed},
		{"scheme but no host", "http://", ErrNoHost},
		{"path only", "/just/a/path", ErrSchemeNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := g.ValidateURL(tt.url)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("ValidateURL(%q) = %v, want nil", tt.url, err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("ValidateURL(%q) = %v, want %v", tt.url, err, tt.want)
			}
		})
	}
}

func TestValidateURLCustomSchemes(t *testing.T) {
	g := New(Config{AllowedSchemes: []string{"https"}})

	if err := g.ValidateURL("https://example.com"); err != nil {
		t.Errorf("https should be allowed: %v", err)
	}
	if err := g.ValidateURL("http://example.com"); !errors.Is(err, ErrSchemeNotAllowed) {
		t.Errorf("http should be blocked when only https is allowed, got %v", err)
	}
}

// TestBlockReason is the table that matters most: every address family that must
// never be dialled, with the metadata endpoint called out explicitly.
func TestBlockReason(t *testing.T) {
	tests := []struct {
		ip      string
		blocked bool
		note    string
	}{
		// The one that turns an SSRF into stolen cloud credentials.
		{"169.254.169.254", true, "AWS/GCP/Azure metadata"},
		{"169.254.0.1", true, "link-local"},
		{"fe80::1", true, "IPv6 link-local"},

		{"127.0.0.1", true, "loopback"},
		{"127.1.2.3", true, "loopback range"},
		{"::1", true, "IPv6 loopback"},
		{"0.0.0.0", true, "unspecified"},
		{"::", true, "IPv6 unspecified"},

		{"10.0.0.1", true, "RFC1918 /8"},
		{"172.16.0.1", true, "RFC1918 /12"},
		{"172.31.255.255", true, "RFC1918 /12 upper"},
		{"192.168.1.1", true, "RFC1918 /16"},
		{"fd00::1", true, "IPv6 unique-local"},

		{"100.64.0.1", true, "CGNAT"},
		{"198.18.0.1", true, "benchmarking"},
		{"224.0.0.1", true, "multicast"},
		{"240.0.0.1", true, "reserved"},
		{"192.0.2.1", true, "TEST-NET-1"},
		{"192.88.99.1", true, "6to4 relay"},

		// IPv4-mapped IPv6 must be judged on the embedded IPv4 address.
		{"::ffff:127.0.0.1", true, "IPv4-mapped loopback"},
		{"::ffff:169.254.169.254", true, "IPv4-mapped metadata"},
		{"::ffff:10.0.0.1", true, "IPv4-mapped private"},

		// NAT64 well-known prefix wrapping a private address.
		{"64:ff9b::7f00:1", true, "NAT64-embedded loopback"},
		{"64:ff9b::a9fe:a9fe", true, "NAT64-embedded metadata"},

		// Ordinary public addresses must still work.
		{"93.184.216.34", false, "public IPv4"},
		{"8.8.8.8", false, "public IPv4"},
		{"172.32.0.1", false, "just outside RFC1918 /12"},
		{"100.128.0.1", false, "just outside CGNAT"},
		{"2606:2800:220:1:248:1893:25c8:1946", false, "public IPv6"},
	}

	for _, tt := range tests {
		t.Run(tt.ip+"/"+tt.note, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("test bug: %q is not a valid IP", tt.ip)
			}
			reason := blockReason(ip)
			if tt.blocked && reason == "" {
				t.Errorf("%s (%s) should be blocked, was allowed", tt.ip, tt.note)
			}
			if !tt.blocked && reason != "" {
				t.Errorf("%s (%s) should be allowed, was blocked as %q", tt.ip, tt.note, reason)
			}
		})
	}
}

func TestDialContextBlocksLiteralAddresses(t *testing.T) {
	g := Default()

	for _, addr := range []string{
		"169.254.169.254:80",
		"127.0.0.1:6379",
		"10.0.0.1:80",
		"[::1]:80",
		"[::ffff:127.0.0.1]:80",
	} {
		t.Run(addr, func(t *testing.T) {
			conn, err := g.DialContext(context.Background(), "tcp", addr)
			if err == nil {
				_ = conn.Close()
				t.Fatalf("dial to %s succeeded, expected it to be blocked", addr)
			}
			if !errors.Is(err, ErrBlockedAddress) {
				t.Fatalf("dial to %s = %v, want ErrBlockedAddress", addr, err)
			}
		})
	}
}

// TestDialContextBlocksRebinding covers the case the hostname check misses: a name
// that looks innocuous but resolves to a private address.
func TestDialContextBlocksRebinding(t *testing.T) {
	// localhost is the simplest name guaranteed to resolve to loopback on any host
	// running these tests, so it stands in for evil.example.com → 127.0.0.1.
	g := Default()

	conn, err := g.DialContext(context.Background(), "tcp", "localhost:80")
	if err == nil {
		_ = conn.Close()
		t.Fatal("dial to localhost succeeded — a name resolving to loopback must be blocked")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("dial to localhost = %v, want ErrBlockedAddress", err)
	}
}

func TestAllowPrivateAddressesOptOut(t *testing.T) {
	// A real listener on loopback, which the default guard refuses to reach.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	addr := net.JoinHostPort(u.Hostname(), u.Port())

	t.Run("blocked by default", func(t *testing.T) {
		if conn, err := Default().DialContext(context.Background(), "tcp", addr); err == nil {
			_ = conn.Close()
			t.Fatal("expected loopback to be blocked by default")
		}
	})

	t.Run("allowed when opted in", func(t *testing.T) {
		g := New(Config{AllowPrivateAddresses: true})
		conn, err := g.DialContext(context.Background(), "tcp", addr)
		if err != nil {
			t.Fatalf("expected loopback to be reachable with AllowPrivateAddresses, got %v", err)
		}
		_ = conn.Close()
	})

	t.Run("allowed for an exempt host", func(t *testing.T) {
		g := New(Config{AllowedPrivateHosts: []string{u.Hostname()}})
		conn, err := g.DialContext(context.Background(), "tcp", addr)
		if err != nil {
			t.Fatalf("expected exempt host to be reachable, got %v", err)
		}
		_ = conn.Close()
	})
}

func TestCheckRedirectBlocksSchemeChange(t *testing.T) {
	g := Default()
	check := g.CheckRedirect(10)

	// A 302 to file:// never reaches the dialer, so it has to be caught here.
	req := &http.Request{URL: mustParse(t, "file:///etc/passwd")}
	err := check(req, nil)
	if !errors.Is(err, ErrSchemeNotAllowed) {
		t.Fatalf("redirect to file:// = %v, want ErrSchemeNotAllowed", err)
	}

	ok := &http.Request{URL: mustParse(t, "https://example.com/next")}
	if err := check(ok, nil); err != nil {
		t.Fatalf("ordinary https redirect was blocked: %v", err)
	}
}

func TestCheckRedirectEnforcesLimit(t *testing.T) {
	g := Default()
	check := g.CheckRedirect(3)

	via := make([]*http.Request, 3)
	err := check(&http.Request{URL: mustParse(t, "https://example.com/4")}, via)
	if err == nil || !strings.Contains(err.Error(), "stopped after 3 redirects") {
		t.Fatalf("expected the redirect limit to fire, got %v", err)
	}
}

// TestRedirectToMetadataIsBlockedEndToEnd wires a real client through the guard and
// bounces it at the metadata address, which is the actual attack rather than a unit
// of it.
func TestRedirectToMetadataIsBlockedEndToEnd(t *testing.T) {
	g := New(Config{AllowPrivateAddresses: false})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer ts.Close()

	// The test server itself is on loopback, so exempt it — the point of this test is
	// the redirect target, not the origin.
	u := mustParse(t, ts.URL)
	g = New(Config{AllowedPrivateHosts: []string{u.Hostname()}})

	client := &http.Client{
		Transport:     &http.Transport{DialContext: g.DialContext},
		CheckRedirect: g.CheckRedirect(10),
	}

	resp, err := client.Get(ts.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("redirect to the metadata endpoint was followed")
	}
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("got %v, want ErrBlockedAddress", err)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// FuzzValidateURL fuzzes the scheme/host check.
//
// This is the guard's cheapest layer and the one most exposed to arbitrary strings,
// since MCP tool arguments and REST bodies both land here. Anything it accepts must
// be an http/https URL with a host; a gap is an SSRF hole.
func FuzzValidateURL(f *testing.F) {
	for _, seed := range []string{
		"https://example.com",
		"http://example.com:8080/a?b=c#d",
		"file:///etc/passwd",
		"gopher://127.0.0.1:6379/_INFO",
		"jar:http://example.com/a!/b",
		"data:text/html,<h1>x</h1>",
		"HTTPS://EXAMPLE.COM",
		"https://user:pass@example.com",
		"https://[::1]:80/",
		"//protocol-relative/x",
		"http://",
		"https://xn--e1afmkfd.xn--p1ai/",
		"garbage",
		"",
		" https://example.com",
		"https://example.com\n",
	} {
		f.Add(seed)
	}

	g := Default()

	f.Fuzz(func(t *testing.T, raw string) {
		if err := g.ValidateURL(raw); err != nil {
			return
		}

		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("accepted a URL that does not parse: %q", raw)
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
		default:
			t.Fatalf("accepted scheme %q from %q", u.Scheme, raw)
		}
		if u.Hostname() == "" {
			t.Fatalf("accepted a URL with no host: %q", raw)
		}
	})
}

// FuzzBlockReason fuzzes the address classifier over raw 16-byte addresses, which
// reaches shapes textual IP parsing will not produce.
func FuzzBlockReason(f *testing.F) {
	for _, seed := range []string{
		"127.0.0.1", "169.254.169.254", "10.0.0.1", "8.8.8.8", "::1",
		"::ffff:127.0.0.1", "64:ff9b::7f00:1", "fe80::1", "fd00::1",
	} {
		if ip := net.ParseIP(seed); ip != nil {
			f.Add([]byte(ip.To16()))
		}
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) != 4 && len(raw) != 16 {
			return
		}
		ip := net.IP(raw)

		// Must not panic, and must never allow an address the stdlib itself
		// classifies as non-public — that combination is the whole guard.
		reason := blockReason(ip)
		if reason == "" {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
				ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
				t.Fatalf("allowed non-public address %v", ip)
			}
		}
	})
}
