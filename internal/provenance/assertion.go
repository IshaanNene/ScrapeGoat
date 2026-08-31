package provenance

import (
	"bytes"
	"html"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
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
	NewEvidenceIndex(observationHash, body).Ground(a, claimed)
}

// EvidenceIndex grounds many claims against one observation.
//
// A page yields as many claims as the operator configured selectors, and each has
// to be located in the same bytes. Doing that independently would rebuild the
// normalised projection of the body once per value — two allocations the size of
// the page each time, on a path that runs for every page in the crawl.
//
// The projection is built lazily and at most once, because most selector results
// are found by the exact search and never need it.
type EvidenceIndex struct {
	hash string
	body []byte

	// Two projections, built lazily and at most once each.
	//
	// markup keeps tags, because an attribute value lives inside one: the href a
	// selector read is only present in the source as part of `<a href="...">`.
	// text drops them, because prose does not survive them: an article extracted
	// from a page is a join of text across dozens of elements and appears nowhere
	// in the source as a contiguous run.
	markupOnce sync.Once
	markup     projection

	textOnce sync.Once
	text     projection
}

// projection is a rendering of the body plus, for each rendered byte, the source
// range it came from.
type projection struct {
	norm   []byte
	starts []int
	ends   []int
}

// NewEvidenceIndex prepares body for grounding. Cheap: the expensive part is
// deferred until a lookup actually needs it.
func NewEvidenceIndex(hash string, body []byte) *EvidenceIndex {
	return &EvidenceIndex{hash: hash, body: body}
}

// Hash is the observation the index grounds against.
func (ix *EvidenceIndex) Hash() string { return ix.hash }

// Locate finds claimed within the body and returns its byte range.
//
// Tries the exact bytes first, since a hit there needs no explanation. Falls back
// to a whitespace-normalised search that still reports true offsets into the
// original, by mapping each normalised byte back to the source byte it came from
// rather than searching a copy and losing the correspondence.
func (ix *EvidenceIndex) Locate(claimed string) (start, end int, ok bool) {
	if ix == nil || len(ix.body) == 0 || claimed == "" {
		return 0, 0, false
	}

	if i := indexAligned(ix.body, []byte(claimed)); i >= 0 {
		return i, i + len(claimed), true
	}

	needle := normaliseSpace([]byte(claimed))
	if len(needle) == 0 {
		return 0, 0, false
	}

	// Markup-preserving first. It is the stricter of the two — a hit there means
	// the value appears as one run of source text — and it is the one that can
	// match an attribute value at all.
	ix.markupOnce.Do(func() { ix.markup = buildProjection(ix.body, keepTags) })
	if i := indexAligned(ix.markup.norm, needle); i >= 0 {
		return ix.markup.starts[i], ix.markup.ends[i+len(needle)-1], true
	}

	// Then without tags, which is the only way prose can be found. The span this
	// yields covers the markup between the first and last matched characters, and
	// that is the right citation: the claim is that this region of the document is
	// where the text came from, not that the text sits in it contiguously.
	ix.textOnce.Do(func() { ix.text = buildProjection(ix.body, stripTags) })
	if i := indexAligned(ix.text.norm, needle); i >= 0 {
		return ix.text.starts[i], ix.text.ends[i+len(needle)-1], true
	}

	return 0, 0, false
}

// indexAligned is bytes.Index restricted to matches that begin and end on a
// character boundary.
//
// The search is over bytes, and without this a needle could match the interior of
// a multi-byte character: decoding `&ac;` yields ∾, three bytes, and a claim
// consisting of that character's second byte alone would "match" it. The resulting
// span would be well-formed, in bounds, and would cut a fragment of a character
// nobody claimed — a citation that is wrong in a way no bounds check would notice.
//
// Found by the fuzz target, on an input no real extractor would produce and every
// hostile one could.
func indexAligned(haystack, needle []byte) int {
	for from := 0; from <= len(haystack)-len(needle); {
		j := bytes.Index(haystack[from:], needle)
		if j < 0 {
			return -1
		}
		i := from + j
		if runeAligned(haystack, i, len(needle)) {
			return i
		}
		from = i + 1
	}
	return -1
}

