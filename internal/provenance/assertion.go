package provenance

import (
	"bytes"
	"time"
	"unicode"
)

// Observation is an immutable, content-addressed fetch: some bytes, and everything
// known about how they were obtained.
//
// Hash is the primary key. Every derived value points back to an observation by
// hash rather than by URL, because a URL is not an identifier — it is a request,
// and the same one returns different bytes on Tuesday. Addressing the bytes is what
// lets a claim made today be checked against the page as it actually was.
//
// This is deliberately the fetch half of Record, named as its own thing. Record
// currently carries the fetch facts and the extracted text in one flat struct, so
// there is no way to say "these four values came from this one page" — the page is
// re-described alongside each one. Observation is the shared referent that makes
// that sayable.
type Observation struct {
	// Hash is the SHA-256 of the raw response body, hex-encoded — the same digest
	// the fetch log stores, so a corpus row and a recorded fetch join on it.
	Hash string `json:"hash"`

	// URL is the address fetched; FinalURL is where it ended after redirects;
	// CanonicalURL is the page's own claim about its address, when it made one.
	// All three, because they disagree constantly and each answers a different
	// question: what was asked for, what answered, and what the page says it is.
	URL          string `json:"url"`
	FinalURL     string `json:"final_url,omitempty"`
	CanonicalURL string `json:"canonical_url,omitempty"`

	FetchedAt  time.Time `json:"fetched_at"`
	StatusCode int       `json:"status_code"`
	MIMEType   string    `json:"mime_type,omitempty"`

	// Substrate is what did the fetching: "http", "browser", "replay". A browser
	// fetch and an HTTP fetch of the same URL can return materially different
	// bytes, and a replayed fetch returns bytes that were obtained at some other
	// time entirely. A consumer comparing two observations needs to know which.
	Substrate string `json:"substrate,omitempty"`

	// CrawlerIdentity is the User-Agent actually sent. Part of the identity of the
	// observation because robots.txt is answered per agent: the same URL permitted
	// to one identity may be refused to another, so the policy recorded below is
	// only meaningful alongside the identity that obtained it.
	CrawlerIdentity string `json:"crawler_identity,omitempty"`

	// Policy is what the source asked for at the moment of the fetch, not what it
	// asks for now. Recorded rather than recomputed: robots.txt changes, and a
	// corpus re-checked next year answers a different question from the one that
	// governed the crawl.
	Policy PolicyState `json:"policy"`

	// CrawlID ties the observation to the fetch log that produced it.
	CrawlID string `json:"crawl_id,omitempty"`
}

// PolicyState is the permission position at fetch time, gathered from robots.txt
// and from the page itself.
//
// A struct rather than three fields on Observation so that "what were we allowed to
// do" is one thing a reader can look at, and so that adding a signal — Content-Usage
// from the AIPREF work, for instance — is a change in one place.
type PolicyState struct {
	// RobotsAllowed is what robots.txt said about this crawler fetching this URL.
	RobotsAllowed bool `json:"robots_allowed"`

	// AIDirectives is what the site said to AI crawlers generally, whether or not
	// any of it applied to this one. A site that blocks GPTBot and CCBot but not us
	// has still expressed an intent, and discarding it discards the most relevant
	// fact about the corpus's defensibility.
	AIDirectives *AIDirectiveSummary `json:"ai_directives,omitempty"`

	// Signals is what the page itself said: noai, TDM reservation, licence.
	Signals PageSignals `json:"signals"`
}

// Restrictive reports whether anything about the source asked to be left out of AI
// training. Mirrors Record.Restrictive so the two shapes cannot drift on the one
// question the corpus exists to answer.
func (p PolicyState) Restrictive() bool {
	if p.Signals.Restrictive() {
		return true
	}
	return p.AIDirectives != nil && len(p.AIDirectives.AgentsBlocked) > 0
}

