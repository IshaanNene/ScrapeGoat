package fingerprint

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// DialFunc is the plain TCP dialer the handshake runs on top of.
//
// This is always the SSRF guard's dialer in practice. Fingerprinting sits *above*
// the address checks, never around them: the connection must already have been
// judged safe before a single TLS byte is written, or a nicer-looking handshake
// would just be a nicer-looking request to the cloud metadata endpoint.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Transport is an http.RoundTripper that performs uTLS handshakes and writes
// headers in the profile's order.
type Transport struct {
	profile   Profile
	dial      DialFunc
	tlsConfig *utls.Config

	h1 *http.Transport
	h2 *http2.Transport

	// alpn caches the negotiated protocol per host, so the probe below happens
	// once per host rather than once per request.
	alpnMu sync.RWMutex
	alpn   map[string]string
}

// NewTransport builds a fingerprinting transport.
//
// insecureSkipVerify is threaded through rather than hardcoded because the plain
// fetcher already exposes it as an operator setting; defaulting it differently
// here would be a surprise.
func NewTransport(p Profile, dial DialFunc, insecureSkipVerify bool) *Transport {
	t := &Transport{
		profile: p,
		dial:    dial,
		tlsConfig: &utls.Config{
			InsecureSkipVerify: insecureSkipVerify, // nolint:gosec // operator-configured
		},
	}

	// HTTP/1.1 path.
	t.h1 = &http.Transport{
		DialTLSContext:      t.dialTLS,
		DialContext:         dial,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		DisableCompression:  true, // the fetcher handles decompression, including brotli
	}

	// HTTP/2 path. Go's http.Transport cannot upgrade a connection returned from
	// DialTLSContext by itself: it inspects the conn for a crypto/tls
	// ConnectionState to read the negotiated ALPN protocol, and a *utls.UConn does
	// not satisfy that check. So the two protocols are dispatched here instead,
	// on the ALPN result from the handshake we performed.
	t.h2 = &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return t.dialTLS(ctx, network, addr)
		},
		DisableCompression: true,
		AllowHTTP:          false,
	}

	return t
}

// dialTLS opens a TCP connection through the guarded dialer and completes a uTLS
// handshake using the profile's ClientHello.
func (t *Transport) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	raw, err := t.dial(ctx, network, addr)
	if err != nil {
		return nil, err
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("split host:port %q: %w", addr, err)
	}

	cfg := t.tlsConfig.Clone()
	cfg.ServerName = host

	uconn := utls.UClient(raw, cfg, t.profile.ClientHello)
	if err := uconn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("utls handshake with %s: %w", addr, err)
	}

	return uconn, nil
}

// negotiatedProtocol reports the ALPN protocol on a completed uTLS connection.
func negotiatedProtocol(c net.Conn) string {
	if u, ok := c.(*utls.UConn); ok {
		return u.ConnectionState().NegotiatedProtocol
	}
	return ""
}

// RoundTrip applies the profile and dispatches on the negotiated protocol.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.profile.Apply(req)

	if req.URL.Scheme != "https" {
		return t.h1.RoundTrip(req)
	}

	// Dispatch on the negotiated protocol.
	//
	// This is a probe rather than something cleaner because net/http's own h2
	// upgrade path ends in `pconn.conn.(*tls.Conn)` — a concrete type assertion
	// that a *utls.UConn cannot satisfy. Wrapping the UConn to expose a
	// crypto/tls.ConnectionState gets past the ALPN lookup and then panics on that
	// assertion, so the two transports are dispatched here instead.
	//
	// Cost is one extra handshake per host for the whole crawl, cached below.
	proto, err := t.probeALPN(req)
	if err != nil {
		return nil, err
	}
	if proto == http2.NextProtoTLS {
		return t.h2.RoundTrip(req)
	}
	return t.h1.RoundTrip(req)
}

// alpnCache remembers the negotiated protocol per host so the probe happens once.
func (t *Transport) probeALPN(req *http.Request) (string, error) {
	host := req.URL.Host
	if req.URL.Port() == "" {
		host = net.JoinHostPort(req.URL.Hostname(), "443")
	}

	t.alpnMu.RLock()
	proto, known := t.alpn[host]
	t.alpnMu.RUnlock()
	if known {
		return proto, nil
	}

	conn, err := t.dialTLS(req.Context(), "tcp", host)
	if err != nil {
		return "", err
	}
	proto = negotiatedProtocol(conn)
	_ = conn.Close()

	t.alpnMu.Lock()
	if t.alpn == nil {
		t.alpn = make(map[string]string)
	}
	t.alpn[host] = proto
	t.alpnMu.Unlock()

	return proto, nil
}

// CloseIdleConnections releases pooled connections on both transports.
func (t *Transport) CloseIdleConnections() {
	t.h1.CloseIdleConnections()
	t.h2.CloseIdleConnections()
}

// Profile returns the transport's profile.
func (t *Transport) Profile() Profile { return t.profile }
