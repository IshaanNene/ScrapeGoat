// Package safety enforces the network trust boundary for every outbound fetch.
//
// ScrapeGoat takes URLs from places the operator does not control: an MCP client
// driven by a model that just read a web page, a REST endpoint reachable from a
// browser, a link extracted from a crawled document. Any of those can name
// http://169.254.169.254/latest/meta-data/ or http://127.0.0.1:6379/ and, absent a
// guard, the crawler will fetch it and hand the result back to the caller.
//
// The guard has three layers, and all three are needed:
//
//  1. Scheme allowlist, applied to the URL string. Blocks file://, gopher://, and
//     the rest of what net/url will happily parse.
//  2. Address checks applied AFTER DNS resolution, in the dialer. Checking the
//     hostname is not enough — an attacker controls their own DNS and can point
//     evil.example.com at 127.0.0.1.
//  3. Dialing the specific IP that was validated, rather than re-resolving the
//     hostname. Without this, a DNS-rebinding attacker can answer the guard's
//     lookup with a public IP and the dialer's lookup with a private one.
//
// Redirects are re-checked per hop by the same dialer, because the transport
// dials again for each hop. CheckRedirect adds the scheme check on top, since a
// 302 to file:// never reaches the dialer at all.
package safety

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Errors returned by the guard. All are terminal: retrying a blocked address just
// blocks again, so callers should not mark them retryable.
var (
	// ErrSchemeNotAllowed is returned for any scheme outside the allowlist.
	ErrSchemeNotAllowed = errors.New("url scheme not allowed")

	// ErrNoHost is returned for URLs that parse but name no host, such as a bare
	// "garbage" string, which url.Parse accepts as a relative reference.
	ErrNoHost = errors.New("url has no host")

	// ErrBlockedAddress is returned when a hostname resolves to an address outside
	// the public internet.
	ErrBlockedAddress = errors.New("blocked non-public address")

	// ErrNoResolvedAddress is returned when resolution yields no usable address.
	ErrNoResolvedAddress = errors.New("hostname resolved to no usable address")
)

// Config configures a URLGuard.
type Config struct {
	// AllowedSchemes is the set of permitted URL schemes. Empty means http+https.
	AllowedSchemes []string

	// AllowPrivateAddresses disables the address checks entirely. Intended for
	// crawling an internal network on purpose; it makes the process an open proxy
	// to anything that can reach it, so it must be an explicit operator decision.
	AllowPrivateAddresses bool

	// AllowedPrivateHosts lists hosts exempt from the address checks even when
	// AllowPrivateAddresses is false. Use for a known internal target, e.g.
	// "staging.internal". Matched against the hostname, case-insensitively.
	AllowedPrivateHosts []string

	// Resolver is used for the pre-dial lookup. Nil means net.DefaultResolver.
	Resolver *net.Resolver

	// DialTimeout and KeepAlive are passed to the underlying dialer.
	DialTimeout time.Duration
	KeepAlive   time.Duration
}

// URLGuard validates URLs and produces a DialContext that refuses to connect to
// non-public addresses.
type URLGuard struct {
	schemes      map[string]bool
	allowPrivate bool
	exemptHosts  map[string]bool
	resolver     *net.Resolver
	dialer       *net.Dialer
}

// New builds a URLGuard from cfg.
func New(cfg Config) *URLGuard {
	schemes := make(map[string]bool, len(cfg.AllowedSchemes))
	for _, s := range cfg.AllowedSchemes {
		schemes[strings.ToLower(s)] = true
	}
	if len(schemes) == 0 {
		schemes["http"] = true
		schemes["https"] = true
	}

	exempt := make(map[string]bool, len(cfg.AllowedPrivateHosts))
	for _, h := range cfg.AllowedPrivateHosts {
		exempt[strings.ToLower(h)] = true
	}

	resolver := cfg.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}

	dialTimeout := cfg.DialTimeout
	if dialTimeout == 0 {
		dialTimeout = 30 * time.Second
	}
	keepAlive := cfg.KeepAlive
	if keepAlive == 0 {
		keepAlive = 30 * time.Second
	}

	return &URLGuard{
		schemes:      schemes,
		allowPrivate: cfg.AllowPrivateAddresses,
		exemptHosts:  exempt,
		resolver:     resolver,
		dialer:       &net.Dialer{Timeout: dialTimeout, KeepAlive: keepAlive},
	}
}

