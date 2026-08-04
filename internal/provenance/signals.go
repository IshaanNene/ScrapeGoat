// Package provenance records where a corpus record came from and what the source
// said about how it may be used.
//
// This is not bookkeeping. A crawled dataset is defensible exactly to the extent
// that it can answer "where did this come from, and were you allowed to take it?"
// — and the answer has to be captured at fetch time, because the page can change
// its mind afterwards and the crawl cannot go back and ask.
//
// The signals here are deliberately recorded rather than enforced. A crawler that
// silently dropped every page carrying a restrictive signal would produce a corpus
// whose gaps are invisible, and a downstream user who wanted a different policy
// would have no way to apply it. Recording keeps the decision where it belongs:
// with whoever builds the dataset, in full view of what the site asked for.
package provenance

import (
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// PageSignals is what one page said about its own reuse.
//
// Every field is a statement the source made, not a judgement this package
// reached. Absence is meaningful and distinct from denial: NoAI false means "the
// page did not say no", never "the page said yes".
type PageSignals struct {
	// NoIndex and NoFollow are the classic robots meta directives, which arrive by
	// meta tag or X-Robots-Tag header — the header is easy to forget and carries
	// exactly the same weight.
	NoIndex  bool `json:"noindex,omitempty"`
	NoFollow bool `json:"nofollow,omitempty"`

	// NoAI and NoImageAI are the emerging opt-outs for model training. Not a
	// standard, and honoured inconsistently, which is a reason to record them
	// rather than a reason to ignore them.
	NoAI      bool `json:"noai,omitempty"`
	NoImageAI bool `json:"noimageai,omitempty"`

	// TDMReservation is the W3C Text and Data Mining Reservation Protocol: 0 means
	// mining is permitted, 1 means rights are reserved. Nil means the page was
	// silent, which is not the same as 0 — under some jurisdictions silence and
	// permission are genuinely different answers.
	TDMReservation *int   `json:"tdm_reservation,omitempty"`
	TDMPolicy      string `json:"tdm_policy,omitempty"`

	// Licence, when the page declares one. Sources are ranked by how explicit they
	// are; see licenceFrom.
	Licence       string `json:"licence,omitempty"`
	LicenceSource string `json:"licence_source,omitempty"`

	// Canonical is the page's own claim about its address. Recorded because a
	// corpus keyed on the fetched URL will hold the same document many times over
	// when a site paginates or tracks with query parameters.
	Canonical string `json:"canonical,omitempty"`
}

// Restrictive reports whether the page asked to be left out of AI training.
//
// A convenience for callers who want to filter, not a policy this package
// applies. NoIndex is deliberately excluded: it is a statement about search
// engines, and reading it as an AI opt-out would put words in the source's mouth.
func (s PageSignals) Restrictive() bool {
	if s.NoAI || s.NoImageAI {
		return true
	}
	return s.TDMReservation != nil && *s.TDMReservation == 1
}

// robotsTokens are the directive names understood in meta robots and
// X-Robots-Tag. Both carry the same vocabulary in the same comma-separated shape.
func parseRobotsTokens(content string, s *PageSignals) {
	for _, tok := range strings.Split(content, ",") {
		switch strings.ToLower(strings.TrimSpace(tok)) {
		case "noindex":
			s.NoIndex = true
		case "nofollow":
			s.NoFollow = true
		case "noai":
			s.NoAI = true
		case "noimageai":
			s.NoImageAI = true
		case "none":
			// "none" is defined as noindex plus nofollow.
			s.NoIndex, s.NoFollow = true, true
		}
	}
}

// FromHeaders reads the signals a response carries in its headers.
func FromHeaders(h http.Header) PageSignals {
	var s PageSignals
	if h == nil {
		return s
	}

	// X-Robots-Tag may appear more than once, and each value may itself name a
	// specific user agent ("googlebot: noindex"). The agent-scoped form is recorded
	// as if unscoped: this is a report of what the site said, and a directive aimed
	// at one crawler is still evidence of intent.
	for _, v := range h.Values("X-Robots-Tag") {
		if _, rest, found := strings.Cut(v, ":"); found {
			v = rest
		}
		parseRobotsTokens(v, &s)
	}

	if v := strings.TrimSpace(h.Get("TDM-Reservation")); v != "" {
		if n, ok := tdmValue(v); ok {
			s.TDMReservation = &n
		}
	}
	if v := strings.TrimSpace(h.Get("TDM-Policy")); v != "" {
		s.TDMPolicy = v
	}

	// A Link header can carry rel="license" just as the HTML element can.
	if lic := licenceFromLinkHeader(h.Values("Link")); lic != "" {
		s.Licence, s.LicenceSource = lic, "link-header"
	}

	return s
}

// FromDocument reads the signals a parsed page carries in its markup.
func FromDocument(doc *goquery.Document) PageSignals {
	var s PageSignals
	if doc == nil {
		return s
	}

	doc.Find("meta").Each(func(_ int, m *goquery.Selection) {
		name, _ := m.Attr("name")
		if name == "" {
			// Some pages use http-equiv, and a few use property.
			if v, ok := m.Attr("property"); ok {
				name = v
			}
		}
		content, _ := m.Attr("content")

		switch strings.ToLower(strings.TrimSpace(name)) {
		case "robots":
			parseRobotsTokens(content, &s)
		case "tdm-reservation":
			if n, ok := tdmValue(strings.TrimSpace(content)); ok {
				s.TDMReservation = &n
			}
		case "tdm-policy":
			s.TDMPolicy = strings.TrimSpace(content)
		case "license", "licence", "dcterms.license", "dc.rights":
			if v := strings.TrimSpace(content); v != "" && s.Licence == "" {
				s.Licence, s.LicenceSource = v, "meta"
			}
		}
	})

	// A named-crawler meta tag is not standard but is used in practice, and it is
	// unambiguous about intent when present.
	doc.Find(`meta[name]`).Each(func(_ int, m *goquery.Selection) {
		name, _ := m.Attr("name")
		if isAIAgentToken(name) {
			content, _ := m.Attr("content")
			parseRobotsTokens(content, &s)
			if strings.Contains(strings.ToLower(content), "noindex") {
				s.NoAI = true
			}
		}
	})

	if href, ok := doc.Find(`link[rel~="license"]`).First().Attr("href"); ok {
		if v := strings.TrimSpace(href); v != "" {
			// A link element is more explicit than a meta tag, so it wins.
			s.Licence, s.LicenceSource = v, "link"
		}
	}

	if href, ok := doc.Find(`link[rel="canonical"]`).First().Attr("href"); ok {
		s.Canonical = strings.TrimSpace(href)
	}

	return s
}

// Merge combines header and document signals.
//
// Restrictions union rather than override: a header saying noai and a page saying
// nothing still means noai, and the reverse holds too. Taking the more restrictive
// reading is the only safe direction when the two disagree, because the cost of
// wrongly including a page that asked to be left out is not symmetric with the
// cost of wrongly excluding one.
func Merge(header, doc PageSignals) PageSignals {
	out := PageSignals{
		NoIndex:   header.NoIndex || doc.NoIndex,
		NoFollow:  header.NoFollow || doc.NoFollow,
		NoAI:      header.NoAI || doc.NoAI,
		NoImageAI: header.NoImageAI || doc.NoImageAI,
		Canonical: doc.Canonical,
	}

	// Reservation: 1 wins over 0, and either wins over silence.
	switch {
	case header.TDMReservation != nil && *header.TDMReservation == 1:
		out.TDMReservation = header.TDMReservation
	case doc.TDMReservation != nil && *doc.TDMReservation == 1:
		out.TDMReservation = doc.TDMReservation
	case header.TDMReservation != nil:
		out.TDMReservation = header.TDMReservation
	case doc.TDMReservation != nil:
		out.TDMReservation = doc.TDMReservation
	}

	out.TDMPolicy = firstNonEmpty(header.TDMPolicy, doc.TDMPolicy)

	// Licence: the document's own declaration is preferred over a Link header,
	// since the header is more often set by infrastructure than by the author.
	if doc.Licence != "" {
		out.Licence, out.LicenceSource = doc.Licence, doc.LicenceSource
	} else {
		out.Licence, out.LicenceSource = header.Licence, header.LicenceSource
	}

	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// tdmValue parses a reservation value. The protocol defines only 0 and 1; anything
// else is treated as unsaid rather than guessed at.
func tdmValue(v string) (int, bool) {
	switch v {
	case "0":
		return 0, true
	case "1":
		return 1, true
	}
	return 0, false
}

// licenceFromLinkHeader pulls the href out of a Link header with rel="license".
func licenceFromLinkHeader(values []string) string {
	for _, header := range values {
		for _, link := range strings.Split(header, ",") {
			parts := strings.Split(link, ";")
			if len(parts) < 2 {
				continue
			}

			var isLicence bool
			for _, p := range parts[1:] {
				p = strings.TrimSpace(strings.ToLower(p))
				if strings.HasPrefix(p, "rel=") {
					rel := strings.Trim(strings.TrimPrefix(p, "rel="), `"'`)
					for _, r := range strings.Fields(rel) {
						if r == "license" {
							isLicence = true
						}
					}
				}
			}
			if !isLicence {
				continue
			}

			href := strings.TrimSpace(parts[0])
			href = strings.TrimPrefix(href, "<")
			href = strings.TrimSuffix(href, ">")
			if href != "" {
				return href
			}
		}
	}
	return ""
}
