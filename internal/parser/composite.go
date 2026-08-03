package parser

import (
	"log/slog"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// CompositeParser combines multiple parser implementations.
// It delegates to the appropriate parser based on rule type.
type CompositeParser struct {
	css        *CSSParser
	regex      *RegexParser
	xpath      *XPathParser
	structured *StructuredDataExtractor
	logger     *slog.Logger
}

// NewCompositeParser creates a parser that handles CSS, regex, and XPath rules.
func NewCompositeParser(logger *slog.Logger) *CompositeParser {
	return &CompositeParser{
		css:        NewCSSParser(logger),
		regex:      NewRegexParser(logger),
		xpath:      NewXPathParser(logger),
		structured: NewStructuredDataExtractor(logger),
		logger:     logger.With("component", "composite_parser"),
	}
}

// Parse implements Parser by delegating to sub-parsers.
func (p *CompositeParser) Parse(resp *types.Response, rules []config.ParseRule) ([]*types.Item, []string, error) {
	var allItems []*types.Item
	var allLinks []string

	// Separate rules by type
	var cssRules []config.ParseRule
	var regexRules []config.ParseRule
	var xpathRules []config.ParseRule

	for _, rule := range rules {
		switch rule.Type {
		case "regex":
			regexRules = append(regexRules, rule)
		case "xpath":
			xpathRules = append(xpathRules, rule)
		default: // "css" or empty defaults to CSS
			cssRules = append(cssRules, rule)
		}
	}

	// CSS parsing (always runs for link discovery)
	cssItems, links, err := p.css.Parse(resp, cssRules)
	if err != nil {
		p.logger.Warn("CSS parser error", "error", err)
	}
	allItems = append(allItems, cssItems...)
	allLinks = append(allLinks, links...)

	// Regex parsing
	if len(regexRules) > 0 {
		regexItems, _, err := p.regex.Parse(resp, regexRules)
		if err != nil {
			p.logger.Warn("regex parser error", "error", err)
		}
		allItems = append(allItems, regexItems...)
	}

	// XPath parsing
	if len(xpathRules) > 0 {
		xpathItems, _, err := p.xpath.Parse(resp, xpathRules)
		if err != nil {
			p.logger.Warn("XPath parser error", "error", err)
		}
		allItems = append(allItems, xpathItems...)
	}

	// Auto-extract structured data (JSON-LD, OpenGraph, etc.)
	sdResults, err := p.structured.Extract(resp)
	if err != nil {
		p.logger.Warn("structured data extraction error", "error", err)
	}
	if sdItem := StructuredDataToItem(sdResults, resp.Request.URLString()); sdItem != nil {
		allItems = append(allItems, sdItem)
	}

	// Merge items from different parsers targeting the same page
	if len(allItems) > 1 {
		merged := types.NewItem(resp.Request.URLString())
		for _, item := range allItems {
			for k, v := range item.Fields {
				merged.Set(k, v)
			}
		}
		allItems = []*types.Item{merged}
	}

	return allItems, allLinks, nil
}
