package parser

import (
	"log/slog"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
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

// Parse implements Parser.
func (p *CSSParser) Parse(resp *types.Response, rules []config.ParseRule) ([]*types.Item, []string, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, nil, &types.ParseError{
			URL: resp.Request.URLString(),
			Err: err,
		}
	}

	var items []*types.Item

	// Extract links from the page
	links := p.extractLinks(doc, resp.FinalURL)

	// If no rules, just return links (discovery mode)
	if len(rules) == 0 {
		return nil, links, nil
	}

	// Apply extraction rules
	item := types.NewItem(resp.Request.URLString())

	for _, rule := range rules {
		if rule.Type != "css" && rule.Type != "" {
			continue // Skip non-CSS rules
		}

		values := p.extractCSS(doc, rule)
		if len(values) == 1 {
			item.Set(rule.Name, values[0])
		} else if len(values) > 1 {
			item.Set(rule.Name, values)
		}
	}

	if len(item.Fields) > 0 {
		items = append(items, item)
	}

	return items, links, nil
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