// EvidenceSpan points at the bytes that support a value.
//
// This is the field the whole model rests on. A corpus that says "the price is
// £51.25" is worth what its producer's reputation is worth. A corpus that says "the
// price is £51.25, and here are bytes 8,412–8,419 of the document whose SHA-256 is
// abc…, which read £51.25" can be checked by anyone holding the bytes, years later,
// without the extractor, the model, or the network.
type EvidenceSpan struct {
	// ObservationHash identifies which bytes. Required: a span without it is a pair
	// of integers into an unspecified document.
	ObservationHash string `json:"observation_hash"`

	// ByteStart and ByteEnd are a half-open range into the raw body, [start, end).
	// Byte offsets rather than character offsets because the raw body is what is
	// stored and hashed, and its encoding is not always known.
	ByteStart int `json:"byte_start"`
	ByteEnd   int `json:"byte_end"`
}

// Empty reports whether the span points at nothing.
func (e EvidenceSpan) Empty() bool {
	return e.ObservationHash == "" || e.ByteEnd <= e.ByteStart
}

// Len is the span's length in bytes.
func (e EvidenceSpan) Len() int {
	if e.Empty() {
		return 0
	}
	return e.ByteEnd - e.ByteStart
}

// Assertion is one derived claim about an observation, with the evidence for it.
//
// The unit is the claim, not the page. A page yields many assertions, each of which
// may have been produced by a different method with a different reliability — a
// title read from <title>, a price read from a CSS selector, a summary produced by a
// model. Flattening those into one row of fields, as Item does, loses the only thing
// a consumer needs in order to decide which of them to trust.
type Assertion struct {
	SchemaVersion int `json:"schema_version"`

	// SourceURL is the page this claim was derived from.
	//
	// Denormalised from the observation on purpose, for the same reason
	// AIDirectiveSummary is: a row lifted out of the file and shown to someone
	// asking where a value came from has to answer without a join. It is also not
	// redundant. Content addressing means two URLs serving identical bytes share
	// one observation — a site's "/" and "/index.html", say — and the hash alone
	// then cannot say which request produced the claim.
	SourceURL string `json:"source_url"`

	// Field is the name of the claim, Value its content.
	Field string `json:"field"`
	Value any    `json:"value"`

	// Index is the position of this value among the values the same derivation
	// produced for the same field — the third link a selector matched is index 2.
	// Corpus rows carry no inherent order, so without it a multi-valued field
	// cannot be reassembled in the order the page presented it.
	Index int `json:"index,omitempty"`

	// Evidence points at the bytes supporting Value.
	Evidence EvidenceSpan `json:"evidence"`

	// Method identifies what produced this, specifically enough to reproduce it:
	// "css:.price_color", "jsonld:offers.price", "density:main", "model:<name>".
	// MethodVersion is the version of that method, so a corpus built across a
	// change to the extractor can still say which values came from which.
	Method        string `json:"method"`
	MethodVersion string `json:"method_version,omitempty"`

	// Confidence is 0..1 and is only meaningful when Validated. Validate zeroes it
	// on failure rather than leaving the deriver's claim in place: an unverified
	// confidence is exactly the number a downstream filter should not be able to
	// read past by forgetting to check a flag.
	Confidence float64 `json:"confidence"`

	// Validated records whether Evidence was checked against the bytes and found
	// to support Value.
	Validated bool `json:"validated"`

	// Unsupported marks an assertion whose claimed evidence could not be located in
	// the observation. Recorded, never silently dropped — a value that cannot be
	// grounded is a finding about the derivation, and discarding it would hide
	// exactly the cases worth looking at.
	Unsupported bool `json:"unsupported,omitempty"`

	// DerivedFrom lists upstream assertion identities this was computed from, for
	// claims built on other claims rather than directly on bytes.
	DerivedFrom []string `json:"derived_from,omitempty"`
}

// Validate locates claimed in body and records the result on a.
//
// Three outcomes, all of them recorded:
//
//   - Found verbatim: the byte range is stored and Validated is set. The claim is
//     now checkable by anyone holding the bytes.
//   - Found after whitespace normalisation: the same, mapped back to real offsets
//     in the original body. Extracted text is almost always whitespace-normalised
//     relative to its source, so requiring a verbatim match would fail nearly
//     everything for a reason that has nothing to do with truthfulness.
//   - Not found: Validated is false, Unsupported is true, Confidence is zeroed, and
//     the assertion is kept.
//
// A failed match is not proof of fabrication, and must not be reported as one. Text
// spanning inline markup — "Hello world" from "Hello <b>world</b>" — is real text
// from the page that does not appear as a contiguous byte range anywhere in it. That
// is a known limit of matching against raw bytes rather than a tag-aware projection
// of them, and the honest reading of Unsupported is "this value could not be
// grounded by this method", not "this value is false".
func (a *Assertion) Validate(observationHash string, body []byte, claimed string) {
	a.Evidence.ObservationHash = observationHash

	start, end, ok := locate(body, claimed)
	if !ok {
		a.Evidence.ByteStart, a.Evidence.ByteEnd = 0, 0
		a.Validated = false
		a.Unsupported = true
		a.Confidence = 0
		return
	}

	a.Evidence.ByteStart, a.Evidence.ByteEnd = start, end
	a.Validated = true
	a.Unsupported = false
}

