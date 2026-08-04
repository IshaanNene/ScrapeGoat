package extract

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

// Tuning constants.
//
// Each is a threshold with a cost on both sides, so each says what it trades.
// They were set by running the benchmark, not by intuition — see
// docs/EXTRACTION.md.
const (
	// minTextLen is the shortest text a node can hold and still be scored as
	// prose. Below this it is a caption, a byline, or a button label.
	minTextLen = 25

	// maxLinkDensity is the link-text-to-total-text ratio above which a node is
	// navigation rather than content. Real prose contains links; a nav bar is
	// almost entirely links.
	maxLinkDensity = 0.5

	// lengthScoreCap bounds the reward for sheer size, so one enormous block —
	// a comment thread, say — cannot outscore the article on volume alone.
	lengthScoreCap = 3.0

	// siblingScoreRatio is how good a sibling must be, relative to the winner, to
	// be included with it. Articles are often split across several containers by
	// ad insertion, so taking only the single best node loses real text.
	siblingScoreRatio = 0.2

	// uniformityPenalty is applied to containers whose children are many, short,
	// and similar in length — the shape of a comment thread or a link list, not
	// of an article.
	uniformityPenalty = 0.35

	// uniformChildMin is how many similar children it takes before the shape is
	// evidence rather than coincidence.
	uniformChildMin = 4

	// headingBoost multiplies a candidate that contains the page's main heading.
	//
	// This is the signal that separates an article from a comment thread when both
	// are prose and the thread is longer. A page has one <h1> and it titles the
	// main content — that is what the element is for, so relying on it is reading
	// the document's own structure rather than guessing at class names the way the
	// old selector list did.
	headingBoost = 4.0

	// minConfidence is the score share below which the result is reported as
	// content-free rather than returned. A page that is entirely navigation has no
	// article, and saying so beats confidently returning the menu.
	minConfidence = 0.10
)

// Result is an extraction with the signals behind it.
//
// Confidence is returned rather than kept private because a downstream corpus
// builder needs to filter: an extractor that silently returns navigation is worse
// than one that says it is unsure. The previous selector-based extractor had no
// way to express doubt, so a page it could not handle looked identical to a page
// with no content.
type Result struct {
	// Text is the extracted main content.
	Text string

	// Title is the page's heading, if one was found within the content.
	Title string

	// Confidence is a rough 0..1 signal, from the winning node's score relative
	// to the rest of the page. Not a probability — a ranking aid.
	Confidence float64

	// LinkDensity of the selected content. High values suggest a mis-selection.
	LinkDensity float64

	// Blocks is how many separate nodes were merged into the result.
	Blocks int
}

// Extractor pulls main content out of HTML.
type Extractor struct {
	// MinTextLen overrides the default minimum block length.
	MinTextLen int
}

// New returns an Extractor with default tuning.
func New() *Extractor { return &Extractor{MinTextLen: minTextLen} }

// FromHTML extracts main content from an HTML document.
func (e *Extractor) FromHTML(htmlStr string) (*Result, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlStr))
	if err != nil {
		return nil, err
	}
	return e.FromDocument(doc), nil
}

// FromDocument extracts main content from an already-parsed document.
func (e *Extractor) FromDocument(doc *goquery.Document) *Result {
	e.strip(doc)

	scores := e.scoreCandidates(doc)
	if len(scores) == 0 {
		return &Result{}
	}

	var bestNode *goquery.Selection
	bestScore := 0.0
	for node, s := range scores {
		if s > bestScore {
			bestScore, bestNode = s, node
		}
	}
	if bestNode == nil {
		return &Result{}
	}

	// Total score across candidates, for a relative confidence signal.
	total := 0.0
	for _, s := range scores {
		total += s
	}

	blocks := []*goquery.Selection{bestNode}
	text := []string{cleanBlock(bestNode)}

	// Include siblings that score comparably. An article interrupted by an inline
	// ad or a pull-quote becomes several containers, and taking only the best one
	// silently truncates it.
	threshold := bestScore * siblingScoreRatio
	bestNode.Siblings().Each(func(_ int, sib *goquery.Selection) {
		if s, ok := scores[sib]; ok && s >= threshold {
			if t := cleanBlock(sib); t != "" {
				blocks = append(blocks, sib)
				text = append(text, t)
			}
		}
	})

	joined := normalise(strings.Join(text, " "))

	confidence := 0.0
	if total > 0 {
		confidence = bestScore / total
	}

	density := linkDensity(bestNode)

	// Refuse rather than guess.
	//
	// Plenty of pages have no article: tag indexes, link directories, category
	// listings, pagination stubs. Returning the best-scoring block anyway means a
	// corpus quietly fills with navigation, and nothing downstream can tell that
	// from real content. An empty result with a reason is more useful than a
	// confident wrong one — which is exactly what the old selector extractor could
	// not express.
	if density > maxLinkDensity || confidence < minConfidence {
		return &Result{
			Confidence:  confidence,
			LinkDensity: density,
		}
	}

	return &Result{
		Text:        joined,
		Title:       findTitle(doc, bestNode),
		Confidence:  confidence,
		LinkDensity: density,
		Blocks:      len(blocks),
	}
}

