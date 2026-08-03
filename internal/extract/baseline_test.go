package extract

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Baselines.
//
// Measured before any new extractor exists, so "it got better" has something to be
// better than. Two of these are deliberately trivial — they exist to make the
// scores interpretable rather than to compete.

// wholeBodyExtractor returns every word in <body>.
//
// The floor for precision and the ceiling for recall. Any extractor scoring below
// this on F1 is worse than doing nothing at all, and its recall number should
// always be read against this one: perfect recall is free.
type wholeBodyExtractor struct{}

func (wholeBodyExtractor) Name() string { return "whole-body (no extraction)" }

func (wholeBodyExtractor) Extract(html string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}
	return normalise(doc.Find("body").Text()), nil
}

// paragraphExtractor returns the text of every <p>.
//
// The obvious first idea, and a surprisingly strong baseline on prose pages —
// which is exactly why it is here. A new extractor that cannot beat "concatenate
// the paragraphs" has not earned its complexity.
type paragraphExtractor struct{}

func (paragraphExtractor) Name() string { return "all <p> tags" }

func (paragraphExtractor) Extract(html string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}

	var parts []string
	doc.Find("p").Each(func(_ int, s *goquery.Selection) {
		if t := normalise(s.Text()); t != "" {
			parts = append(parts, t)
		}
	})
	return strings.Join(parts, " "), nil
}

// selectorExtractor reproduces the extraction ScrapeGoat shipped in v0.1.0:
// a fixed list of CSS selectors tried in order.
//
// This is the incumbent, and the number to beat. Its shape is the argument for
// replacing it — the list works when a site happens to use one of these class
// names and returns nothing useful otherwise, with no signal to the caller about
// which case they are in.
type selectorExtractor struct{}

func (selectorExtractor) Name() string { return "selector list (v0.1.0)" }

func (selectorExtractor) Extract(html string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return "", err
	}

	selectors := []string{
		"article",
		"[itemtype*='Article']",
		"[itemtype*='NewsArticle']",
		"[itemtype*='BlogPosting']",
		".post",
		".article",
		".blog-post",
		".entry-content",
	}

	for _, sel := range selectors {
		if node := doc.Find(sel).First(); node.Length() > 0 {
			if t := normalise(node.Text()); t != "" {
				return t, nil
			}
		}
	}

	// No selector matched. The shipped version fell through to returning nothing
	// for the article, which is the silent failure this whole package exists to
	// fix.
	return "", nil
}

// densityExtractor is this package's extractor, under test.
type densityExtractor struct{}

func (densityExtractor) Name() string { return "density scoring (this package)" }

func (densityExtractor) Extract(html string) (string, error) {
	res, err := New().FromHTML(html)
	if err != nil {
		return "", err
	}
	return res.Text, nil
}
