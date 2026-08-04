package engine

import (
	"strings"

	"golang.org/x/net/publicsuffix"
)

// domainMatches reports whether host falls under the rule.
//
// The match is by registrable-domain suffix, not string equality. Exact matching
// meant `allowed_domains: [example.com]` silently rejected www.example.com and
// every other subdomain — which is almost never what someone configuring a crawl
// means, and fails in a way that looks like the crawler simply finding nothing.
//
// The public-suffix check is what stops a rule from spanning a registrable
// boundary. Without it, a rule of "co.uk" would match every British site, and a
// rule of "com" would match the entire internet. A rule that *is* a public suffix
// is therefore treated as exact-match only.
func domainMatches(rule, host string) bool {
	rule = normaliseHost(rule)
	host = normaliseHost(host)

	if rule == "" || host == "" {
		return false
	}
	if rule == host {
		return true
	}

	// A rule that is itself a public suffix ("com", "co.uk", "github.io") must not
	// swallow everything registered beneath it.
	if isPublicSuffix(rule) {
		return false
	}

	// Suffix match on a label boundary, so "notexample.com" does not match a rule
	// of "example.com".
	return strings.HasSuffix(host, "."+rule)
}

// normaliseHost lowercases, strips a trailing dot (the FQDN root), and drops any
// port that came along with the host.
func normaliseHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ".")
	if i := strings.LastIndex(h, ":"); i != -1 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return h
}

// isPublicSuffix reports whether s is a public suffix in its own right — that is,
// a name under which anyone may register, rather than a registered domain.
func isPublicSuffix(s string) bool {
	suffix, _ := publicsuffix.PublicSuffix(s)
	return suffix == s
}

// isDomainAllowed checks a host against the allow and deny lists.
//
// The allowlist wins when set: a host must match one of its rules. Otherwise the
// denylist applies. Both match by registrable-domain suffix.
func (e *Engine) isDomainAllowed(domain string) bool {
	if len(e.cfg.Engine.AllowedDomains) > 0 {
		for _, rule := range e.cfg.Engine.AllowedDomains {
			if domainMatches(rule, domain) {
				return true
			}
		}
		return false
	}

	for _, rule := range e.cfg.Engine.DisallowedDomains {
		if domainMatches(rule, domain) {
			return false
		}
	}
	return true
}
