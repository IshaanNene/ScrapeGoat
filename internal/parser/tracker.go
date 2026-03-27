package parser

import (
	"log/slog"
	"math"
	"strings"

	"github.com/PuerkitoBio/goquery"

	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// SmartTracker tracks elements across page changes using multiple strategies.
// It can relocate content even when a site's HTML structure changes.
type SmartTracker struct {
	logger    *slog.Logger
	snapshots map[string]*elementSnapshot
}

// elementSnapshot stores attributes of an element for later matching.
type elementSnapshot struct {
	Tag        string
	ID         string
	Classes    []string
	Text       string
	Attributes map[string]string
	Path       string // CSS path from root
	Selector   string // Original CSS selector
	TextHash   string // Hash of text content
}

// SimilarElement represents an element found via similarity matching.
type SimilarElement struct {
	Selector   string  `json:"selector"`
	Text       string  `json:"text"`
	Similarity float64 `json:"similarity"` // 0 to 1
	Tag        string  `json:"tag"`
}

// NewSmartTracker creates a new smart element tracker.
func NewSmartTracker(logger *slog.Logger) *SmartTracker {
	return &SmartTracker{
		logger:    logger.With("component", "smart_tracker"),
		snapshots: make(map[string]*elementSnapshot),
	}
}

// TakeSnapshot saves the current state of an element for later relocation.
func (st *SmartTracker) TakeSnapshot(resp *types.Response, selector string, name string) error {
	doc, err := resp.Document()
	if err != nil {
		return err
	}

	sel := doc.Find(selector).First()
	if sel.Length() == 0 {
		return &types.ParseError{
			URL:      resp.Request.URLString(),
			Selector: selector,
			Err:      types.ErrEmptyResponse,
		}
	}

	snap := &elementSnapshot{
		Tag:        goquery.NodeName(sel),
		Text:       strings.TrimSpace(sel.Text()),
		Classes:    strings.Fields(attrOr(sel, "class")),
		ID:         attrOr(sel, "id"),
		Attributes: extractAttributes(sel),
		Path:       buildElementPath(sel, 5),
		Selector:   selector,
	}

	st.snapshots[name] = snap
	st.logger.Debug("snapshot taken", "name", name, "selector", selector, "tag", snap.Tag)
	return nil
}

// Relocate tries to find a previously snapshotted element on a new page.
// It uses multiple strategies: original selector, ID, class, text content, path.
func (st *SmartTracker) Relocate(resp *types.Response, name string) (string, *goquery.Selection, error) {
	snap, ok := st.snapshots[name]
	if !ok {
		return "", nil, &types.ParseError{
			URL: resp.Request.URLString(),
			Err: types.ErrInvalidURL,
		}
	}

	doc, err := resp.Document()
	if err != nil {
		return "", nil, err
	}

	// Strategy 1: Try original selector
	sel := doc.Find(snap.Selector)
	if sel.Length() == 1 {
		st.logger.Debug("relocated via original selector", "name", name)
		return snap.Selector, sel, nil
	}

	// Strategy 2: Try ID
	if snap.ID != "" {
		selector := "#" + cssEscape(snap.ID)
		sel = doc.Find(selector)
		if sel.Length() == 1 {
			st.logger.Debug("relocated via ID", "name", name, "id", snap.ID)
			return selector, sel, nil
		}
	}

	// Strategy 3: Try data attributes
	for attr, val := range snap.Attributes {
		if strings.HasPrefix(attr, "data-") || attr == "name" || attr == "aria-label" {
			selector := snap.Tag + `[` + attr + `="` + val + `"]`
			sel = doc.Find(selector)
			if sel.Length() == 1 {
				st.logger.Debug("relocated via data attribute", "name", name, "attr", attr)
				return selector, sel, nil
			}
		}
	}

	// Strategy 4: Try class combination
	if len(snap.Classes) > 0 {
		selector := snap.Tag
		for _, c := range snap.Classes {
			selector += "." + cssEscape(c)
		}
		sel = doc.Find(selector)
		if sel.Length() == 1 {
			st.logger.Debug("relocated via classes", "name", name)
			return selector, sel, nil
		}
	}

	// Strategy 5: Try path-based selector
	if snap.Path != "" {
		sel = doc.Find(snap.Path)
		if sel.Length() == 1 {
			st.logger.Debug("relocated via path", "name", name)
			return snap.Path, sel, nil
		}
	}

	// Strategy 6: Text content similarity search
	if snap.Text != "" {
		bestMatch, bestSel, bestScore := st.findByTextSimilarity(doc, snap)
		if bestScore > 0.7 {
			st.logger.Debug("relocated via text similarity",
				"name", name, "score", bestScore, "selector", bestMatch)
			return bestMatch, bestSel, nil
		}
	}

	return "", nil, &types.ParseError{
		URL:      resp.Request.URLString(),
		Selector: snap.Selector,
		Err:      types.ErrEmptyResponse,
	}
}

// FindSimilar finds elements with similar structure/content to a given element.
func (st *SmartTracker) FindSimilar(resp *types.Response, selector string, maxResults int) ([]SimilarElement, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, err
	}

	reference := doc.Find(selector).First()
	if reference.Length() == 0 {
		return nil, nil
	}

	refTag := goquery.NodeName(reference)
	refClasses := strings.Fields(attrOr(reference, "class", ""))
	refText := strings.TrimSpace(reference.Text())
	refAttrs := extractAttributes(reference)

	var results []SimilarElement

	doc.Find(refTag).Each(func(i int, sel *goquery.Selection) {
		if sel.IsSelection(reference) {
			return // Skip the reference element itself
		}

		score := computeSimilarity(sel, refTag, refClasses, refText, refAttrs)
		if score > 0.3 {
			results = append(results, SimilarElement{
				Selector:   buildElementPath(sel, 3),
				Text:       truncate(strings.TrimSpace(sel.Text()), 100),
				Similarity: score,
				Tag:        refTag,
			})
		}
	})

	// Sort by similarity descending
	for i := 1; i < len(results); i++ {
		key := results[i]
		j := i - 1
		for j >= 0 && results[j].Similarity < key.Similarity {
			results[j+1] = results[j]
			j--
		}
		results[j+1] = key
	}

	if len(results) > maxResults {
		results = results[:maxResults]
	}

	return results, nil
}