// strip removes nodes that are never content.
//
// Done before scoring rather than after: a <script> body is text as far as the
// DOM is concerned, and leaving it in inflates the score of whatever contains it.
func (e *Extractor) strip(doc *goquery.Document) {
	doc.Find("script, style, noscript, svg, iframe, form, button, template").Remove()

	// Structural elements that are boilerplate by definition. Unlike class-name
	// matching, these are specified by HTML itself, so trusting them is not the
	// same guess the selector list was making.
	doc.Find("nav, aside, footer, header").Remove()
}

// scoreCandidates scores every block-level node that could hold content.
//
// The scoring is readability-style: each paragraph contributes to its parent and,
// at half weight, to its grandparent — because the container that holds the most
// prose is usually the article, and its parent is usually the page wrapper.
func (e *Extractor) scoreCandidates(doc *goquery.Document) map[*goquery.Selection]float64 {
	minLen := e.MinTextLen
	if minLen <= 0 {
		minLen = minTextLen
	}

	// The page's main heading, used below to identify the article's container.
	var mainHeading *html.Node
	if h := doc.Find("h1").First(); h.Length() > 0 && len(h.Nodes) > 0 {
		mainHeading = h.Nodes[0]
	}

	// Keyed by the underlying node so two selections over the same element
	// accumulate together rather than competing.
	byNode := map[*html.Node]float64{}
	selFor := map[*html.Node]*goquery.Selection{}

	doc.Find("p, pre, blockquote, li, td").Each(func(_ int, s *goquery.Selection) {
		text := flattenText(s.Clone())
		if len(text) < minLen {
			return
		}

		score := paragraphScore(text)

		parent := s.Parent()
		if parent.Length() == 0 || len(parent.Nodes) == 0 {
			return
		}
		pn := parent.Nodes[0]
		byNode[pn] += score
		selFor[pn] = parent

		if gp := parent.Parent(); gp.Length() > 0 && len(gp.Nodes) > 0 {
			gn := gp.Nodes[0]
			byNode[gn] += score / 2
			selFor[gn] = gp
		}
	})

	out := make(map[*goquery.Selection]float64, len(byNode))
	for node, raw := range byNode {
		sel := selFor[node]

		// Link density: a container that is mostly anchor text is a menu.
		adjusted := raw * (1 - linkDensity(sel))

		// Uniformity: many similar short children is the shape of a comment
		// thread or a card grid. Article paragraphs vary in length; comments
		// cluster around the same short length.
		if isUniformlyShort(sel) {
			adjusted *= uniformityPenalty
		}

		// Contains the page's main heading: this is the article. Applied last so
		// it can rescue a short article that the length-based scoring would
		// otherwise lose to a long comment thread.
		if mainHeading != nil && containsNode(sel, mainHeading) {
			adjusted *= headingBoost
		}

		if adjusted > 0 {
			out[sel] = adjusted
		}
	}
	return out
}

// paragraphScore rates a single block of prose.
//
// Commas stand in for syntactic complexity — the cheapest available proxy for
// "this is a written sentence rather than a label". Length is capped so that one
// very long block cannot dominate on size alone.
func paragraphScore(text string) float64 {
	score := 1.0
	score += float64(strings.Count(text, ","))
	score += float64(strings.Count(text, "."))

	if l := float64(len(text)) / 100; l > lengthScoreCap {
		score += lengthScoreCap
	} else {
		score += l
	}
	return score
}

