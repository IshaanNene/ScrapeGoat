package provenance

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// PriorCorpus is what an earlier crawl recorded, indexed by URL.
//
// It exists to answer one question per page — "do we already have this, and what
// did the server say would prove it unchanged?" — so that a recrawl can ask the
// server to confirm rather than resend. Over half of AI-crawler traffic is
// reported to be re-fetching pages that have not changed; a conditional request
// turns each of those from a full body into a 304.
type PriorCorpus struct {
	mu    sync.RWMutex
	byURL map[string]Record
}

// NewPriorCorpus indexes records by URL.
//
// Later records win on a repeated URL. A corpus is append-only and may hold
// several crawls, so the last one is the most recent thing known about the page —
// and validators from an older fetch would ask the server about a version we no
// longer hold.
func NewPriorCorpus(records []Record) *PriorCorpus {
	p := &PriorCorpus{byURL: make(map[string]Record, len(records))}
	for _, r := range records {
		if r.URL == "" {
			continue
		}
		p.byURL[r.URL] = r
	}
	return p
}

// LoadPriorCorpus reads a corpus file and indexes it.
func LoadPriorCorpus(path string) (*PriorCorpus, error) {
	records, err := ReadAnyCorpus(path)
	if err != nil {
		return nil, fmt.Errorf("provenance: load prior corpus %s: %w", path, err)
	}
	return NewPriorCorpus(records), nil
}

// Lookup returns what the earlier crawl recorded for a URL.
func (p *PriorCorpus) Lookup(rawURL string) (Record, bool) {
	if p == nil {
		return Record{}, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.byURL[rawURL]
	return r, ok
}

// Len reports how many URLs are indexed.
func (p *PriorCorpus) Len() int {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.byURL)
}

// Validated counts the indexed pages that carry a validator, which is the number
// a refresh could confirm without downloading. The rest will always cost a full
// fetch, and saying so before a run beats discovering it during one.
func (p *PriorCorpus) Validated() int {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	n := 0
	for _, r := range p.byURL {
		if r.ETag != "" || r.LastModified != "" {
			n++
		}
	}
	return n
}

// ConditionalHeaders sets If-None-Match and If-Modified-Since on h for a URL the
// prior corpus knows, reporting whether it set anything.
//
// Both are sent when both are known. RFC 9110 has the server prefer the ETag and
// treat the date as a fallback, so sending both costs a few bytes and covers a
// server that issues one inconsistently.
//
// The values go out exactly as they came back. An ETag is an opaque string — the
// weak-comparison marker is part of it — and a Last-Modified is echoed in the
// server's own formatting rather than reparsed, because a re-rendered HTTP-date
// is a date the server never sent.
func (p *PriorCorpus) ConditionalHeaders(rawURL string, h http.Header) bool {
	rec, ok := p.Lookup(rawURL)
	if !ok {
		return false
	}

	set := false
	if rec.ETag != "" {
		h.Set("If-None-Match", rec.ETag)
		set = true
	}
	if rec.LastModified != "" {
		h.Set("If-Modified-Since", rec.LastModified)
		set = true
	}
	return set
}

// Refreshed returns rec as confirmed still current at fetchedAt.
//
// Everything derived stays: the content hash, the text, the policy state. A 304
// says the bytes did not change, so re-deriving would spend work to produce what
// is already held — and could not produce it anyway, since a 304 carries no body.
// Only the moment of confirmation moves, which is the fact the response actually
// established.
func Refreshed(rec Record, fetchedAt time.Time, crawlID string) Record {
	rec.FetchedAt = fetchedAt
	if crawlID != "" {
		rec.CrawlID = crawlID
	}
	return rec
}

// URLs returns every indexed URL, sorted.
//
// Sorted so that a refresh queues pages in the same order every run: the map is
// keyed for lookup, and Go randomises its iteration. A crawl whose frontier order
// depends on map internals is one whose output ordering cannot be reproduced.
func (p *PriorCorpus) URLs() []string {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make([]string, 0, len(p.byURL))
	for u := range p.byURL {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}