// Default returns a guard with the safe defaults: http and https only, no private
// addresses. This is what every entry point should use unless the operator has
// explicitly configured otherwise.
func Default() *URLGuard { return New(Config{}) }

// ValidateURL performs the checks that can be made from the URL string alone.
// It is cheap and should be called at every ingress point — MCP tool arguments,
// API request bodies, CLI seed URLs — so that bad input is rejected with a clear
// message before any connection is attempted.
//
// It deliberately does NOT resolve DNS: a validated URL is not a safe URL, only a
// well-formed one. The address checks happen in DialContext, where they cannot be
// raced by a rebinding attacker.
func (g *URLGuard) ValidateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	return g.ValidateParsedURL(u)
}

// ValidateParsedURL is ValidateURL for an already-parsed URL.
func (g *URLGuard) ValidateParsedURL(u *url.URL) error {
	if u == nil {
		return ErrNoHost
	}

	scheme := strings.ToLower(u.Scheme)
	if !g.schemes[scheme] {
		return fmt.Errorf("%w: %q", ErrSchemeNotAllowed, u.Scheme)
	}

	if u.Hostname() == "" {
		return fmt.Errorf("%w: %q", ErrNoHost, u.String())
	}

	return nil
}

// CheckRedirect returns a func suitable for http.Client.CheckRedirect. It applies
// the scheme check to each hop — a 302 to file:///etc/passwd is rejected here,
// since it would never reach the dialer — and enforces maxRedirects.
//
// The address checks for redirect targets happen in DialContext, which the
// transport calls again for each hop.
func (g *URLGuard) CheckRedirect(maxRedirects int) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if maxRedirects >= 0 && len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if err := g.ValidateParsedURL(req.URL); err != nil {
			return fmt.Errorf("redirect blocked: %w", err)
		}
		return nil
	}
}

// HTTPClient returns an http.Client that dials through the guard and re-checks the
// scheme on every redirect hop.
//
// Use this instead of a bare &http.Client{} anywhere a URL supplied by a caller can
// reach the network. A guarded fetcher does not help if a sibling component —
// sitemap discovery, a health check, a webhook — carries its own unguarded client;
// the attacker will simply use that one.
func (g *URLGuard) HTTPClient(timeout time.Duration, maxRedirects int) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         g.DialContext,
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: g.CheckRedirect(maxRedirects),
	}
}

