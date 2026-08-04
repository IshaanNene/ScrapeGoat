package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Content is the extracted text handed to the builder.
//
// Restated here as a plain struct rather than imported from internal/extract, so
// that provenance does not depend on one particular extractor. A caller using a
// different one — or none — can still produce records.
type Content struct {
	Text       string
	Title      string
	Language   string
	Confidence float64
}

// Source is everything the builder needs about one fetched response.
//
// The caller supplies ContentHash rather than the builder computing it, because
// the authoritative value is the one the fetch log filed the body under. Deriving
// it a second time here would let the two drift, and the whole point of the field
// is that it joins a record to bytes someone else can check.
type Source struct {
	URL         string
	FinalURL    string
	ContentHash string
	Body        []byte

	StatusCode int
	Headers    http.Header
	FetchedAt  time.Time

	CrawlerIdentity string
	CrawlID         string

	// RobotsAllowed is the decision the crawl actually operated under.
	RobotsAllowed bool

	// Robots is what the site's robots.txt said to everyone. Zero value means no
	// robots.txt was seen, which the record distinguishes from an empty one.
	Robots RobotsReport
}

// Build assembles a record from a fetched response and its extracted content.
//
// doc may be nil, in which case only header signals are read. That is the right
// behaviour for a non-HTML response rather than an error: a PDF has provenance
// too, it just has fewer places to state it.
func Build(src Source, doc *goquery.Document, content Content) Record {
	signals := Merge(FromHeaders(src.Headers), FromDocument(doc))

	hash := src.ContentHash
	if hash == "" && src.Body != nil {
		// Only as a fallback for callers not running through the fetch log. Kept
		// identical to fetchlog.Digest so the two agree when both are present.
		sum := sha256.Sum256(src.Body)
		hash = hex.EncodeToString(sum[:])
	}

	rec := Record{
		SchemaVersion: SchemaVersion,

		URL:          src.URL,
		CanonicalURL: signals.Canonical,
		ContentHash:  hash,

		FetchedAt:       src.FetchedAt,
		StatusCode:      src.StatusCode,
		MIMEType:        mimeType(src.Headers),
		FinalURL:        src.FinalURL,
		CrawlerIdentity: src.CrawlerIdentity,

		Text:                 content.Text,
		Title:                content.Title,
		Language:             language(src.Headers, doc, content.Language),
		ExtractionConfidence: content.Confidence,

		RobotsAllowed: src.RobotsAllowed,
		Signals:       signals,
		CrawlID:       src.CrawlID,
	}

	// Only attach a directive summary when a robots.txt was actually seen. An
	// empty summary would read as "the site said nothing", which is a claim, and
	// we would not be entitled to make it.
	if src.Robots.Present {
		rec.AIDirectives = SummariseDirectives(src.Robots)
	}

	return rec
}

// mimeType returns the media type without parameters, lowercased.
func mimeType(h http.Header) string {
	if h == nil {
		return ""
	}
	ct := h.Get("Content-Type")
	if ct == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		// A malformed Content-Type still tells us something; take the part before
		// the first semicolon rather than discarding the header entirely.
		if before, _, found := strings.Cut(ct, ";"); found {
			return strings.ToLower(strings.TrimSpace(before))
		}
		return strings.ToLower(strings.TrimSpace(ct))
	}
	return strings.ToLower(mt)
}

// language resolves the document language, preferring what the caller determined
// over what the page claims — a detector looks at the text, and <html lang> is
// frequently a template default nobody updated.
func language(h http.Header, doc *goquery.Document, detected string) string {
	if detected != "" {
		return normaliseLang(detected)
	}
	if doc != nil {
		if v, ok := doc.Find("html").First().Attr("lang"); ok && strings.TrimSpace(v) != "" {
			return normaliseLang(v)
		}
	}
	if h != nil {
		if v := h.Get("Content-Language"); v != "" {
			// A list means the server did not commit to one; take the first.
			if first, _, found := strings.Cut(v, ","); found {
				return normaliseLang(first)
			}
			return normaliseLang(v)
		}
	}
	return ""
}

func normaliseLang(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}
