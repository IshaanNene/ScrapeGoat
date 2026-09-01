package parser

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// RegexParser extracts data using regular expressions.
type RegexParser struct {
	logger *slog.Logger
	cache  map[string]*regexp.Regexp
}

// NewRegexParser creates a new regex parser.
func NewRegexParser(logger *slog.Logger) *RegexParser {
	return &RegexParser{
		logger: logger.With("component", "regex_parser"),
		cache:  make(map[string]*regexp.Regexp),
	}
}

// Parse implements Parser for regex rules, as a view over Derive.
func (p *RegexParser) Parse(resp *types.Response, rules []config.ParseRule) ([]*types.Item, []string, error) {
	assertions, _, err := p.Derive(resp, rules)
	var items []*types.Item
	if item := ItemFromAssertions(resp.Request.URLString(), assertions); item != nil {
		items = append(items, item)
	}
	// A bad pattern is reported but does not discard what the good ones matched,
	// which is what this returned before and what a partial rule set deserves.
	return items, nil, err
}

// Derive returns one assertion per regex match.
func (p *RegexParser) Derive(resp *types.Response, rules []config.ParseRule) ([]provenance.Assertion, []string, error) {
	body := string(resp.Body)
	var assertions []provenance.Assertion
	var errs []string

	for _, rule := range rules {
		if rule.Type != "regex" {
			continue
		}

		re, err := p.getOrCompile(rule.Pattern)
		if err != nil {
			errs = append(errs, fmt.Sprintf("rule %q: %v", rule.Name, err))
			continue
		}

		assertions = append(assertions,
			valueAssertions(rule.Name, "regex:"+rule.Pattern, regexVersion, p.extractRegex(re, body))...)
	}

	var retErr error
	if len(errs) > 0 {
		retErr = &types.ParseError{
			URL: resp.Request.URLString(),
			Err: fmt.Errorf("regex errors: %s", strings.Join(errs, "; ")),
		}
	}

	return assertions, nil, retErr
}

// extractRegex applies a compiled regex to the body and returns matches.
func (p *RegexParser) extractRegex(re *regexp.Regexp, body string) []string {
	var values []string

	// Check for named capture groups
	names := re.SubexpNames()
	hasNamedGroups := false
	for _, name := range names {
		if name != "" {
			hasNamedGroups = true
			break
		}
	}

	if hasNamedGroups {
		// Named capture groups: return named group values
		matches := re.FindAllStringSubmatch(body, -1)
		for _, match := range matches {
			for i, name := range names {
				if name != "" && i < len(match) && match[i] != "" {
					values = append(values, match[i])
				}
			}
		}
	} else if re.NumSubexp() > 0 {
		// Unnamed capture groups: return first group
		matches := re.FindAllStringSubmatch(body, -1)
		for _, match := range matches {
			if len(match) > 1 {
				values = append(values, match[1])
			}
		}
	} else {
		// No capture groups: return full matches
		values = re.FindAllString(body, -1)
	}

	return values
}

// getOrCompile returns a cached compiled regex or compiles and caches a new one.
func (p *RegexParser) getOrCompile(pattern string) (*regexp.Regexp, error) {
	if re, ok := p.cache[pattern]; ok {
		return re, nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex %q: %w", pattern, err)
	}

	p.cache[pattern] = re
	return re, nil
}
