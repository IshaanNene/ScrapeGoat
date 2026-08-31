package parser

import (
	"log/slog"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// CSSParser extracts data using CSS selectors via goquery.
type CSSParser struct {
	logger *slog.Logger
}

// NewCSSParser creates a new CSS selector parser.
func NewCSSParser(logger *slog.Logger) *CSSParser {
	return &CSSParser{
		logger: logger.With("component", "css_parser"),
	}
}

// Parse implements Parser as a view over Derive.
//
// One derivation, two ways of looking at it. Parse existed first and is kept for
// callers that only want items; it must not become a second extraction path.
func (p *CSSParser) Parse(resp *types.Response, rules []config.ParseRule) ([]*types.Item, []string, error) {
	assertions, links, err := p.Derive(resp, rules)
	if err != nil {
		return nil, nil, err
	}
	var items []*types.Item
	if item := ItemFromAssertions(resp.Request.URLString(), assertions); item != nil {
		items = append(items, item)
	}
	return items, links, nil
}

// Derive extracts the page's links and one assertion per matched value.
func (p *CSSParser) Derive(resp *types.Response, rules []config.ParseRule) ([]provenance.Assertion, []string, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, nil, &types.ParseError{
			URL: resp.Request.URLString(),
			Err: err,
		}
	}

	// Extract links from the page
	links := p.extractLinks(doc, resp.FinalURL)

	// If no rules, just return links (discovery mode)
	if len(rules) == 0 {
		return nil, links, nil
	}

	var assertions []provenance.Assertion
	for _, rule := range rules {
		if rule.Type != "css" && rule.Type != "" {
			continue // Skip non-CSS rules
		}
		assertions = append(assertions,
			valueAssertions(rule.Name, cssMethod(rule), cssVersion, p.extractCSS(doc, rule))...)
	}

	return assertions, links, nil
}

// cssMethod names the derivation specifically enough to repeat it. The attribute
// is part of the identity: the same selector reading href and reading text are two
// different claims about the same elements.
func cssMethod(rule config.ParseRule) string {
	if rule.Attribute != "" && rule.Attribute != "text" {
		return "css:" + rule.Selector + "@" + rule.Attribute
	}
	return "css:" + rule.Selector
}

// extractCSS applies a single CSS rule and returns matched values.
func (p *CSSParser) extractCSS(doc *goquery.Document, rule config.ParseRule) []string {
	var values []string

	doc.Find(rule.Selector).Each(func(i int, sel *goquery.Selection) {
		var val string

		switch rule.Attribute {
		case "", "text":
			val = strings.TrimSpace(sel.Text())
		case "html", "innerHTML":
			val, _ = sel.Html()
		case "outerHTML":
			val, _ = goquery.OuterHtml(sel)
		default:
			val, _ = sel.Attr(rule.Attribute)
		}

		if val != "" {
			values = append(values, val)
		}
	})

	return values
}

// extractLinks finds all <a href> links in the document.
func (p *CSSParser) extractLinks(doc *goquery.Document, baseURL string) []string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}

	// A <base href> overrides the document URL for resolving every relative link
	// on the page. Ignoring it silently rewrites every relative link to the wrong
	// host — on a site that serves content from a CDN path, that means crawling
	// URLs that do not exist while never reaching the ones that do.
	//
	// Per the HTML spec only the first <base href> counts, and it is itself
	// resolved against the document URL so a relative base works.
	if href, ok := doc.Find("base[href]").First().Attr("href"); ok {
		if parsed, err := url.Parse(strings.TrimSpace(href)); err == nil {
			base = base.ResolveReference(parsed)
		}
	}

	seen := make(map[string]bool)
	var links []string

	doc.Find("a[href]").Each(func(i int, sel *goquery.Selection) {
		href, exists := sel.Attr("href")
		if !exists || href == "" {
			return
		}

		// Skip anchors, javascript:, mailto:, tel:
		href = strings.TrimSpace(href)
		if strings.HasPrefix(href, "#") ||
			strings.HasPrefix(href, "javascript:") ||
			strings.HasPrefix(href, "mailto:") ||
			strings.HasPrefix(href, "tel:") ||
			strings.HasPrefix(href, "data:") {
			return
		}

		// Resolve relative URLs
		parsedHref, err := url.Parse(href)
		if err != nil {
			return
		}
		resolved := base.ResolveReference(parsedHref)

		// Only follow HTTP/HTTPS links
		if resolved.Scheme != "http" && resolved.Scheme != "https" {
			return
		}

		// Remove fragment
		resolved.Fragment = ""

		absURL := resolved.String()
		if !seen[absURL] {
			seen[absURL] = true
			links = append(links, absURL)
		}
	})

	return links
}
