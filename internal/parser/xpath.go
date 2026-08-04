package parser

import (
	"log/slog"
	"strings"

	"github.com/antchfx/htmlquery"
	"golang.org/x/net/html"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
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

// Parse implements Parser for XPath rules.
func (p *XPathParser) Parse(resp *types.Response, rules []config.ParseRule) ([]*types.Item, []string, error) {
	doc, err := html.Parse(strings.NewReader(string(resp.Body)))
	if err != nil {
		return nil, nil, &types.ParseError{
			URL: resp.Request.URLString(),
			Err: err,
		}
	}

	item := types.NewItem(resp.Request.URLString())

	for _, rule := range rules {
		if rule.Type != "xpath" {
			continue
		}

		values := p.extractXPath(doc, rule)
		if len(values) == 1 {
			item.Set(rule.Name, values[0])
		} else if len(values) > 1 {
			item.Set(rule.Name, values)
		}
	}

	var items []*types.Item
	if len(item.Fields) > 0 {
		items = append(items, item)
	}

	return items, nil, nil
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
