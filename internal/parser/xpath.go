package parser

import (
	"log/slog"
	"strings"

	"github.com/antchfx/htmlquery"
	"golang.org/x/net/html"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// XPathParser extracts data using XPath expressions.
type XPathParser struct {
	logger *slog.Logger
}

// NewXPathParser creates a new XPath parser.
func NewXPathParser(logger *slog.Logger) *XPathParser {
	return &XPathParser{
		logger: logger.With("component", "xpath_parser"),
	}
}

// Parse implements Parser for XPath rules, as a view over Derive.
func (p *XPathParser) Parse(resp *types.Response, rules []config.ParseRule) ([]*types.Item, []string, error) {
	assertions, _, err := p.Derive(resp, rules)
	if err != nil {
		return nil, nil, err
	}
	var items []*types.Item
	if item := ItemFromAssertions(resp.Request.URLString(), assertions); item != nil {
		items = append(items, item)
	}
	return items, nil, nil
}

// Derive returns one assertion per value matched by an XPath rule.
func (p *XPathParser) Derive(resp *types.Response, rules []config.ParseRule) ([]provenance.Assertion, []string, error) {
	doc, err := html.Parse(strings.NewReader(string(resp.Body)))
	if err != nil {
		return nil, nil, &types.ParseError{
			URL: resp.Request.URLString(),
			Err: err,
		}
	}

	var assertions []provenance.Assertion
	for _, rule := range rules {
		if rule.Type != "xpath" {
			continue
		}
		assertions = append(assertions,
			valueAssertions(rule.Name, "xpath:"+rule.Selector, xpathVersion, p.extractXPath(doc, rule))...)
	}

	return assertions, nil, nil
}

// extractXPath applies a single XPath expression and returns matched values.
func (p *XPathParser) extractXPath(doc *html.Node, rule config.ParseRule) []string {
	nodes, err := htmlquery.QueryAll(doc, rule.Selector)
	if err != nil {
		p.logger.Warn("invalid xpath", "selector", rule.Selector, "error", err)
		return nil
	}

	var values []string
	for _, node := range nodes {
		var val string

		switch rule.Attribute {
		case "", "text":
			val = strings.TrimSpace(htmlquery.InnerText(node))
		case "html", "innerHTML":
			val = htmlquery.OutputHTML(node, false)
		case "outerHTML":
			val = htmlquery.OutputHTML(node, true)
		default:
			// Get attribute value
			val = htmlquery.SelectAttr(node, rule.Attribute)
		}

		if val != "" {
			values = append(values, val)
		}
	}

	return values
}
