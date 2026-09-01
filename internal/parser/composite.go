package parser

import (
	"log/slog"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
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

// Parse implements Parser as a view over Derive.
//
// The sub-parsers used to be asked for items and their items merged afterwards.
// They are now asked for assertions and the single item is projected from the
// merged set, which is the same output by a shorter route: merging items meant
// building four maps and collapsing them, and it discarded which parser had
// produced each field on the way.
func (p *CompositeParser) Parse(resp *types.Response, rules []config.ParseRule) ([]*types.Item, []string, error) {
	assertions, links, err := p.Derive(resp, rules)
	if err != nil {
		return nil, links, err
	}
	var items []*types.Item
	if item := p.ItemFrom(resp.Request.URLString(), assertions); item != nil {
		items = append(items, item)
	}
	return items, links, nil
}

// ItemFrom projects assertions into the legacy item shape.
func (p *CompositeParser) ItemFrom(sourceURL string, assertions []provenance.Assertion) *types.Item {
	return ItemFromAssertions(sourceURL, assertions)
}

// Derive runs every sub-parser and returns their combined assertions.
//
// Later assertions win on a field collision, which is the order the merged item
// used to resolve in: CSS rules first, then regex, then XPath, then what the page
// declared about itself. That ordering is now visible here rather than implied by
// the sequence of map merges it used to come out of.
func (p *CompositeParser) Derive(resp *types.Response, rules []config.ParseRule) ([]provenance.Assertion, []string, error) {
	var all []provenance.Assertion
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
	cssAssertions, links, err := p.css.Derive(resp, cssRules)
	if err != nil {
		p.logger.Warn("CSS parser error", "error", err)
	}
	all = append(all, cssAssertions...)
	allLinks = append(allLinks, links...)

	// Regex parsing
	if len(regexRules) > 0 {
		regexAssertions, _, err := p.regex.Derive(resp, regexRules)
		if err != nil {
			p.logger.Warn("regex parser error", "error", err)
		}
		all = append(all, regexAssertions...)
	}

	// XPath parsing
	if len(xpathRules) > 0 {
		xpathAssertions, _, err := p.xpath.Derive(resp, xpathRules)
		if err != nil {
			p.logger.Warn("XPath parser error", "error", err)
		}
		all = append(all, xpathAssertions...)
	}

	// Auto-extract structured data (JSON-LD, OpenGraph, etc.)
	sdResults, err := p.structured.Extract(resp)
	if err != nil {
		p.logger.Warn("structured data extraction error", "error", err)
	}
	all = append(all, StructuredDataToAssertions(sdResults)...)

	return dropShadowed(all), allLinks, nil
}

// dropShadowed keeps only the last assertion set for any field that more than one
// derivation claimed.
//
// Two parsers naming the same field is a configuration the merged item resolved
// silently by overwriting. Keeping both assertions instead would look like a
// multi-valued field to the projection and turn a shadowed scalar into a list — a
// change in output type produced by nothing the page did.
func dropShadowed(all []provenance.Assertion) []provenance.Assertion {
	lastMethod := map[string]string{}
	for _, a := range all {
		lastMethod[a.Field] = a.Method
	}
	out := all[:0:0]
	for _, a := range all {
		if lastMethod[a.Field] == a.Method {
			out = append(out, a)
		}
	}
	return out
}