// locate finds claimed within body and returns its byte range.
//
// Tries the exact bytes first, since a hit there needs no explanation. Falls back to
// a whitespace-normalised search that still reports true offsets into body, by
// walking the original while matching the normalised form rather than searching a
// normalised copy and losing the mapping.
func locate(body []byte, claimed string) (start, end int, ok bool) {
	if len(body) == 0 || claimed == "" {
		return 0, 0, false
	}

	if i := bytes.Index(body, []byte(claimed)); i >= 0 {
		return i, i + len(claimed), true
	}

	needle := normaliseSpace([]byte(claimed))
	if len(needle) == 0 {
		return 0, 0, false
	}

	// norm holds the normalised body; offsets maps each normalised byte back to the
	// index in body it came from, which is what makes the result a real span rather
	// than a position in a temporary string.
	norm, offsets := normaliseWithOffsets(body)
	i := bytes.Index(norm, needle)
	if i < 0 {
		return 0, 0, false
	}

	start = offsets[i]
	// The end offset is one past the source byte the last matched byte came from.
	last := offsets[i+len(needle)-1]
	return start, last + 1, true
}

// normaliseSpace collapses every run of whitespace to a single space and trims the
// ends.
func normaliseSpace(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inSpace := true // leading whitespace is trimmed by starting in the space state
	for _, c := range b {
		if isSpaceByte(c) {
			if !inSpace {
				out = append(out, ' ')
				inSpace = true
			}
			continue
		}
		out = append(out, c)
		inSpace = false
	}
	return bytes.TrimRight(out, " ")
}

// normaliseWithOffsets is normaliseSpace that also reports, for each output byte,
// which input byte produced it.
func normaliseWithOffsets(b []byte) (norm []byte, offsets []int) {
	norm = make([]byte, 0, len(b))
	offsets = make([]int, 0, len(b))
	inSpace := true
	for i, c := range b {
		if isSpaceByte(c) {
			if !inSpace {
				norm = append(norm, ' ')
				offsets = append(offsets, i)
				inSpace = true
			}
			continue
		}
		norm = append(norm, c)
		offsets = append(offsets, i)
		inSpace = false
	}
	// Trailing spaces are dropped from both, keeping them the same length.
	for len(norm) > 0 && norm[len(norm)-1] == ' ' {
		norm = norm[:len(norm)-1]
		offsets = offsets[:len(offsets)-1]
	}
	return norm, offsets
}

func isSpaceByte(c byte) bool {
	return c < unicode.MaxASCII && (c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v')
}

// Observation returns the fetch half of a record as its own value.
//
// Record is the flat, storage-facing shape and Observation is the structured one;
// they describe the same fetch, and this is the single place that says so. It
// exists so that assertions can be joined to what they are about without a reader
// having to know that Record.ContentHash and EvidenceSpan.ObservationHash are the
// same key by convention — here it is the same key by construction.
func (r Record) Observation() Observation {
	return Observation{
		Hash:            r.ContentHash,
		URL:             r.URL,
		FinalURL:        r.FinalURL,
		CanonicalURL:    r.CanonicalURL,
		FetchedAt:       r.FetchedAt,
		StatusCode:      r.StatusCode,
		MIMEType:        r.MIMEType,
		CrawlerIdentity: r.CrawlerIdentity,
		CrawlID:         r.CrawlID,
		Policy: PolicyState{
			RobotsAllowed: r.RobotsAllowed,
			AIDirectives:  r.AIDirectives,
			Signals:       r.Signals,
		},
	}
}