// runeAligned reports whether [i, i+n) begins and ends on a character boundary.
func runeAligned(b []byte, i, n int) bool {
	if !utf8.RuneStart(b[i]) {
		return false
	}
	end := i + n
	return end >= len(b) || utf8.RuneStart(b[end])
}

// Ground records on a whether claimed can be found in the observation.
//
// The three outcomes are the ones described on Validate, which is this with a
// single-use index.
func (ix *EvidenceIndex) Ground(a *Assertion, claimed string) {
	if a == nil || ix == nil {
		return
	}
	a.Evidence.ObservationHash = ix.hash

	start, end, ok := ix.Locate(claimed)
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

// normaliseSpace collapses every run of whitespace to a single space and trims the
// ends.
func normaliseSpace(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inSpace := true // leading whitespace is trimmed by starting in the space state

	// Decoded explicitly rather than with `range` over a string, because that
	// substitutes U+FFFD for every invalid byte. buildProjection passes those
	// bytes through — a span has to index the bytes that are actually there — so
	// substituting here would give the needle a three-byte replacement character
	// where the haystack still had the original, and the two would never match.
	// Pages with a mis-declared encoding are common enough that this is a real
	// case rather than a fuzzer's invention.
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size <= 1 {
			out = append(out, b[i])
			inSpace = false
			i++
			continue
		}
		if unicode.IsSpace(r) {
			if !inSpace {
				out = append(out, ' ')
				inSpace = true
			}
			i += size
			continue
		}
		out = append(out, b[i:i+size]...)
		inSpace = false
		i += size
	}
	return bytes.TrimRight(out, " ")
}

// tagMode says whether a projection keeps markup or drops it.
type tagMode bool

const (
	keepTags  tagMode = false
	stripTags tagMode = true
)