// DialContext resolves the host, rejects the connection if any resolved address is
// outside the public internet, and then dials the validated address directly.
//
// Dialing the resolved IP rather than the hostname is what makes this resistant to
// DNS rebinding: there is exactly one lookup, and the address we checked is the
// address we connect to.
func (g *URLGuard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("split host:port %q: %w", addr, err)
	}

	if g.allowPrivate || g.exemptHosts[strings.ToLower(host)] {
		return g.dialer.DialContext(ctx, network, addr)
	}

	// A literal IP needs no lookup, but still needs checking.
	if ip := net.ParseIP(host); ip != nil {
		if reason := blockReason(ip); reason != "" {
			return nil, fmt.Errorf("%w %s (%s)", ErrBlockedAddress, ip, reason)
		}
		return g.dialer.DialContext(ctx, network, addr)
	}

	ips, err := g.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrNoResolvedAddress, host)
	}

	// Reject if ANY resolved address is non-public. A host that returns both a
	// public and a private address is either misconfigured or attacking us, and
	// picking the public one would leave the outcome up to resolver ordering.
	for _, ipa := range ips {
		if reason := blockReason(ipa.IP); reason != "" {
			return nil, fmt.Errorf("%w %s for host %q (%s)", ErrBlockedAddress, ipa.IP, host, reason)
		}
	}

	// Connect to the address we just validated, not to the name.
	var lastErr error
	for _, ipa := range ips {
		conn, err := g.dialer.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// blockReason returns a human-readable reason if ip is not a public unicast
// address, or "" if the address is fine to dial.
func blockReason(ip net.IP) string {
	switch {
	case ip == nil:
		return "unparseable address"
	case ip.IsUnspecified():
		// 0.0.0.0 and :: — on many stacks these route to localhost.
		return "unspecified"
	case ip.IsLoopback():
		return "loopback"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		// Covers 169.254.0.0/16, which is where every cloud metadata service lives.
		return "link-local"
	case ip.IsPrivate():
		// RFC 1918 and RFC 4193 unique-local.
		return "private"
	case ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return "multicast"
	case isCGNAT(ip):
		return "carrier-grade NAT"
	case isBenchmarking(ip):
		return "benchmarking range"
	case isIPv4Reserved(ip):
		return "reserved"
	case is6to4Relay(ip):
		return "6to4 relay anycast"
	}

	// An IPv4-mapped or NAT64-embedded IPv6 address must be judged on the IPv4
	// address inside it, or ::ffff:127.0.0.1 walks straight through.
	if v4 := ip.To4(); v4 != nil && !ip.Equal(v4) {
		return blockReason(v4)
	}
	if v4 := nat64Embedded(ip); v4 != nil {
		if reason := blockReason(v4); reason != "" {
			return "NAT64-embedded " + reason
		}
	}

	return ""
}

var (
	cgnatNet       = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
	benchmarkNet   = &net.IPNet{IP: net.IPv4(198, 18, 0, 0), Mask: net.CIDRMask(15, 32)}
	reserved240Net = &net.IPNet{IP: net.IPv4(240, 0, 0, 0), Mask: net.CIDRMask(4, 32)}
	sixToFourRelay = &net.IPNet{IP: net.IPv4(192, 88, 99, 0), Mask: net.CIDRMask(24, 32)}
	nat64WellKnown = &net.IPNet{IP: net.ParseIP("64:ff9b::"), Mask: net.CIDRMask(96, 128)}
	testNets       = []*net.IPNet{
		{IP: net.IPv4(192, 0, 2, 0), Mask: net.CIDRMask(24, 32)},    // TEST-NET-1
		{IP: net.IPv4(198, 51, 100, 0), Mask: net.CIDRMask(24, 32)}, // TEST-NET-2
		{IP: net.IPv4(203, 0, 113, 0), Mask: net.CIDRMask(24, 32)},  // TEST-NET-3
	}
	ipv4SpecialNets = []*net.IPNet{
		{IP: net.IPv4(192, 0, 0, 0), Mask: net.CIDRMask(24, 32)}, // IETF protocol assignments
	}
)

func isCGNAT(ip net.IP) bool { return cgnatNet.Contains(ip) }

func isBenchmarking(ip net.IP) bool { return benchmarkNet.Contains(ip) }

func isIPv4Reserved(ip net.IP) bool {
	if reserved240Net.Contains(ip) {
		return true
	}
	for _, n := range append(append([]*net.IPNet{}, testNets...), ipv4SpecialNets...) {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func is6to4Relay(ip net.IP) bool { return sixToFourRelay.Contains(ip) }

// nat64Embedded extracts the IPv4 address from a well-known-prefix NAT64 address,
// or returns nil.
func nat64Embedded(ip net.IP) net.IP {
	if !nat64WellKnown.Contains(ip) {
		return nil
	}
	b := ip.To16()
	if b == nil {
		return nil
	}
	return net.IPv4(b[12], b[13], b[14], b[15])
}
