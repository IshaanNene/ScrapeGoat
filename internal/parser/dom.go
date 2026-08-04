package parser

import (
	"log/slog"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// DOMTraverser provides parent/child/sibling DOM navigation.
type DOMTraverser struct {
	logger *slog.Logger
}

// NewDOMTraverser creates a new DOM traversal helper.
func NewDOMTraverser(logger *slog.Logger) *DOMTraverser {
	return &DOMTraverser{
		logger: logger.With("component", "dom_traverser"),
	}
}

// TraversalResult holds the result of a DOM traversal operation.
type TraversalResult struct {
	Text      string
	HTML      string
	Attribute string
	Tag       string
	Children  []TraversalResult
}

// FindParent navigates to the parent element of matches.
func (dt *DOMTraverser) FindParent(resp *types.Response, selector string, levels int) ([]TraversalResult, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, err
	}

	var results []TraversalResult
	doc.Find(selector).Each(func(i int, sel *goquery.Selection) {
		parent := sel
		for j := 0; j < levels; j++ {
			parent = parent.Parent()
		}
		results = append(results, selectionToResult(parent))
	})

	return results, nil
}

// FindChildren navigates to direct children of matched elements.
func (dt *DOMTraverser) FindChildren(resp *types.Response, selector, childSelector string) ([]TraversalResult, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, err
	}

	var results []TraversalResult
	doc.Find(selector).Each(func(i int, sel *goquery.Selection) {
		var children []TraversalResult
		if childSelector != "" {
			sel.Find(childSelector).Each(func(j int, child *goquery.Selection) {
				children = append(children, selectionToResult(child))
			})
		} else {
			sel.Children().Each(func(j int, child *goquery.Selection) {
				children = append(children, selectionToResult(child))
			})
		}
		result := selectionToResult(sel)
		result.Children = children
		results = append(results, result)
	})

	return results, nil
}

// FindSiblings finds sibling elements of matched elements.
func (dt *DOMTraverser) FindSiblings(resp *types.Response, selector string, direction string) ([]TraversalResult, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, err
	}

	var results []TraversalResult
	doc.Find(selector).Each(func(i int, sel *goquery.Selection) {
		var siblings *goquery.Selection
		switch direction {
		case "next":
			siblings = sel.Next()
		case "prev":
			siblings = sel.Prev()
		case "all-next":
			siblings = sel.NextAll()
		case "all-prev":
			siblings = sel.PrevAll()
		default:
			siblings = sel.Siblings()
		}
		siblings.Each(func(j int, sib *goquery.Selection) {
			results = append(results, selectionToResult(sib))
		})
	})

	return results, nil
}

// FindClosest traverses up the tree finding the first ancestor matching the selector.
func (dt *DOMTraverser) FindClosest(resp *types.Response, startSelector, ancestorSelector string) ([]TraversalResult, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, err
	}

	var results []TraversalResult
	doc.Find(startSelector).Each(func(i int, sel *goquery.Selection) {
		closest := sel.Closest(ancestorSelector)
		if closest.Length() > 0 {
			results = append(results, selectionToResult(closest))
		}
	})

	return results, nil
}

// ExtractTable parses an HTML table into a 2D string array.
func (dt *DOMTraverser) ExtractTable(resp *types.Response, tableSelector string) ([][]string, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, err
	}

	var table [][]string

	doc.Find(tableSelector).First().Find("tr").Each(func(i int, row *goquery.Selection) {
		var cells []string
		row.Find("td, th").Each(func(j int, cell *goquery.Selection) {
			cells = append(cells, strings.TrimSpace(cell.Text()))
		})
		if len(cells) > 0 {
			table = append(table, cells)
		}
	})

	return table, nil
}

// ExtractList extracts list items (li) from a list (ul/ol).
func (dt *DOMTraverser) ExtractList(resp *types.Response, listSelector string) ([]string, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, err
	}

	var items []string
	doc.Find(listSelector).Find("li").Each(func(i int, sel *goquery.Selection) {
		items = append(items, strings.TrimSpace(sel.Text()))
	})

	return items, nil
}

func selectionToResult(sel *goquery.Selection) TraversalResult {
	tag := goquery.NodeName(sel)
	text := strings.TrimSpace(sel.Text())
	html, _ := sel.Html()

	return TraversalResult{
		Text: text,
		HTML: html,
		Tag:  tag,
	}
}