// buildProjection renders body the way an extractor sees it, while remembering
// where every rendered byte came from.
//
// Three transformations, because each stands between a value and the bytes that
// produced it, and any one alone leaves most of the web unmatched:
//
//   - Whitespace is collapsed. Source HTML is indented; extracted text is not.
//   - Character references are decoded. `It&#39;s` in the source is `It's` once
//     any DOM library has read it, and an apostrophe is not an exotic character —
//     requiring a literal match would fail on ampersands, quotes, dashes and
//     non-breaking spaces, which is a large fraction of real prose.
//   - Tags are dropped, when asked. Main-content extraction returns text joined
//     across dozens of elements, which appears nowhere in the source as one run.
//     Each tag renders as a space, matching how the extractor joins the blocks it
//     selected, so `<p>a</p><p>b</p>` renders as `a b` rather than `ab`.
//
// starts and ends give each rendered byte its source range rather than a single
// offset, because a decoded reference is one rendered character from five source
// bytes. Recording only the start and adding one would cut a span off mid-entity
// and produce a citation that does not parse.
//
// The tag-as-space rule has a known cost: markup inside a word, `co<b>de</b>`,
// renders as `co de` and will not match the extractor's `code`. Prose does not
// usually do that, and the failure is a value that cannot be grounded rather than
// one grounded wrongly.
func buildProjection(body []byte, mode tagMode) projection {
	p := projection{
		norm:   make([]byte, 0, len(body)),
		starts: make([]int, 0, len(body)),
		ends:   make([]int, 0, len(body)),
	}

	inSpace := true // leading whitespace is trimmed by starting in the space state

	// Whitespace is judged per rune, not per byte. A page that writes &nbsp; or a
	// thin space is writing whitespace, and every DOM-based extractor collapses it
	// — so a byte-wise projection keeps a character the claim does not have, and
	// the value stops matching a page it plainly came from. U+00A0 in particular
	// is everywhere in real markup.
	emitRune := func(r rune, from, to int) {
		if unicode.IsSpace(r) {
			if inSpace {
				return
			}
			p.norm = append(p.norm, ' ')
			p.starts = append(p.starts, from)
			p.ends = append(p.ends, to)
			inSpace = true
			return
		}
		var buf [utf8.UTFMax]byte
		n := utf8.EncodeRune(buf[:], r)
		for k := 0; k < n; k++ {
			p.norm = append(p.norm, buf[k])
			p.starts = append(p.starts, from)
			p.ends = append(p.ends, to)
		}
		inSpace = false
	}

	emitByte := func(c byte, from, to int) {
		p.norm = append(p.norm, c)
		p.starts = append(p.starts, from)
		p.ends = append(p.ends, to)
		inSpace = false
	}

	for i := 0; i < len(body); {
		c := body[i]

		if mode == stripTags && c == '<' {
			// Some elements contain characters that are not page text: script and
			// style bodies, the label on a submit button, the fallback inside an
			// iframe. Every extractor drops them, so keeping their contents here
			// would interleave JavaScript and button labels with the prose and
			// stop an article matching a page it plainly came from.
			if end, ok := skipElement(body, i, nonTextElements...); ok {
				emitRune(' ', i, end)
				i = end
				continue
			}
			if end, ok := skipTag(body, i); ok {
				emitRune(' ', i, end)
				i = end
				continue
			}
		}

		if c == '&' {
			if decoded, end, ok := decodeEntity(body, i); ok {
				for _, r := range string(decoded) {
					emitRune(r, i, end)
				}
				i = end
				continue
			}
		}

		r, size := utf8.DecodeRune(body[i:])
		if r == utf8.RuneError && size <= 1 {
			// Not valid UTF-8. Pass the byte through rather than replacing it:
			// the span has to index the bytes that are actually there.
			emitByte(c, i, i+1)
			i++
			continue
		}

		if unicode.IsSpace(r) {
			j := i
			for j < len(body) {
				rr, sz := utf8.DecodeRune(body[j:])
				if sz == 0 || !unicode.IsSpace(rr) || (rr == utf8.RuneError && sz <= 1) {
					break
				}
				j += sz
			}
			emitRune(' ', i, j)
			i = j
			continue
		}

		emitRune(r, i, i+size)
		i += size
	}

	for len(p.norm) > 0 && p.norm[len(p.norm)-1] == ' ' {
		p.norm = p.norm[:len(p.norm)-1]
		p.starts = p.starts[:len(p.starts)-1]
		p.ends = p.ends[:len(p.ends)-1]
	}
	return p
}

// skipElement returns the index just past the whole element starting at body[i],
// contents included, when it opens one of the named tags.
//
// An unclosed element is not skipped at all rather than swallowing the remainder
// of the document: a projection that silently discarded half a page would ground
// nothing and explain nothing.
func skipElement(body []byte, i int, names ...string) (end int, ok bool) {
	for _, name := range names {
		open := "<" + name
		if len(body)-i < len(open) || !strings.EqualFold(string(body[i:i+len(open)]), open) {
			continue
		}
		// The next character must end the name, or "<scriptish" would match.
		if n := i + len(open); n < len(body) && !isSpaceByte(body[n]) && body[n] != '>' && body[n] != '/' {
			continue
		}
		closing := []byte("</" + name)
		rest := body[i:]
		idx := bytes.Index(bytes.ToLower(rest), closing)
		if idx < 0 {
			return 0, false
		}
		after := i + idx + len(closing)
		if gt := bytes.IndexByte(body[after:], '>'); gt >= 0 {
			return after + gt + 1, true
		}
		return 0, false
	}
	return 0, false
}