// linkDensity is the fraction of a node's text that sits inside anchors.
func linkDensity(s *goquery.Selection) float64 {
	total := len(normalise(s.Text()))
	if total == 0 {
		return 0
	}

	linked := 0
	s.Find("a").Each(func(_ int, a *goquery.Selection) {
		linked += len(normalise(a.Text()))
	})

	d := float64(linked) / float64(total)
	if d > 1 {
		return 1
	}
	return d
}

// isUniformlyShort reports whether a node's children look like a repeated list of
// similar short items rather than an article's paragraphs.
//
// This is the signal that separates a comment thread from a post, and it is the
// only one that does: comments are prose, so link density does not help, and there
// are many of them, so raw accumulated score favours them.
func isUniformlyShort(s *goquery.Selection) bool {
	var lengths []int
	s.Children().Each(func(_ int, c *goquery.Selection) {
		if t := normalise(c.Text()); t != "" {
			lengths = append(lengths, len(t))
		}
	})

	if len(lengths) < uniformChildMin {
		return false
	}

	sum := 0
	for _, l := range lengths {
		sum += l
	}
	meanLen := float64(sum) / float64(len(lengths))

	// Article paragraphs run well past 200 characters; comments and list items
	// rarely do.
	if meanLen > 200 {
		return false
	}

	// Low spread means the children are the same kind of thing repeated.
	variance := 0.0
	for _, l := range lengths {
		d := float64(l) - meanLen
		variance += d * d
	}
	stddev := 0.0
	if len(lengths) > 1 {
		stddev = sqrt(variance / float64(len(lengths)))
	}

	return meanLen == 0 || stddev/meanLen < 1.0
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	// Newton's method; avoids importing math for one call in a hot-ish path.
	z := x
	for i := 0; i < 12; i++ {
		z -= (z*z - x) / (2 * z)
	}
	return z
}

// blockElements end a run of text. Anything here gets a space after it before the
// tree is flattened; see cleanBlock.
const blockElements = "address, article, aside, blockquote, br, dd, div, dl, dt, " +
	"fieldset, figcaption, figure, footer, form, h1, h2, h3, h4, h5, h6, header, " +
	"hr, li, main, nav, ol, p, pre, section, table, td, th, tr, ul"

// cleanBlock renders a node's prose, dropping structure that is not content.
func cleanBlock(s *goquery.Selection) string {
	clone := s.Clone()
	clone.Find("nav, aside, footer, header, form, button, figure figcaption").Remove()
	return flattenText(clone)
}

// flattenText renders a subtree as prose with block boundaries preserved.
//
// goquery's Text() concatenates descendant text nodes with nothing between them,
// so <h1>Reserved</h1><p>This page…</p> comes out as "ReservedThis page…". In a
// corpus that is worse than ugly: the join fabricates a token that appears in no
// document, and every downstream tokeniser, language detector, and dedupe hash
// inherits it. Inserting a space after each block element costs one pass and the
// extra whitespace is collapsed by normalise anyway.
func flattenText(s *goquery.Selection) string {
	s.Find(blockElements).AfterHtml(" ")
	return normalise(s.Text())
}

// findTitle looks for the heading belonging to the extracted content, preferring
// one inside it over the document <title>, which usually carries site branding.
func findTitle(doc *goquery.Document, content *goquery.Selection) string {
	if h := content.Find("h1").First(); h.Length() > 0 {
		if t := normalise(h.Text()); t != "" {
			return t
		}
	}
	if h := doc.Find("h1").First(); h.Length() > 0 {
		if t := normalise(h.Text()); t != "" {
			return t
		}
	}
	return normalise(doc.Find("title").First().Text())
}

// normalise collapses whitespace.
func normalise(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// containsNode reports whether target is sel or a descendant of it.
func containsNode(sel *goquery.Selection, target *html.Node) bool {
	if len(sel.Nodes) == 0 || target == nil {
		return false
	}
	root := sel.Nodes[0]
	for n := target; n != nil; n = n.Parent {
		if n == root {
			return true
		}
	}
	return false
}
