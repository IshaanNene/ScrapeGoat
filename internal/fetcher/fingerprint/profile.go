// Package fingerprint makes ScrapeGoat's TLS handshake and HTTP headers look like
// the browser it claims to be.
//
// # Why the previous approach did nothing
//
// internal/fetcher/stealth.go rotated `tls.Config.CipherSuites` between a
// "Chrome-like" and a "Firefox-like" list. That cannot work, for two reasons:
//
//   - Go's crypto/tls ignores CipherSuites entirely for TLS 1.3, and reorders what
//     it does use according to its own preference logic. The bytes on the wire were
//     never the ones in that list.
//   - Even with the right cipher list, JA3 also covers the TLS version, the
//     extension list and its order, the supported groups, and the EC point formats.
//     crypto/tls emits a fixed, distinctive arrangement of all of those. Every Go
//     program has essentially the same JA3, and it looks like nothing a browser
//     sends.
//
// So the fingerprint was unchanged by the randomisation, and the type doing the
// randomising (TLSTransport) had no callers anyway.
//
// # What this does instead
//
// uTLS builds the ClientHello byte-for-byte from a recorded browser template, so
// the JA3 is the browser's. On top of that:
//
//   - The User-Agent is bound to the ClientHello. A Chrome JA3 arriving with a
//     Firefox User-Agent is a *stronger* automation signal than an honest Go
//     fingerprint, because no real client produces that combination. Profiles pair
//     the two, and the pairing is the whole point of the type.
//   - Header *values* match the browser: its exact Accept string, Sec-Fetch-*
//     set, and Client Hints. Header *order* is not controlled — see Profile.Apply
//     for why, and ROADMAP.md for the gap that leaves.
//
// # What this does not do
//
// This is not an anti-detection guarantee, and nothing here should be described as
// one. A determined operator has many other signals: TCP/IP stack characteristics,
// HTTP/2 SETTINGS frame values and frame ordering, timing, behavioural patterns,
// and the absence of a real JavaScript runtime. This closes the single loudest
// signal — "this is a Go program" — and nothing more.
package fingerprint

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"

	utls "github.com/refraction-networking/utls"
)

// Profile pairs a TLS ClientHello template with the HTTP identity that browser
// would present. The pairing is the point: the parts must be consistent or the
// combination is more identifying than either half.
type Profile struct {
	// Name identifies the profile in configuration and logs.
	Name string

	// UserAgent is the User-Agent this ClientHello belongs to.
	UserAgent string

	// ClientHello is the uTLS template used for the handshake.
	ClientHello utls.ClientHelloID

	// Headers are the browser's request headers.
	//
	// Their *order* on the wire is not controlled — see the note on Apply. The
	// values are what this buys: a Go client sending Chrome's exact Accept and
	// Sec-Fetch-* set is much less distinctive than one sending Go's defaults.
	Headers []Header

	// SecChUA is the Client Hints brand list, sent only by Chromium browsers.
	// Empty means the profile does not send Client Hints — Firefox and Safari
	// do not, and sending them would contradict the User-Agent.
	SecChUA string

	// Platform is the value for sec-ch-ua-platform.
	Platform string
}

// Header is an ordered header pair.
type Header struct {
	Key   string
	Value string
}

