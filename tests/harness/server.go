// Package harness provides adversarial HTTP test servers for stress-testing ScrapeGoat.
// Each mode simulates a specific real-world failure condition.
package harness

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"
)

// Mode identifies an adversarial test scenario.
type Mode int

const (
	ModeInfinitePagination Mode = iota + 1
	ModeRedirectLoop
	ModeSlowDrip
	ModeHugeBomb
	ModeMalformedHTML
	ModeDNSFailure
	ModeTLSEdgeCases
	ModeConcurrencyStampede
	ModeJSHeavy
	ModeRobotsTxtAdversarial
	ModeCookieJarPoisoning  // MODE 11
	ModeGzipBomb           // MODE 12
	ModeCharsetConfusion   // MODE 13
	ModeLinkFarm           // MODE 14
	ModeHoneypotTrap       // MODE 15
)

// ServerConfig configures an adversarial test server.
type ServerConfig struct {
	Mode    Mode
	Variant string // sub-variant (e.g., "expired_cert", "self_signed")
}

// Stats tracks server-side metrics.
type Stats struct {
	RequestsServed atomic.Int64
	ErrorsReturned atomic.Int64
}

// NewServer creates an adversarial httptest.Server for the given mode.
func NewServer(cfg ServerConfig) (*httptest.Server, *Stats) {
	stats := &Stats{}

	var handler http.Handler
	switch cfg.Mode {
	case ModeInfinitePagination:
		handler = infinitePaginationHandler(stats)
	case ModeRedirectLoop:
		handler = redirectLoopHandler(stats)
	case ModeSlowDrip:
		handler = slowDripHandler(stats)
	case ModeHugeBomb:
		handler = hugeBombHandler(stats)
	case ModeMalformedHTML:
		handler = malformedHTMLHandler(stats, cfg.Variant)
	case ModeDNSFailure:
		handler = dnsFailureHandler(stats)
	case ModeConcurrencyStampede:
		handler = stampedeHandler(stats)
	case ModeJSHeavy:
		handler = jsHeavyHandler(stats)
	case ModeRobotsTxtAdversarial:
		handler = robotsTxtAdversarialHandler(stats, cfg.Variant)
	case ModeCookieJarPoisoning:
		handler = cookieJarPoisoningHandler(stats)
	case ModeGzipBomb:
		handler = gzipBombHandler(stats)
	case ModeCharsetConfusion:
		handler = charsetConfusionHandler(stats)
	case ModeLinkFarm:
		handler = linkFarmHandler(stats)
	case ModeHoneypotTrap:
		handler = honeypotTrapHandler(stats)
	default:
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			fmt.Fprint(w, "<html><body>default</body></html>")
		})
	}

	ts := httptest.NewServer(handler)
	return ts, stats
}

// --- MODE 1: Infinite Pagination ---

func infinitePaginationHandler(stats *Stats) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)

		page := 1
		fmt.Sscanf(r.URL.Path, "/page/%d", &page)

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Page %d</title></head>
<body>
<h1>Infinite Pagination - Page %d</h1>
<div class="content">
%s
</div>
<nav class="pagination">
  <a href="/page/%d" class="next">Next Page →</a>
  <a href="/page/%d" class="next2">Skip to page %d</a>
</nav>
</body></html>`, page, page,
			generateFiller(5), // ~5KB of filler content
			page+1, page+10, page+10)
	})
}

// --- MODE 2: Redirect Loop ---

func redirectLoopHandler(stats *Stats) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)

		switch r.URL.Path {
		case "/a", "/":
			http.Redirect(w, r, "/b", http.StatusFound)
		case "/b":
			http.Redirect(w, r, "/c", http.StatusFound)
		case "/c":
			http.Redirect(w, r, "/a", http.StatusFound)
		default:
			http.Redirect(w, r, "/a", http.StatusFound)
		}
	})
}

// --- MODE 3: Slow Drip ---

func slowDripHandler(stats *Stats) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)

		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)

		flusher, ok := w.(http.Flusher)
		// Drip 1 byte every 100ms
		data := []byte("<html><body>" + strings.Repeat("x", 10000) + "</body></html>")
		for i, b := range data {
			_, err := w.Write([]byte{b})
			if err != nil {
				return
			}
			if ok && i%100 == 0 {
				flusher.Flush()
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
}

// --- MODE 4: Huge Response Bomb ---

func hugeBombHandler(stats *Stats) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)

		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)

		// Stream 500MB
		w.Write([]byte("<html><body>"))
		chunk := []byte(strings.Repeat("A", 64*1024)) // 64KB chunks
		for sent := 0; sent < 500*1024*1024; sent += len(chunk) {
			_, err := w.Write(chunk)
			if err != nil {
				return // client disconnected
			}
		}
		w.Write([]byte("</body></html>"))
	})
}

// --- MODE 5: Malformed HTML ---

func malformedHTMLHandler(stats *Stats, variant string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)
		w.Header().Set("Content-Type", "text/html")

		switch variant {
		case "deep_nesting":
			// 10,000 levels of nested divs
			var b strings.Builder
			b.WriteString("<html><body>")
			for i := 0; i < 10000; i++ {
				b.WriteString("<div>")
			}
			b.WriteString("deeply nested content")
			for i := 0; i < 10000; i++ {
				b.WriteString("</div>")
			}
			b.WriteString("</body></html>")
			w.Write([]byte(b.String()))

		case "unclosed_tags":
			w.Write([]byte(`<html><body>
