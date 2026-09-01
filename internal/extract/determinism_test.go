package extract

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

// TestExtractionIsReproducible asserts that extracting the same document twice
// gives the same answer.
//
// It did not. Candidates were held in a map, and Go randomises map iteration, so
// the total used for the confidence denominator was summed in a different order on
// every run — and floating point addition is not associative, so the value moved in
// its last bits. Two replays of one fetch log produced corpora that differed.
//
// The test runs many iterations because the failure is probabilistic: a small map
// often happens to iterate the same way twice.
func TestExtractionIsReproducible(t *testing.T) {
	html := buildDeterminismPage()

	first := extractOnce(t, html)
	if first.Text == "" {
		t.Fatal("fixture produced no extraction; the test would pass vacuously")
	}

	for i := 0; i < 200; i++ {
		got := extractOnce(t, html)
		if got.Confidence != first.Confidence {
			t.Fatalf("iteration %d: Confidence = %v, first run gave %v — extraction is not reproducible",
				i, got.Confidence, first.Confidence)
		}
		if got.Text != first.Text {
			t.Fatalf("iteration %d: extracted different text than the first run", i)
		}
		if got.LinkDensity != first.LinkDensity {
			t.Fatalf("iteration %d: LinkDensity = %v, first run gave %v", i, got.LinkDensity, first.LinkDensity)
		}
	}
}

// TestTiedCandidatesResolveDeterministically covers the other half. The winner was
// chosen with a strict >, so two containers on an identical score handed the article
// to whichever the map happened to yield first — a different block of text, not just
// a different number.
func TestTiedCandidatesResolveDeterministically(t *testing.T) {
	// Two structurally identical containers: same tags, same text, same score.
	block := strings.Repeat("A sentence of prose, with a comma and a stop. ", 12)
	html := `<html><body><main>` +
		`<div class="one"><p>` + block + `</p></div>` +
		`<div class="two"><p>` + block + `</p></div>` +
		`</main></body></html>`

	first := extractOnce(t, html)
	for i := 0; i < 200; i++ {
		if got := extractOnce(t, html); got.Text != first.Text {
			t.Fatalf("iteration %d selected a different container on a tie", i)
		}
	}
}

func extractOnce(t *testing.T, html string) *Result {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return New().FromDocument(doc)
}

// buildDeterminismPage makes a page with many scored containers, because the
// nondeterminism scales with how many entries the map holds.
func buildDeterminismPage() string {
	var b strings.Builder
	b.WriteString("<html><body>")
	b.WriteString("<article><h1>The Heading</h1>")
	for i := 0; i < 30; i++ {
		b.WriteString("<p>Paragraph of real prose, long enough to score, with commas, " +
			"stops. And a second sentence for good measure.</p>")
	}
	b.WriteString("</article>")
	for i := 0; i < 20; i++ {
		b.WriteString("<div class='side'><p>A shorter competing block, still scoring, " +
			"with punctuation. More text here.</p></div>")
	}
	b.WriteString("</body></html>")
	return b.String()
}
