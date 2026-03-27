package parser

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// AutoSelectorGenerator automatically generates CSS selectors for elements.
type AutoSelectorGenerator struct {
	logger *slog.Logger
}

// NewAutoSelectorGenerator creates a new auto-selector generator.
func NewAutoSelectorGenerator(logger *slog.Logger) *AutoSelectorGenerator {
	return &AutoSelectorGenerator{
		logger: logger.With("component", "auto_selector"),
	}
}

// SelectorCandidate represents a generated selector with a confidence score.
type SelectorCandidate struct {
	Selector    string  `json:"selector"`
	Specificity int     `json:"specificity"` // Higher = more specific
	MatchCount  int     `json:"match_count"` // How many elements this matches
	Score       float64 `json:"score"`       // Confidence score (0-1)
}

// GenerateForText finds elements containing the given text and generates selectors for them.
func (asg *AutoSelectorGenerator) GenerateForText(resp *types.Response, text string) ([]SelectorCandidate, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, err
	}

	var candidates []SelectorCandidate

	doc.Find("*").Each(func(i int, sel *goquery.Selection) {
		nodeText := strings.TrimSpace(sel.Text())
		if nodeText == "" || !strings.Contains(nodeText, text) {
			return
		}

		// Only match leaf-ish elements (avoid matching on <body>, <html>)
		if sel.Children().Length() > 5 {
			return
		}

		selectors := generateSelectorsForElement(sel)
		for _, s := range selectors {
			count := doc.Find(s.Selector).Length()
			s.MatchCount = count
			switch {
			case count == 1:
				s.Score = 1.0
			case count <= 3:
				s.Score = 0.8
			case count <= 10:
				s.Score = 0.5
			default:
				s.Score = 0.2
			}
			candidates = append(candidates, s)
		}
	})

	// Sort by score (best first)
	sortCandidates(candidates)

	// Limit to top 10
	if len(candidates) > 10 {
		candidates = candidates[:10]
	}

	return candidates, nil
}

// GenerateForElement generates selectors for a specific element matched by a basic selector.
func (asg *AutoSelectorGenerator) GenerateForElement(resp *types.Response, basicSelector string) ([]SelectorCandidate, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, err
	}

	var candidates []SelectorCandidate

	doc.Find(basicSelector).First().Each(func(i int, sel *goquery.Selection) {
		selectors := generateSelectorsForElement(sel)
		for _, s := range selectors {
			count := doc.Find(s.Selector).Length()
			s.MatchCount = count
			switch {
			case count == 1:
				s.Score = 1.0
			case count <= 3:
				s.Score = 0.8
			default:
				s.Score = float64(1) / float64(count)
			}
			candidates = append(candidates, s)
		}
	})

	sortCandidates(candidates)
	return candidates, nil
}

// generateSelectorsForElement creates multiple selector strategies for an element.
func generateSelectorsForElement(sel *goquery.Selection) []SelectorCandidate {
	var candidates []SelectorCandidate
	tag := goquery.NodeName(sel)

	// 1. ID selector (most specific)
	if id, exists := sel.Attr("id"); exists && id != "" {
		candidates = append(candidates, SelectorCandidate{
			Selector:    "#" + cssEscape(id),
			Specificity: 100,
		})
	}

	// 2. Class-based selector
	if class, exists := sel.Attr("class"); exists && class != "" {
		classes := strings.Fields(class)
		if len(classes) > 0 {
			// Single most specific class
			for _, c := range classes {
				candidates = append(candidates, SelectorCandidate{
					Selector:    tag + "." + cssEscape(c),
					Specificity: 20,
				})
			}
			// All classes combined
			if len(classes) > 1 {
				combined := tag
				for _, c := range classes {
					combined += "." + cssEscape(c)
				}
				candidates = append(candidates, SelectorCandidate{
					Selector:    combined,
					Specificity: 10 + len(classes)*10,
				})
			}
		}
	}

	// 3. Data attribute selectors
	for _, attr := range []string{"data-testid", "data-id", "data-name", "data-type", "role", "aria-label", "name"} {
		if val, exists := sel.Attr(attr); exists && val != "" {
			candidates = append(candidates, SelectorCandidate{
				Selector:    fmt.Sprintf(`%s[%s="%s"]`, tag, attr, val),
				Specificity: 50,
			})
		}
	}

	// 4. Path-based selector (parent > child chain)
	path := buildElementPath(sel, 3)
	if path != "" {
		candidates = append(candidates, SelectorCandidate{
			Selector:    path,
			Specificity: 30,
		})
	}

	// 5. nth-child selector
	parent := sel.Parent()
	if parent.Length() > 0 {
		idx := sel.Index() + 1
		parentTag := goquery.NodeName(parent)
		if parentTag != "" && parentTag != "html" && parentTag != "body" {
			candidates = append(candidates, SelectorCandidate{
				Selector:    fmt.Sprintf("%s > %s:nth-child(%d)", parentTag, tag, idx),
				Specificity: 25,
			})
		}
	}

	return candidates
}

// buildElementPath constructs a CSS path from ancestors.
func buildElementPath(sel *goquery.Selection, maxDepth int) string {
	var parts []string
	current := sel

	for i := 0; i < maxDepth; i++ {
		tag := goquery.NodeName(current)
		if tag == "" || tag == "html" || tag == "body" {
			break
		}

		part := tag
		if id, exists := current.Attr("id"); exists && id != "" {
			part = "#" + cssEscape(id)
			parts = append([]string{part}, parts...)
			break
		}
		if class, exists := current.Attr("class"); exists && class != "" {
			classes := strings.Fields(class)
			if len(classes) > 0 {
				part += "." + cssEscape(classes[0])
			}
		}

		parts = append([]string{part}, parts...)
		current = current.Parent()
	}

	return strings.Join(parts, " > ")
}

// cssEscape escapes special characters in CSS selectors.
func cssEscape(s string) string {
	replacer := strings.NewReplacer(
		":", `\:`,
		".", `\.`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"/", `\/`,
		" ", `\ `,
	)
	return replacer.Replace(s)
}

// sortCandidates sorts by score descending, then specificity descending.
func sortCandidates(candidates []SelectorCandidate) {
	for i := 1; i < len(candidates); i++ {
		key := candidates[i]
		j := i - 1
		for j >= 0 && (candidates[j].Score < key.Score ||
			(candidates[j].Score == key.Score && candidates[j].Specificity < key.Specificity)) {
			candidates[j+1] = candidates[j]
			j--
		}
		candidates[j+1] = key
	}
}