<div><p>Paragraph 1<p>Paragraph 2<div>Nested without close
<table><tr><td>Cell 1<td>Cell 2<tr><td>Cell 3<td>Cell 4
<a href="/link1">Link 1<a href="/link2">Link 2
<img src="test.jpg"><br><hr>
Some text with <b>bold <i>and italic</b> crossed</i> tags
</body></html>`))

		case "mixed_encoding":
			// UTF-8 body declared as ISO-8859-1
			w.Header().Set("Content-Type", "text/html; charset=iso-8859-1")
			w.Write([]byte(`<html><head><meta charset="iso-8859-1"></head>
<body>
<h1>Ünïcödé Tëxt</h1>
<p>Price: €99.99 — Special™ Çhàracters</p>
<p>日本語テスト Chinese: 中文</p>
</body></html>`))

		case "null_bytes":
			content := "<html><body>\x00<h1>Title\x00with\x00nulls</h1>\x00<p>Content\x00here</p></body></html>"
			w.Write([]byte(content))

		default:
			// All combined
			var b strings.Builder
			b.WriteString("<html><body>")
			for i := 0; i < 100; i++ {
				b.WriteString("<div>")
			}
			b.WriteString("<p>Unclosed paragraph<p>Another unclosed")
			b.WriteString("\x00null\x00bytes\x00here")
			b.WriteString("</body></html>")
			w.Write([]byte(b.String()))
		}
	})
}

// --- MODE 6: DNS Failure (simulated via connection handling) ---

func dnsFailureHandler(stats *Stats) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)
		// This handler won't help with DNS failures (since httptest is localhost).
		// DNS failure testing is done by using invalid hostnames in the test itself.
		w.WriteHeader(200)
		fmt.Fprint(w, "<html><body>DNS test placeholder</body></html>")
	})
}

// --- MODE 7: TLS Edge Cases ---
// (TLS servers are created directly in tests using crypto/tls)

// NewTLSServer creates an httptest TLS server with custom certificate behavior.
func NewTLSServer(variant string) (*httptest.Server, *Stats) {
	stats := &Stats{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)
		w.Write([]byte("<html><body>TLS test page</body></html>"))
	})

	ts := httptest.NewTLSServer(handler)

	switch variant {
	case "self_signed":
		// httptest.NewTLSServer already uses a self-signed cert
	case "expired":
		// Can't easily create expired cert at runtime; test skips
	}

	return ts, stats
}

// --- MODE 8: Concurrency Stampede ---

func stampedeHandler(stats *Stats) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)

		// Random delay to simulate varying response times
		time.Sleep(time.Duration(rand.Intn(50)+5) * time.Millisecond)

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body>
<h1>Stampede Test Page</h1>
<p>Request served at %s</p>
<a href="/page/1">Link 1</a>
<a href="/page/2">Link 2</a>
</body></html>`, time.Now().Format(time.RFC3339Nano))
	})
}

// --- MODE 9: JavaScript-Heavy Page ---

func jsHeavyHandler(stats *Stats) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>JS Heavy</title></head>
<body>
<div id="app"></div>
<script>
  document.getElementById('app').innerHTML = '<h1>Rendered by JS</h1><ul>' +
    Array.from({length: 50}, (_, i) =>
      '<li class="item"><a href="/product/' + i + '">Product ' + i + '</a></li>'
    ).join('') + '</ul>';
</script>
<script src="https://cdn.example.com/framework.js"></script>
<script src="https://cdn.example.com/vendor.js"></script>
<noscript><p>Please enable JavaScript</p></noscript>
</body></html>`)
	})
}

// --- MODE 10: Robots.txt Adversarial ---

func robotsTxtAdversarialHandler(stats *Stats, variant string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)

		if r.URL.Path == "/robots.txt" {
			switch variant {
			case "timeout":
				// 5-second delay (simulates timeout)
				time.Sleep(5 * time.Second)
				w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
			case "500":
				w.WriteHeader(500)
				w.Write([]byte("Internal Server Error"))
			case "huge":
				// 10MB robots.txt
				w.Write([]byte("User-agent: *\n"))
				for i := 0; i < 500000; i++ {
					fmt.Fprintf(w, "Disallow: /path-%d/\n", i)
				}
			case "disallow_all":
				w.Write([]byte("User-agent: *\nDisallow: /\n"))
			default:
				w.Write([]byte("User-agent: *\nDisallow: /private/\nAllow: /\n"))
			}
			return
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><h1>Page: %s</h1><a href="/page2">Next</a></body></html>`, r.URL.Path)
	})
}

