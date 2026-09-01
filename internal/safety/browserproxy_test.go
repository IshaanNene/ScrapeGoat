package safety

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestGuardedProxyRefusesConnectToLinkLocal is the browser-substrate half of the
// SSRF defence: the CONNECT tunnel is where an https fetch of the cloud metadata
// endpoint would be established, so it is where it has to be refused.
func TestGuardedProxyRefusesConnectToLinkLocal(t *testing.T) {
	p := startTestProxy(t)

	for _, target := range []string{
		"169.254.169.254:80",
		"127.0.0.1:8080",
		"[::1]:80",
		"10.0.0.1:443",
		"metadata.google.internal:80",
	} {
		t.Run(target, func(t *testing.T) {
			conn, err := net.Dial("tcp", p.Addr())
			if err != nil {
				t.Fatalf("dial proxy: %v", err)
			}
			defer conn.Close()

			if _, err := io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"); err != nil {
				t.Fatalf("write CONNECT: %v", err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
			if err != nil {
				// A refused tunnel that also drops the connection is still a refusal.
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Fatalf("CONNECT %s established a tunnel; the guard must refuse it", target)
			}
		})
	}
}

// TestGuardedProxyRefusesPlainHTTPToLinkLocal covers the absolute-form request a
// browser sends for an http:// page, the other half of the same surface.
func TestGuardedProxyRefusesPlainHTTPToLinkLocal(t *testing.T) {
	p := startTestProxy(t)

	client := &http.Client{Transport: &http.Transport{Proxy: proxyFunc(t, p.Addr())}}
	resp, err := client.Get("http://169.254.169.254/latest/meta-data/")
	if err != nil {
		return // refused at the transport; also a pass
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		t.Fatalf("proxy fetched link-local metadata: status %d, body %q", resp.StatusCode, body)
	}
}

// TestGuardedProxyForwardsPublicRequests checks the guard is a filter and not a
// wall — a proxy that refuses everything would pass the tests above and break
// every real fetch.
func TestGuardedProxyForwardsPublicRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello from upstream")
	}))
	defer upstream.Close()

	// The httptest server is on loopback, which the guard blocks by design, so this
	// case needs the same explicit opt-out an operator crawling an internal host
	// would use.
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	g := New(Config{AllowedPrivateHosts: []string{u.Hostname()}})
	p, err := StartGuardedProxy(g)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	client := &http.Client{Transport: &http.Transport{Proxy: proxyFunc(t, p.Addr())}}
	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatalf("proxied GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello from upstream") {
		t.Fatalf("body = %q, want the upstream response", body)
	}
}

func TestGuardedProxyRequiresGuard(t *testing.T) {
	if _, err := StartGuardedProxy(nil); err == nil {
		t.Fatal("StartGuardedProxy(nil) = nil error; a nil guard must not yield an open proxy")
	}
}

func startTestProxy(t *testing.T) *GuardedProxy {
	t.Helper()
	p, err := StartGuardedProxy(Default())
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func proxyFunc(t *testing.T, addr string) func(*http.Request) (*url.URL, error) {
	t.Helper()
	u, err := url.Parse("http://" + addr)
	if err != nil {
		t.Fatalf("parse proxy addr: %v", err)
	}
	return http.ProxyURL(u)
}