// skipTag returns the index just past the tag starting at body[i], which must be
// '<'. Quoted attribute values may contain '>', so the scan tracks quoting rather
// than stopping at the first one.
func skipTag(body []byte, i int) (end int, ok bool) {
	// Comments end at "-->" and not at the first '>', which is part of it. Getting
	// this wrong leaves the comment's body in the projection as though it were
	// page text — and pages comment out whole blocks of markup, so the text a
	// reader never sees ends up interleaved with the text they do.
	if bytes.HasPrefix(body[i:], []byte("<!--")) {
		if j := bytes.Index(body[i+4:], []byte("-->")); j >= 0 {
			return i + 4 + j + 3, true
		}
		return 0, false
	}

	var quote byte
	for j := i + 1; j < len(body); j++ {
		c := body[j]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return j + 1, true
		}
	}
	// An unterminated tag is malformed input; treat the '<' as ordinary text
	// rather than swallowing the rest of the document.
	return 0, false
}

// nonTextElements have their contents dropped, not just their tags.
//
// This mirrors the first line of Extractor.strip in internal/extract, and has to:
// a projection that keeps what the extractor threw away cannot match what the
// extractor returned. The two lists are not shared because provenance has no
// business depending on an extractor, and a corpus should be groundable against
// text produced by any of them. The golden corpus is what keeps them honest — if
// they drift, density claims stop grounding and the frozen output says so.
//
// Deliberately excludes nav, aside, footer and header, which the extractor also
// removes. Those hold real text; the extractor drops them as boilerplate rather
// than as non-text, and dropping them here would make the projection an opinion
// about what matters rather than a rendering of what is there.
var nonTextElements = []string{
	"script", "style", "noscript", "svg", "iframe", "form", "button", "template",
}

// maxEntityLen bounds the search for a reference's closing semicolon. The longest
// named reference in the HTML standard is well under this; the cap is what stops a
// stray ampersand in prose from scanning the rest of the document.
const maxEntityLen = 34

// decodeEntity decodes the character reference starting at body[i], which must be
// '&'. It reports the decoded bytes and the index just past the reference.
//
// Only terminated references are recognised. HTML permits a few without the
// semicolon, but accepting those here would let "AT&T" consume the T, and a wrong
// span is worse than a missing one.
func decodeEntity(body []byte, i int) (decoded []byte, end int, ok bool) {
	limit := min(i+maxEntityLen, len(body))
	for j := i + 1; j < limit; j++ {
		if body[j] == ';' {
			raw := string(body[i : j+1])
			out := html.UnescapeString(raw)
			if out == raw {
				return nil, 0, false // not a reference this library knows
			}
			// A result still carrying the terminator means only a prefix decoded.
			// HTML allows a few references without their semicolon, so
			// UnescapeString reads "&gt0;" as "&gt" followed by the literal "0;"
			// and returns ">0;". Treating that as one reference would map three
			// rendered bytes onto five source bytes and hand back a span covering
			// characters nobody claimed.
			//
			// This also rejects "&semi;" and "&#59;", which legitimately decode to
			// a semicolon. That is the safe direction to be wrong in: the cost is
			// a value that cannot be grounded, against a citation that points at
			// the wrong text.
			if strings.HasSuffix(out, ";") {
				return nil, 0, false
			}
			return []byte(out), j + 1, true
		}
		// A reference contains no whitespace and no second ampersand; either means
		// this one was never a reference.
		if isSpaceByte(body[j]) || body[j] == '&' {
			return nil, 0, false
		}
	}
	return nil, 0, false
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

// SpanSupports reports whether source renders to claimed.
//
// The check an evidence span exists to make possible: cut the bytes the span names
// and ask whether they say what the assertion says they say. Both projections are
// tried, because prose is only found with markup dropped while an attribute value
// is only present with it kept, and a span is legitimate if either renders to the
// claim.
//
// Exported because verification is the point. A corpus whose spans can only be
// checked by the code that wrote them is not evidence, it is a second assertion.
func SpanSupports(source []byte, claimed string) bool {
	want := normaliseSpace([]byte(claimed))
	if len(want) == 0 {
		return false
	}
	if bytes.Equal(buildProjection(source, keepTags).norm, want) {
		return true
	}
	return bytes.Equal(buildProjection(source, stripTags).norm, want)
}