// Profiles are the supported browser identities.
//
// Kept deliberately short. Each entry has to be maintained against a real browser
// release, and a stale profile is worse than no profile: it claims to be Chrome
// 133 while sending Chrome 120's handshake, which is exactly the inconsistency a
// fingerprinter looks for.
var Profiles = []Profile{
	{
		Name:        "chrome",
		UserAgent:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		ClientHello: utls.HelloChrome_133,
		SecChUA:     `"Chromium";v="133", "Not(A:Brand";v="24", "Google Chrome";v="133"`,
		Platform:    `"Windows"`,
		Headers: []Header{
			{"sec-ch-ua-mobile", "?0"},
			{"upgrade-insecure-requests", "1"},
			{"accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"},
			{"sec-fetch-site", "none"},
			{"sec-fetch-mode", "navigate"},
			{"sec-fetch-user", "?1"},
			{"sec-fetch-dest", "document"},
			{"accept-encoding", "gzip, deflate, br"},
			{"accept-language", "en-US,en;q=0.9"},
		},
	},
	{
		Name:        "firefox",
		UserAgent:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0",
		ClientHello: utls.HelloFirefox_120,
		// No SecChUA: Firefox does not implement Client Hints. Sending them with a
		// Firefox User-Agent would be a contradiction a fingerprinter can see.
		Headers: []Header{
			{"accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"},
			{"accept-language", "en-US,en;q=0.5"},
			{"accept-encoding", "gzip, deflate, br"},
			{"upgrade-insecure-requests", "1"},
			{"sec-fetch-dest", "document"},
			{"sec-fetch-mode", "navigate"},
			{"sec-fetch-site", "none"},
			{"sec-fetch-user", "?1"},
		},
	},
	{
		Name:        "safari",
		UserAgent:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
		ClientHello: utls.HelloSafari_16_0,
		Headers: []Header{
			{"accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
			{"accept-language", "en-US,en;q=0.9"},
			{"accept-encoding", "gzip, deflate, br"},
		},
	},
	{
		Name:        "edge",
		UserAgent:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36 Edg/133.0.0.0",
		ClientHello: utls.HelloEdge_106,
		SecChUA:     `"Microsoft Edge";v="133", "Chromium";v="133", "Not(A:Brand";v="24"`,
		Platform:    `"Windows"`,
		Headers: []Header{
			{"sec-ch-ua-mobile", "?0"},
			{"upgrade-insecure-requests", "1"},
			{"accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"},
			{"sec-fetch-site", "none"},
			{"sec-fetch-mode", "navigate"},
			{"sec-fetch-user", "?1"},
			{"sec-fetch-dest", "document"},
			{"accept-encoding", "gzip, deflate, br"},
			{"accept-language", "en-US,en;q=0.9"},
		},
	},
}

// ByName returns the named profile.
func ByName(name string) (Profile, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range Profiles {
		if p.Name == name {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("unknown fingerprint profile %q (have %s)", name, Names())
}

// Names lists the available profile names.
func Names() string {
	names := make([]string, len(Profiles))
	for i, p := range Profiles {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}

// Random returns a randomly chosen profile.
//
// Rotating between profiles spreads a crawl across several plausible identities.
// It does not make any single request less identifiable — each request is still
// exactly one browser — and a site correlating by IP will see one address changing
// browser between requests, which is its own signal. Rotate per-session, not
// per-request, unless proxies are rotating too.
func Random() Profile {
	return Profiles[rand.IntN(len(Profiles))]
}

// Apply sets the profile's headers on a request, without disturbing any the
// caller set deliberately.
//
// Header *order* is not controlled, and this deliberately does not pretend
// otherwise. net/http writes HTTP/1.1 headers by ranging a map, and x/net/http2
// sorts them; reproducing a browser's exact order needs a custom header writer or
// a forked transport. Order is a real fingerprinting signal, so this is a genuine
// gap — recorded in ROADMAP.md rather than papered over with a field that looks
// like it does something.
func (p Profile) Apply(req *http.Request) {
	if req.Header == nil {
		req.Header = make(http.Header)
	}

	set := func(k, v string) {
		if v == "" {
			return
		}
		if _, exists := req.Header[http.CanonicalHeaderKey(k)]; exists {
			return // caller set it on purpose
		}
		req.Header.Set(k, v)
	}

	set("User-Agent", p.UserAgent)
	set("sec-ch-ua", p.SecChUA)
	set("sec-ch-ua-platform", p.Platform)

	for _, h := range p.Headers {
		set(h.Key, h.Value)
	}
}
