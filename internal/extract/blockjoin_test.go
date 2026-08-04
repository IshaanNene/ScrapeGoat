package extract

import (
	"strings"
	"testing"
)

// goquery's Text() concatenates descendant text with nothing between it, so a
// heading runs into the paragraph after it. In a corpus that fabricates a token
// appearing in no document, and every tokeniser, language detector, and dedupe
// hash downstream inherits it.
func TestExtractedTextKeepsBlockBoundaries(t *testing.T) {
	html := `<html><body><article>
		<h1>Reserved</h1>
		<p>This page reserves its rights against text and data mining.</p>
		<p>A second paragraph, long enough that the block is chosen as content.</p>
		<ul><li>First item</li><li>Second item</li></ul>
	</article></body></html>`

	res, err := New().FromHTML(html)
	if err != nil {
		t.Fatalf("FromHTML: %v", err)
	}

	for _, bad := range []string{"ReservedThis", "miningA", "itemSecond"} {
		if strings.Contains(res.Text, bad) {
			t.Errorf("block boundary lost, produced %q in: %s", bad, res.Text)
		}
	}
	for _, want := range []string{"Reserved", "text and data mining", "First item", "Second item"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("missing %q in: %s", want, res.Text)
		}
	}
}

// Inline elements must not gain spaces — splitting a word is the same class of
// corruption as joining two.
func TestExtractedTextDoesNotSplitInlineMarkup(t *testing.T) {
	html := `<html><body><article>
		<p>The <strong>quick</strong> brown fox jumps over the lazy dog repeatedly.</p>
		<p>Some <em>emphasis</em> and a <a href="/x">link</a> inside a sentence here.</p>
		<p>Padding so this block wins the density score against the rest of the page.</p>
	</article></body></html>`

	res, err := New().FromHTML(html)
	if err != nil {
		t.Fatalf("FromHTML: %v", err)
	}

	if !strings.Contains(res.Text, "The quick brown fox") {
		t.Errorf("inline markup was split: %s", res.Text)
	}
	if strings.Contains(res.Text, "  ") {
		t.Errorf("double space survived normalisation: %q", res.Text)
	}
}

// <br> is a line break and must separate, even though it is not a container.
func TestBrSeparates(t *testing.T) {
	html := `<html><body><article>
		<p>First line here with enough words to matter<br>Second line here with more words</p>
		<p>Additional padding text so that this article block wins the scoring easily.</p>
	</article></body></html>`

	res, err := New().FromHTML(html)
	if err != nil {
		t.Fatalf("FromHTML: %v", err)
	}
	if strings.Contains(res.Text, "matterSecond") {
		t.Errorf("<br> did not separate: %s", res.Text)
	}
}
