package safety

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GuardedProxy is a loopback HTTP proxy whose every outbound connection is made by
// URLGuard.DialContext.
//
// It exists because the guard is a Go-side control and the browser is not a Go-side
// client. Chromium performs its own DNS and opens its own sockets, so a URL that
// passed ValidateURL is still fetched by a process the guard has no hook into: point
// a headless fetch at http://169.254.169.254/latest/meta-data/ and the scheme check
// passes, Chromium resolves and connects on its own, and cloud credentials come back
// as page content. Nothing in Go was ever asked.
//
// Routing the browser through a proxy puts Go back in the connection path. The
// browser hands us a hostname, we resolve it once, check every resolved address, and
// connect to the address we checked — so a blocked target is refused at the only
// layer that can actually refuse it, rather than being asked about at a layer that
// cannot.
type GuardedProxy struct {
	guard     *URLGuard
	ln        net.Listener
	srv       *http.Server
	transport *http.Transport

	closeOnce sync.Once
	closeErr  error
}

// StartGuardedProxy binds a proxy on the loopback interface and serves it until
// Close. It listens on a kernel-assigned port so that concurrent browser fetchers,
// and concurrent test runs, never contend for a fixed one.
func StartGuardedProxy(g *URLGuard) (*GuardedProxy, error) {
	if g == nil {
		return nil, fmt.Errorf("safety: guarded proxy requires a URLGuard")
	}

	// Loopback only. The proxy will connect to anything the guard permits, so
	// binding it to a routable interface would turn this process into an open
	// proxy for the whole network.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for guarded proxy: %w", err)
	}

	p := &GuardedProxy{
		guard: g,
		ln:    ln,
		transport: &http.Transport{
			DialContext:         g.DialContext,
			ForceAttemptHTTP2:   false, // proxied plaintext hop; h2c buys nothing here
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	p.srv = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 30 * time.Second,
	}

	go func() {
		// ErrServerClosed is the expected outcome of Close; anything else has
		// already failed the request that provoked it.
		_ = p.srv.Serve(ln)
	}()

	return p, nil
}

// Addr returns the host:port the browser should be pointed at.
func (p *GuardedProxy) Addr() string { return p.ln.Addr().String() }

// Close stops serving and releases the listener.
func (p *GuardedProxy) Close() error {
	p.closeOnce.Do(func() {
		p.transport.CloseIdleConnections()
		p.closeErr = p.srv.Close()
	})
	return p.closeErr
}

func (p *GuardedProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleForward(w, r)
}

// handleConnect services the tunnel the browser opens for every https page. The
// guard check happens on the dial, before the tunnel exists — once bytes are
// flowing through a CONNECT tunnel they are opaque TLS and there is nothing left
// to inspect, so the address is the only thing that can be judged and this is the
// only moment it can be judged in.
func (p *GuardedProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	addr := r.Host
	if addr == "" {
		addr = r.URL.Host
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, "443")
	}

	upstream, err := p.guard.DialContext(r.Context(), "tcp", addr)
	if err != nil {
		http.Error(w, fmt.Sprintf("guarded proxy refused CONNECT %s: %v", addr, err), http.StatusForbidden)
		return
	}
	defer upstream.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "guarded proxy: connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		http.Error(w, fmt.Sprintf("guarded proxy: hijack: %v", err), http.StatusInternalServerError)
		return
	}
	defer client.Close()

	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	// Wait for whichever direction finishes first. The deferred Closes above then
	// tear down the other one, so neither copy goroutine outlives the handler —
	// a half-closed tunnel would otherwise leak a goroutine per blocked fetch.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}

// hopByHopHeaders must not be forwarded: they describe the single connection they
// arrived on, not the request. RFC 9110 §7.6.1.
var hopByHopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// handleForward services a plain-http page, which the browser sends in absolute
// form rather than as a tunnel.
func (p *GuardedProxy) handleForward(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() {
		http.Error(w, "guarded proxy: expected an absolute-form request URI", http.StatusBadRequest)
		return
	}
	// Cheap scheme/shape rejection first, so an obviously bad target never reaches
	// the resolver. The address checks still happen in DialContext.
	if err := p.guard.ValidateParsedURL(r.URL); err != nil {
		http.Error(w, fmt.Sprintf("guarded proxy refused %s: %v", r.URL.Redacted(), err), http.StatusForbidden)
		return
	}

	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	stripHopByHop(outReq.Header)

	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("guarded proxy: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	stripHopByHop(resp.Header)
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func stripHopByHop(h http.Header) {
	// Connection names further headers that are themselves hop-by-hop, so read it
	// before deleting it.
	for _, name := range strings.Split(h.Get("Connection"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			h.Del(name)
		}
	}
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
}
