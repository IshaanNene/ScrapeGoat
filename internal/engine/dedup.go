package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// Deduplicator tracks visited URLs to avoid re-crawling.
type Deduplicator struct {
	mu   sync.RWMutex
	seen map[string]struct{}
}

// NewDeduplicator creates a new Deduplicator with the given estimated capacity.
func NewDeduplicator(estimatedCapacity int) *Deduplicator {
	return &Deduplicator{
		seen: make(map[string]struct{}, estimatedCapacity),
	}
}

// IsSeen returns true if the URL (after canonicalization) has been seen before.
func (d *Deduplicator) IsSeen(rawURL string) bool {
	canonical := CanonicalizeURL(rawURL)
	hash := hashURL(canonical)

	d.mu.RLock()
	defer d.mu.RUnlock()
	_, ok := d.seen[hash]
	return ok
}

// MarkSeen marks a URL as seen.
func (d *Deduplicator) MarkSeen(rawURL string) {
	canonical := CanonicalizeURL(rawURL)
	hash := hashURL(canonical)

	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen[hash] = struct{}{}
}

// MarkIfUnseen atomically marks a URL as seen and reports whether it was new.
//
// This is the method the crawl path must use. Calling IsSeen followed by MarkSeen
// is a check-then-act race: two workers extracting the same link from two different
// pages can both observe "unseen" between the read lock and the write lock, and both
// enqueue it. The result is duplicate fetches, duplicate items, and a request budget
// spent twice on the same page.
func (d *Deduplicator) MarkIfUnseen(rawURL string) bool {
	canonical := CanonicalizeURL(rawURL)
	hash := hashURL(canonical)

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.seen[hash]; ok {
		return false
	}
	d.seen[hash] = struct{}{}
	return true
}

// Count returns the number of unique URLs seen.
func (d *Deduplicator) Count() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.seen)
}

// Reset clears all seen URLs.
func (d *Deduplicator) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = make(map[string]struct{})
}

// Export returns all seen URL hashes (for checkpoint serialization).
func (d *Deduplicator) Export() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	hashes := make([]string, 0, len(d.seen))
	for h := range d.seen {
		hashes = append(hashes, h)
	}
	return hashes
}

// Import loads URL hashes (for checkpoint restore).
func (d *Deduplicator) Import(hashes []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, h := range hashes {
		d.seen[h] = struct{}{}
	}
}

// CanonicalizeURL normalizes a URL for deduplication:
// - lowercases scheme and host
// - removes fragment
// - sorts query parameters
// - removes trailing slash (except root)
// - removes default ports (80 for http, 443 for https)
func CanonicalizeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	// Lowercase scheme and host
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)

	// Remove fragment
	u.Fragment = ""

	// Remove default ports
	host := u.Hostname()
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		u.Host = host
	}

	// Sort query parameters
	if u.RawQuery != "" {
		params := u.Query()
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var sorted []string
		for _, k := range keys {
			vals := params[k]
			sort.Strings(vals)
			for _, v := range vals {
				sorted = append(sorted, url.QueryEscape(k)+"="+url.QueryEscape(v))
			}
		}
		u.RawQuery = strings.Join(sorted, "&")
	}

	// Remove trailing slash (except root "/")
	if u.Path != "/" && strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimRight(u.Path, "/")
	}

	// Ensure path is at least "/"
	if u.Path == "" {
		u.Path = "/"
	}

	return u.String()
}

// hashURL creates a compact hash of a URL string.
func hashURL(canonicalURL string) string {
	h := sha256.Sum256([]byte(canonicalURL))
	return hex.EncodeToString(h[:16]) // 128-bit hash
}