// --- Helpers ---

func generateFiller(sizeKB int) string {
	var b strings.Builder
	for b.Len() < sizeKB*1024 {
		b.WriteString(fmt.Sprintf(
			"<p>Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. Item #%d.</p>\n",
			rand.Intn(100000)))
	}
	return b.String()
}

// ListenerOnRandomPort returns a net.Listener on a random available port.
func ListenerOnRandomPort() (net.Listener, int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return ln, port, nil
}

// TLSConfigInsecure returns a *tls.Config that skips certificate verification.
func TLSConfigInsecure() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true} // nolint:gosec // Required for tests
}

// TLSConfigWithCA returns a *tls.Config that trusts the given CA certificate pool.
func TLSConfigWithCA(ts *httptest.Server) *tls.Config {
	certPool := x509.NewCertPool()
	certPool.AddCert(ts.Certificate())
	return &tls.Config{RootCAs: certPool, MinVersion: tls.VersionTLS12} // nolint:gosec // Test requires manual control
}

// --- MODE 11: Cookie Jar Poisoning ---

func cookieJarPoisoningHandler(stats *Stats) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)

		// Set 500 cookies on every response
		for i := 0; i < 500; i++ {
			http.SetCookie(w, &http.Cookie{
				Name:    fmt.Sprintf("tracking_%d", i),
				Value:   strings.Repeat("x", 200), // 200-byte value per cookie
				Path:    "/",
				Expires: time.Now().Add(24 * time.Hour),
			})
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><h1>Cookie Jar Test</h1>
<a href="/page/2">Next</a>
<a href="/page/3">Another</a>
</body></html>`)
	})
}

// --- MODE 12: Gzip Bomb ---

func gzipBombHandler(stats *Stats) http.Handler {
	// Pre-compute the gzip bomb: zeros compress well
	var compressed bytes.Buffer
	gw, _ := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	// 1GB of zeros compresses to ~1MB
	chunk := make([]byte, 1024*1024) // 1MB of zeros
	for i := 0; i < 100; i++ {       // 100MB uncompressed (smaller for test speed)
		gw.Write(chunk)
	}
	gw.Close()
	body := compressed.Bytes()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)

		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(200)
		w.Write(body)
	})
}

// --- MODE 13: Charset Confusion ---

func charsetConfusionHandler(stats *Stats) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)

		// Header says UTF-8 but body contains Shift-JIS bytes
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		// Shift-JIS for "テスト" (test) followed by valid HTML
		shiftJISBytes := []byte{0x83, 0x65, 0x83, 0x58, 0x83, 0x67} // テスト in Shift-JIS

		body := fmt.Sprintf(`<html><body>
<h1 class="title">Product Page</h1>
<p class="description">%s</p>
<p class="price">$19.99</p>
<a href="/page/2">Next</a>
</body></html>`, string(shiftJISBytes))

		w.Write([]byte(body))
	})
}

// --- MODE 14: Link Farm ---

func linkFarmHandler(stats *Stats) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)

		w.Header().Set("Content-Type", "text/html")

		var b strings.Builder
		b.WriteString(`<html><body><h1>Link Farm</h1>`)

		// 10,000 unique links
		for i := 0; i < 10000; i++ {
			fmt.Fprintf(&b, `<a href="/item/%d">Item %d</a>\n`, i, i)
		}

		b.WriteString(`</body></html>`)
		w.Write([]byte(b.String()))
	})
}

// --- MODE 15: Honeypot Trap ---

func honeypotTrapHandler(stats *Stats) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats.RequestsServed.Add(1)

		// If crawling hidden link targets, return a warning
		if strings.HasPrefix(r.URL.Path, "/trap/") {
			w.WriteHeader(403)
			w.Write([]byte(`<html><body><h1>TRAP TRIGGERED</h1>
<p>You followed a honeypot link. Real scrapers should skip hidden links.</p>
</body></html>`))
			stats.ErrorsReturned.Add(1)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>
<h1>Main Content</h1>
<p>This is the visible content.</p>
<a href="/page/2">Visible Link</a>

<!-- Hidden honeypot links that scrapers should ignore -->
<a href="/trap/legal" style="display:none">Legal Warning</a>
<a href="/trap/admin" style="font-size:0px">Admin Panel</a>
<a href="/trap/secret" style="visibility:hidden">Secret Page</a>
<div style="position:absolute;left:-9999px">
  <a href="/trap/offscreen">Offscreen Trap</a>
</div>
<span style="color:#fff;background:#fff">
  <a href="/trap/invisible">Invisible Text Trap</a>
</span>
</body></html>`)
	})
}
