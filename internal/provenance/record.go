package provenance

import (
	"time"
)

// SchemaVersion is the version of the Record shape.
//
// Versioned because a corpus outlives the code that produced it. Someone reading
// a Parquet file in two years needs to know which fields to expect, and "look at
// the git history" is not an answer when the file has been copied somewhere the
// repository has not.
//
// Bump on any change that removes a field or alters the meaning of one. Adding an
// optional field does not require a bump: a reader that ignores it is still
// correct.
const SchemaVersion = 1

// Record is one document in a corpus, with everything needed to say where it came
// from and what the source asked for.
//
// Field order is the order a human reads it in: what it is, where it came from,
// what it says, whether you were allowed to take it.
type Record struct {
	SchemaVersion int `json:"schema_version"`

	// --- identity ---

	// URL is the address fetched. CanonicalURL is the page's own claim about its
	// address, when it made one; the two differ constantly on sites with tracking
	// parameters or pagination, and deduplicating on URL alone over-counts badly.
	URL          string `json:"url"`
	CanonicalURL string `json:"canonical_url,omitempty"`

	// ContentHash addresses the raw response body in the fetch log. It is the join
	// key between a corpus record and the bytes it was derived from, which is what
	// makes the derivation checkable rather than merely asserted.
	ContentHash string `json:"content_hash"`

	// --- fetch ---

	FetchedAt  time.Time `json:"fetched_at"`
	StatusCode int       `json:"status_code"`
	MIMEType   string    `json:"mime_type,omitempty"`
	FinalURL   string    `json:"final_url,omitempty"`

	// CrawlerIdentity is the User-Agent actually sent. Part of provenance because
	// robots.txt is answered per agent: a page permitted to one identity may be
	// refused to another, so the decision recorded below is only meaningful
	// alongside the identity that obtained it.
	CrawlerIdentity string `json:"crawler_identity,omitempty"`

	// ETag and LastModified are the cache validators the server supplied, stored
	// verbatim.
	//
	// Verbatim because they are echoed back, not interpreted: If-None-Match sends
	// the ETag exactly as received, weak-comparison marker and all, and
	// If-Modified-Since sends the Last-Modified string the server itself chose to
	// format. Parsing and re-rendering an HTTP-date is a way to send a server a
	// date it did not say.
	//
	// Recorded whether or not this crawl uses them. A corpus that knows which of
	// its pages carry validators can say what a refresh would cost before anyone
	// runs one, and pages that carry none are the ones that will always cost a
	// full fetch.
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`

	// --- content ---

	Text     string `json:"text,omitempty"`
	Title    string `json:"title,omitempty"`
	Language string `json:"language,omitempty"`

	// ExtractionConfidence is the extractor's own 0..1 ranking signal, carried
	// through so a downstream filter can drop low-confidence text without
	// re-running extraction. Not a probability — see internal/extract.
	ExtractionConfidence float64 `json:"extraction_confidence,omitempty"`

	// --- permission, as stated by the source at fetch time ---

	// RobotsAllowed is what robots.txt said about *this* crawler fetching this URL,
	// at the moment it was fetched. Recorded rather than recomputed: robots.txt
	// changes, and a corpus re-checked next year would be answering a different
	// question from the one that actually governed the crawl.
	RobotsAllowed bool `json:"robots_allowed"`

	// AIDirectives summarises what the site's robots.txt said to AI crawlers,
	// whether or not any of it applied to this one. A site that blocks GPTBot and
	// CCBot but not us has still expressed an intent, and a corpus that discards
	// that has discarded the most relevant fact about its own defensibility.
	AIDirectives *AIDirectiveSummary `json:"ai_directives,omitempty"`

	// Signals is what the page itself said: noai, TDM reservation, licence.
	Signals PageSignals `json:"signals"`

	// --- run identity ---

	// CrawlID ties the record to the fetch log that produced it, so a corpus
	// assembled from several crawls can still be traced back per record.
	CrawlID string `json:"crawl_id,omitempty"`
}

// AIDirectiveSummary is the AI-relevant part of a site's robots.txt, flattened
// for storage next to each record.
//
// Denormalised on purpose. It is per-site data repeated per record, which is
// wasteful in a database and correct in a corpus: the file has to stay meaningful
// when a single row is extracted and shown to someone asking where it came from.
type AIDirectiveSummary struct {
	// RobotsPresent distinguishes a site with no robots.txt from one whose
	// robots.txt imposes nothing. Both permit the crawl; only one is a statement.
	RobotsPresent bool `json:"robots_present"`

	AgentsAddressed []string `json:"agents_addressed,omitempty"`
	AgentsBlocked   []string `json:"agents_blocked,omitempty"`
	VendorsBlocked  []string `json:"vendors_blocked,omitempty"`
}

// SummariseDirectives flattens a RobotsReport for storage on a record.
func SummariseDirectives(r RobotsReport) *AIDirectiveSummary {
	return &AIDirectiveSummary{
		RobotsPresent:   r.Present,
		AgentsAddressed: r.AIAgentsAddressed,
		AgentsBlocked:   r.AIAgentsBlocked,
		VendorsBlocked:  r.AIVendors(r.AIAgentsBlocked),
	}
}

// Restrictive reports whether anything about this record's source asked for it to
// be left out of AI training.
//
// Covers both the page's own signals and a site-wide robots.txt that turned AI
// crawlers away — a site can block every AI agent it has heard of in robots.txt
// while its pages carry no meta tags at all, and reading only the page would miss
// the clearest statement it made.
func (r Record) Restrictive() bool {
	if r.Signals.Restrictive() {
		return true
	}
	return r.AIDirectives != nil && len(r.AIDirectives.AgentsBlocked) > 0
}

// Complete reports whether the record carries enough provenance to be defensible:
// an address, the bytes it came from, and when.
//
// A record failing this is not necessarily wrong, but it cannot answer the
// question the corpus exists to answer, and shipping it silently alongside ones
// that can is how a dataset's guarantees get quietly averaged down.
func (r Record) Complete() bool {
	return r.URL != "" && r.ContentHash != "" && !r.FetchedAt.IsZero()
}
