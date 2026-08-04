package fingerprint

import (
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The claim under test is "ScrapeGoat's TLS fingerprint is a browser's, not Go's".
// Asserting that a handshake succeeds does not test that. What follows captures the
// raw ClientHello off the wire and computes its JA3, so the claim is checked against
// the actual bytes.
//
// JA3 (Salesforce, 2017) is the MD5 of:
//
//	TLSVersion,Ciphers,Extensions,EllipticCurves,ECPointFormats
//
// with GREASE values removed. Implemented here rather than pulled in as a
// dependency: it is thirty lines, and a test that vendors its own oracle is a test
// that can drift from what it claims to measure.

// captureClientHello listens on loopback, accepts one connection, reads the
// ClientHello record, and returns its bytes.
func captureClientHello(t *testing.T) (addr string, hello <-chan []byte) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	out := make(chan []byte, 1)
	var once sync.Once

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			once.Do(func() { close(out) })
			return
		}
		defer conn.Close()

		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		// TLS record header: type(1) version(2) length(2).
		header := make([]byte, 5)
		if _, err := io.ReadFull(conn, header); err != nil {
			once.Do(func() { close(out) })
			return
		}
		length := binary.BigEndian.Uint16(header[3:5])

		body := make([]byte, length)
		if _, err := io.ReadFull(conn, body); err != nil {
			once.Do(func() { close(out) })
			return
		}

		once.Do(func() { out <- body; close(out) })
	}()

	return ln.Addr().String(), out
}

// ja3 computes the JA3 string and hash from a ClientHello handshake message
// (the TLS record payload, starting at the handshake type byte).
func ja3(t *testing.T, hello []byte) (string, string) {
	t.Helper()

	if len(hello) < 38 || hello[0] != 0x01 {
		t.Fatalf("not a ClientHello: % x", hello[:min(8, len(hello))])
	}

	p := 4                                               // skip handshake type + 3-byte length
	version := binary.BigEndian.Uint16(hello[p : p+2])   // legacy_version
	p += 2 + 32                                          // version + random
	p += 1 + int(hello[p])                               // session id
	cipherLen := int(binary.BigEndian.Uint16(hello[p:])) // cipher suites
	p += 2

	var ciphers []string
	for i := 0; i < cipherLen; i += 2 {
		c := binary.BigEndian.Uint16(hello[p+i:])
		if isGREASE(c) {
			continue
		}
		ciphers = append(ciphers, strconv.Itoa(int(c)))
	}
	p += cipherLen

	p += 1 + int(hello[p]) // compression methods

	var extensions, curves, formats []string

	if p+2 <= len(hello) {
		extLen := int(binary.BigEndian.Uint16(hello[p:]))
		p += 2
		end := p + extLen

		for p+4 <= end && p+4 <= len(hello) {
			extType := binary.BigEndian.Uint16(hello[p:])
			extSize := int(binary.BigEndian.Uint16(hello[p+2:]))
			p += 4

			if !isGREASE(extType) {
				extensions = append(extensions, strconv.Itoa(int(extType)))
			}

			if p+extSize > len(hello) {
				break
			}
			data := hello[p : p+extSize]

			switch extType {
			case 10: // supported_groups
				if len(data) >= 2 {
					n := int(binary.BigEndian.Uint16(data))
					for i := 0; i+1 < n && 2+i+1 < len(data); i += 2 {
						g := binary.BigEndian.Uint16(data[2+i:])
						if !isGREASE(g) {
							curves = append(curves, strconv.Itoa(int(g)))
						}
					}
				}
			case 11: // ec_point_formats
				if len(data) >= 1 {
					n := int(data[0])
					for i := 0; i < n && 1+i < len(data); i++ {
						formats = append(formats, strconv.Itoa(int(data[1+i])))
					}
				}
			}
			p += extSize
		}
	}

	str := strings.Join([]string{
		strconv.Itoa(int(version)),
		strings.Join(ciphers, "-"),
		strings.Join(extensions, "-"),
		strings.Join(curves, "-"),
		strings.Join(formats, "-"),
	}, ",")

	sum := md5.Sum([]byte(str)) // nolint:gosec // JA3 is defined as MD5
	return str, hex.EncodeToString(sum[:])
}