// findByTextSimilarity searches for elements with similar text content.
func (st *SmartTracker) findByTextSimilarity(doc *goquery.Document, snap *elementSnapshot) (string, *goquery.Selection, float64) {
	var bestSelector string
	var bestSel *goquery.Selection
	bestScore := 0.0

	doc.Find(snap.Tag).Each(func(i int, sel *goquery.Selection) {
		text := strings.TrimSpace(sel.Text())
		score := textSimilarity(snap.Text, text)
		if score > bestScore {
			bestScore = score
			bestSel = sel
			bestSelector = buildElementPath(sel, 3)
		}
	})

	return bestSelector, bestSel, bestScore
}

// computeSimilarity calculates how similar two elements are.
func computeSimilarity(sel *goquery.Selection, refTag string, refClasses []string, refText string, refAttrs map[string]string) float64 {
	var scores []float64

	// Tag match (required)
	if goquery.NodeName(sel) != refTag {
		return 0
	}

	// Class overlap
	classes := strings.Fields(attrOr(sel, "class"))
	classOverlap := setOverlap(refClasses, classes)
	scores = append(scores, classOverlap*0.3)

	// Text similarity
	text := strings.TrimSpace(sel.Text())
	textSim := textSimilarity(refText, text)
	scores = append(scores, textSim*0.3)

	// Attribute overlap
	attrs := extractAttributes(sel)
	attrOverlap := mapOverlap(refAttrs, attrs)
	scores = append(scores, attrOverlap*0.2)

	// Structure similarity (children count)
	refChildren := 0
	selChildren := sel.Children().Length()
	if refChildren > 0 || selChildren > 0 {
		structSim := 1.0 - math.Abs(float64(refChildren-selChildren))/math.Max(float64(refChildren), float64(selChildren))
		scores = append(scores, structSim*0.2)
	}

	var total float64
	for _, s := range scores {
		total += s
	}
	return total
}

// textSimilarity computes a simple similarity between two strings.
func textSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	if a == "" || b == "" {
		return 0.0
	}

	aWords := strings.Fields(strings.ToLower(a))
	bWords := strings.Fields(strings.ToLower(b))

	if len(aWords) == 0 || len(bWords) == 0 {
		return 0.0
	}

	// Jaccard similarity on words
	aSet := make(map[string]bool, len(aWords))
	for _, w := range aWords {
		aSet[w] = true
	}
	bSet := make(map[string]bool, len(bWords))
	for _, w := range bWords {
		bSet[w] = true
	}

	intersection := 0
	for w := range aSet {
		if bSet[w] {
			intersection++
		}
	}

	union := len(aSet) + len(bSet) - intersection
	if union == 0 {
		return 0.0
	}

	return float64(intersection) / float64(union)
}

// setOverlap computes the Jaccard similarity of two string sets.
func setOverlap(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}

	aSet := make(map[string]bool, len(a))
	for _, s := range a {
		aSet[s] = true
	}

	intersection := 0
	for _, s := range b {
		if aSet[s] {
			intersection++
		}
	}

	union := len(a) + len(b) - intersection
	return float64(intersection) / float64(union)
}

// mapOverlap computes the overlap between two attribute maps.
func mapOverlap(a, b map[string]string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}

	matching := 0
	for k, v := range a {
		if bv, ok := b[k]; ok && bv == v {
			matching++
		}
	}

	total := len(a)
	if len(b) > total {
		total = len(b)
	}
	return float64(matching) / float64(total)
}

// extractAttributes returns all attributes of an element as a map.
func extractAttributes(sel *goquery.Selection) map[string]string {
	attrs := make(map[string]string)
	if sel.Length() == 0 {
		return attrs
	}
	for _, attr := range sel.Get(0).Attr {
		attrs[attr.Key] = attr.Val
	}
	return attrs
}

// attrOr returns an attribute value or a default.
func attrOr(sel *goquery.Selection, attr) string {
	val, exists := sel.Attr(attr)
	if !exists {
		return ""
	}
	return val
}

// truncate shortens a string to maxLen with ellipsis.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