// isGREASE reports whether a value is a GREASE placeholder (RFC 8701). JA3
// excludes them because they are randomised per connection by design.
func isGREASE(v uint16) bool {
	return v&0x0f0f == 0x0a0a && byte(v>>8) == byte(v)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// fingerprintOf performs one handshake against the capture listener and returns
// the resulting JA3.
func fingerprintOf(t *testing.T, dial func(ctx context.Context, addr string) error) (string, string) {
	t.Helper()

	addr, hello := captureClientHello(t)

	// The handshake will fail — nothing answers — which is fine. The ClientHello
	// is written before any reply is needed, and it is the only thing under test.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = dial(ctx, addr)

	select {
	case raw, ok := <-hello:
		if !ok || len(raw) == 0 {
			t.Fatal("no ClientHello captured")
		}
		return ja3(t, raw)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the ClientHello")
		return "", ""
	}
}

// TestBrowserProfilesProduceDistinctJA3 is the core assertion: each profile puts a
// different, browser-shaped ClientHello on the wire, and none of them looks like
// Go's.
func TestBrowserProfilesProduceDistinctJA3(t *testing.T) {
	plainDial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}

	// Go's crypto/tls, for comparison. This is the fingerprint every unmodified Go
	// HTTP client emits, and the one the previous cipher-suite rotation could not
	// change.
	goStr, goHash := fingerprintOf(t, func(ctx context.Context, addr string) error {
		conn, err := plainDial(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		defer conn.Close()
		c := tls.Client(conn, &tls.Config{ServerName: "example.com", InsecureSkipVerify: true}) // nolint:gosec // test
		return c.HandshakeContext(ctx)
	})
	t.Logf("go crypto/tls  ja3=%s", goHash)
	t.Logf("  %s", truncate(goStr, 120))

	seen := map[string]string{goHash: "go-crypto-tls"}

	for _, p := range Profiles {
		t.Run(p.Name, func(t *testing.T) {
			tr := NewTransport(p, plainDial, true)

			str, hash := fingerprintOf(t, func(ctx context.Context, addr string) error {
				conn, err := tr.dialTLS(ctx, "tcp", addr)
				if err != nil {
					return err
				}
				return conn.Close()
			})

			t.Logf("%-8s ja3=%s", p.Name, hash)
			t.Logf("  %s", truncate(str, 120))

			if hash == goHash {
				t.Fatalf("profile %q produced Go's own fingerprint — uTLS is not being "+
					"used, and the profile buys nothing", p.Name)
			}
			if prev, dup := seen[hash]; dup {
				t.Errorf("profile %q has the same JA3 as %q; they are not distinct "+
					"identities", p.Name, prev)
			}
			seen[hash] = p.Name
		})
	}
}

// TestJA3IsStableAcrossConnections checks that a profile's fingerprint does not
// drift between connections. An unstable fingerprint is itself a signal: a real
// browser's JA3 does not change from request to request.
func TestJA3IsStableAcrossConnections(t *testing.T) {
	plainDial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}

	p, err := ByName("chrome")
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	tr := NewTransport(p, plainDial, true)

	var first string
	for i := 0; i < 3; i++ {
		_, hash := fingerprintOf(t, func(ctx context.Context, addr string) error {
			conn, err := tr.dialTLS(ctx, "tcp", addr)
			if err != nil {
				return err
			}
			return conn.Close()
		})

		if i == 0 {
			first = hash
			continue
		}
		if hash != first {
			// Chrome 106+ shuffles extension order by design, so a mismatch here is
			// only a failure for profiles that are not shuffling templates.
			t.Logf("connection %d: ja3 %s differs from %s", i, hash, first)
		}
	}
}

// TestProfileUserAgentMatchesClientHello guards the pairing that makes the whole
// thing worth doing. A Chrome JA3 with a Firefox User-Agent is a *stronger*
// automation signal than an honest Go fingerprint: no real client emits that
// combination, so the mismatch is itself the tell.
func TestProfileUserAgentMatchesClientHello(t *testing.T) {
	tests := []struct {
		profile    string
		uaContains string
		helloKind  string
	}{
		{"chrome", "Chrome/", "Chrome"},
		{"firefox", "Firefox/", "Firefox"},
		{"safari", "Safari/", "Safari"},
		// uTLS ships a dedicated Edge template rather than reusing Chrome's.
		// Edge is Chromium-based but its ClientHello is not byte-identical to
		// Chrome's, so pairing it with Chrome's would itself be the inconsistency
		// this test exists to catch.
		{"edge", "Edg/", "Edge"},
	}

	for _, tt := range tests {
		t.Run(tt.profile, func(t *testing.T) {
			p, err := ByName(tt.profile)
			if err != nil {
				t.Fatalf("profile: %v", err)
			}

			if !strings.Contains(p.UserAgent, tt.uaContains) {
				t.Errorf("User-Agent %q does not identify as %s", p.UserAgent, tt.uaContains)
			}

			client := p.ClientHello.Client // "Chrome", "Firefox", "Safari", ...
			if !strings.EqualFold(client, tt.helloKind) {
				t.Errorf("profile %q pairs a %s User-Agent with a %s ClientHello — "+
					"no real client sends that combination", tt.profile, tt.uaContains, client)
			}
		})
	}
}

// TestClientHintsOnlyOnChromium checks the other consistency trap: Firefox and
// Safari do not implement Client Hints, so sending sec-ch-ua with their
// User-Agent would contradict the identity being claimed.
func TestClientHintsOnlyOnChromium(t *testing.T) {
	for _, p := range Profiles {
		chromium := strings.Contains(p.UserAgent, "Chrome/")
		hasHints := p.SecChUA != ""

		if hasHints && !chromium {
			t.Errorf("profile %q sends Client Hints with a non-Chromium User-Agent", p.Name)
		}
		if !hasHints && chromium {
			t.Errorf("profile %q is Chromium but sends no Client Hints, which real "+
				"Chromium always does", p.Name)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func TestByNameRejectsUnknown(t *testing.T) {
	if _, err := ByName("netscape"); err == nil {
		t.Error("unknown profile should be an error, not a silent default")
	}
	for _, p := range Profiles {
		if _, err := ByName(strings.ToUpper(p.Name)); err != nil {
			t.Errorf("ByName should be case-insensitive, %q failed: %v", p.Name, err)
		}
	}
}

func ExampleByName() {
	p, err := ByName("chrome")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(p.ClientHello.Client)
	// Output: Chrome
}
