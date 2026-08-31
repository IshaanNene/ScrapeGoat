package provenance

import (
	"bytes"
	"strings"
	"testing"
)

func TestValidateFindsVerbatimEvidence(t *testing.T) {
	body := []byte(`<html><body><p class="price">£51.25</p></body></html>`)
	a := &Assertion{Field: "price", Value: "£51.25", Method: "css:.price", Confidence: 0.9}

	a.Validate("abc123", body, "£51.25")

	if !a.Validated {
		t.Fatalf("Validated = false; the text is present verbatim")
	}
	if a.Unsupported {
		t.Error("Unsupported = true for a value that was found")
	}
	if a.Confidence != 0.9 {
		t.Errorf("Confidence = %v, want the deriver's claim preserved on success", a.Confidence)
	}
	if a.Evidence.ObservationHash != "abc123" {
		t.Errorf("ObservationHash = %q, want abc123", a.Evidence.ObservationHash)
	}
	// The span must actually cut the claimed text out of the bytes. This is the
	// whole point: a range that does not is worse than no range at all.
	if got := string(body[a.Evidence.ByteStart:a.Evidence.ByteEnd]); got != "£51.25" {
		t.Errorf("span cuts %q out of the body, want %q", got, "£51.25")
	}
}

// TestValidateFindsWhitespaceNormalisedEvidence covers the common case: extracted
// text has had its whitespace collapsed, so it is not byte-identical to its source
// even though it is the same text.
func TestValidateFindsWhitespaceNormalisedEvidence(t *testing.T) {
	body := []byte("<p>The quick   brown\n\t fox jumps</p>")
	a := &Assertion{Field: "text", Confidence: 0.7}

	a.Validate("h", body, "The quick brown fox jumps")

	if !a.Validated {
		t.Fatalf("Validated = false; the text differs from source only in whitespace")
	}
	got := string(body[a.Evidence.ByteStart:a.Evidence.ByteEnd])
	if got != "The quick   brown\n\t fox jumps" {
		t.Errorf("span cuts %q, want the original run of source text", got)
	}
}

// TestValidateRecordsUnsupportedRatherThanDropping is the property that makes this
// worth having: a claim that cannot be grounded is kept and marked, because those
// are the cases worth looking at.
func TestValidateRecordsUnsupportedRatherThanDropping(t *testing.T) {
	body := []byte("<p>Nothing relevant here.</p>")
	a := &Assertion{Field: "summary", Value: "invented", Method: "model:test", Confidence: 0.95}

	a.Validate("h", body, "a quote that does not appear on the page")

	if a.Validated {
		t.Error("Validated = true for text that is not in the body")
	}
	if !a.Unsupported {
		t.Error("Unsupported = false; an ungrounded claim must be flagged")
	}
	if a.Confidence != 0 {
		t.Errorf("Confidence = %v, want 0 — an unverified confidence must not survive", a.Confidence)
	}
	if a.Value != "invented" {
		t.Error("the value was dropped; it must be recorded and marked, not discarded")
	}
	if !a.Evidence.Empty() {
		t.Errorf("Evidence = %+v, want an empty span", a.Evidence)
	}
}

func TestValidateSpanRoundTripsAcrossAPage(t *testing.T) {
	body := []byte(`<html><head><title>  A  Title </title></head>
<body><nav>Home</nav><article><h1>Heading</h1>
<p>First   paragraph of the article body.</p>
<p>Second paragraph.</p></article></body></html>`)

	for _, claimed := range []string{
		"Heading",
		"First paragraph of the article body.",
		"Second paragraph.",
		"A Title",
	} {
		t.Run(claimed, func(t *testing.T) {
			a := &Assertion{Confidence: 0.5}
			a.Validate("h", body, claimed)
			if !a.Validated {
				t.Fatalf("Validate(%q) did not find it", claimed)
			}
			cut := string(body[a.Evidence.ByteStart:a.Evidence.ByteEnd])
			if normalise(cut) != normalise(claimed) {
				t.Errorf("span cuts %q, which does not normalise to %q", cut, claimed)
			}
		})
	}
}

// TestValidateKnownLimitTagSpanningText documents the failure mode explicitly, so
// that nobody reads Unsupported as proof of fabrication. If this ever starts
// passing, the matcher became tag-aware and the doc comment needs updating.
func TestValidateKnownLimitTagSpanningText(t *testing.T) {
	body := []byte("<p>Hello <b>world</b></p>")
	a := &Assertion{Confidence: 0.8}

	a.Validate("h", body, "Hello world")

	if a.Validated {
		t.Skip("the matcher became tag-aware; update Validate's doc comment, which " +
			"currently documents this as a known limit")
	}
	if !a.Unsupported {
		t.Error("text spanning inline markup should be flagged unsupported, not silently accepted")
	}
}

func TestValidateEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		claimed string
	}{
		{"empty body", "", "anything"},
		{"empty claim", "<p>text</p>", ""},
		{"whitespace-only claim", "<p>text</p>", "   \n\t "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Assertion{Confidence: 1}
			a.Validate("h", []byte(tt.body), tt.claimed)
			if a.Validated {
				t.Errorf("Validated = true for %s", tt.name)
			}
			if !a.Unsupported {
				t.Errorf("Unsupported = false for %s", tt.name)
			}
		})
	}
}

func TestEvidenceSpanEmptyAndLen(t *testing.T) {
	if !(EvidenceSpan{}).Empty() {
		t.Error("zero span is not Empty")
	}
	if !(EvidenceSpan{ByteStart: 5, ByteEnd: 10}).Empty() {
		t.Error("a span with no observation hash must be Empty — it points nowhere")
	}
	if !(EvidenceSpan{ObservationHash: "h", ByteStart: 5, ByteEnd: 5}).Empty() {
		t.Error("a zero-length span must be Empty")
	}
	s := EvidenceSpan{ObservationHash: "h", ByteStart: 5, ByteEnd: 10}
	if s.Empty() {
		t.Error("a real span reports Empty")
	}
	if s.Len() != 5 {
		t.Errorf("Len = %d, want 5", s.Len())
	}
}

func TestPolicyStateRestrictiveMatchesRecord(t *testing.T) {
	blocked := &AIDirectiveSummary{RobotsPresent: true, AgentsBlocked: []string{"GPTBot"}}

	p := PolicyState{AIDirectives: blocked}
	r := Record{AIDirectives: blocked}
	if p.Restrictive() != r.Restrictive() {
		t.Errorf("PolicyState.Restrictive = %v, Record.Restrictive = %v; "+
			"the two shapes must not disagree on the question the corpus exists to answer",
			p.Restrictive(), r.Restrictive())
	}
	if !p.Restrictive() {
		t.Error("a site blocking an AI agent is not reported as restrictive")
	}

	empty := PolicyState{}
	if empty.Restrictive() {
		t.Error("a source that asked for nothing is reported as restrictive")
	}
}

func normalise(s string) string { return strings.Join(strings.Fields(s), " ") }

// FuzzValidateSpanIsAlwaysInBounds fuzzes the byte-offset mapping.
//
// An off-by-one here does not crash and does not fail a unit test with tidy inputs.
// It writes a span that cuts the wrong bytes, and the corpus carries a citation that
// points at the wrong place — discoverable only by someone checking a claim years
// later, which is the one audience this whole model exists for. So the invariants
// are asserted against arbitrary input rather than chosen input.
func FuzzValidateSpanIsAlwaysInBounds(f *testing.F) {
	f.Add("<p>hello world</p>", "hello world")
	f.Add("<p>a   b</p>", "a b")
	f.Add("", "")
	f.Add("   ", " ")
	f.Add("<p>£51.25</p>", "£51.25")
	f.Add("\n\t\r\f\v x \n", "x")
	f.Add("aaaa", "aa")

	f.Fuzz(func(t *testing.T, body, claimed string) {
		a := &Assertion{Confidence: 1}
		a.Validate("h", []byte(body), claimed)

		if a.Validated == a.Unsupported {
			t.Fatalf("Validated=%v and Unsupported=%v; exactly one must hold", a.Validated, a.Unsupported)
		}

		if !a.Validated {
			if a.Confidence != 0 {
				t.Fatalf("unvalidated assertion kept Confidence=%v", a.Confidence)
			}
			return
		}

		s, e := a.Evidence.ByteStart, a.Evidence.ByteEnd
		if s < 0 || e < s || e > len(body) {
			t.Fatalf("span [%d,%d) is out of bounds for a %d-byte body", s, e, len(body))
		}
		// The span has to cut source text that renders to the claim, or the
		// citation is worse than none.
		//
		// Compared through the projection rather than raw, because that is what
		// the span means: `It&#39;s` in the source is the evidence for `It's`, and
		// a byte-for-byte comparison would call that a mismatch. The projection is
		// the definition of "renders to".
		// Rendered through the projection rather than compared raw, because that
		// is what the span means: `It&#39;s` in the source is the evidence for
		// `It's`. Either projection may have produced the match — prose is found
		// only with tags dropped — so the span is correct if it renders to the
		// claim under one of them.
		want := normaliseSpace([]byte(claimed))
		cut := []byte(body[s:e])
		withMarkup := buildProjection(cut, keepTags).norm
		withoutMarkup := buildProjection(cut, stripTags).norm
		if !bytes.Equal(withMarkup, want) && !bytes.Equal(withoutMarkup, want) {
			t.Fatalf("span cuts %q, which renders to %q (tags kept) or %q (tags dropped), want %q",
				cut, withMarkup, withoutMarkup, want)
		}
	})
}

// TestLocateThroughRealMarkup covers the transformations that stand between an
// extracted value and its source. Each case here was a page that would not ground
// before it was handled, found against a real corpus rather than imagined.
func TestLocateThroughRealMarkup(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		claimed string
	}{
		{
			// The single most common one: apostrophes are encoded everywhere.
			name:    "character reference",
			body:    "<title>\n  It&#39;s Only the Himalayas\n</title>",
			claimed: "It's Only the Himalayas",
		},
		{
			name:    "named reference",
			body:    "<p>Marks &amp; Spencer</p>",
			claimed: "Marks & Spencer",
		},
		{
			// The extractor collapses U+00A0 like any other space; a byte-wise
			// projection kept it and the value stopped matching its own page.
			name:    "non-breaking space",
			body:    "<p>In stock (19 available)&nbsp; Warning!</p>",
			claimed: "In stock (19 available) Warning!",
		},
		{
			name:    "prose across elements",
			body:    "<div><p>First paragraph.</p><p>Second paragraph.</p></div>",
			claimed: "First paragraph. Second paragraph.",
		},
		{
			// skipTag stopped at the '>' inside "-->", leaving commented-out
			// markup in the projection as though a reader had seen it.
			name:    "html comment",
			body:    "<p>Before</p><!-- 0 reviews --><p>After</p>",
			claimed: "Before After",
		},
		{
			// The extractor drops these elements entirely, so a projection that
			// keeps their text cannot match what it returned.
			name:    "button label",
			body:    "<div><p>Price £10</p><form><button>Add to basket</button></form><p>In stock</p></div>",
			claimed: "Price £10 In stock",
		},
		{
			name:    "script body",
			body:    "<div><p>Real text</p><script>var x = 'not text';</script><p>More text</p></div>",
			claimed: "Real text More text",
		},
		{
			name:    "attribute value keeps its markup",
			body:    `<a href="../book_981/index.html">Title</a>`,
			claimed: "../book_981/index.html",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ix := NewEvidenceIndex("h", []byte(tt.body))
			s, e, ok := ix.Locate(tt.claimed)
			if !ok {
				t.Fatalf("Locate(%q) found nothing in %q", tt.claimed, tt.body)
			}
			cut := []byte(tt.body)[s:e]
			if !SpanSupports(cut, tt.claimed) {
				t.Errorf("span cuts %q, which does not render to %q", cut, tt.claimed)
			}
		})
	}
}

// TestPartialEntityDecodeIsRejected pins the fuzz finding.
//
// HTML allows a few references without their semicolon, so unescaping "&gt0;"
// yields ">0;" — one reference and a literal tail. Treating that as a single
// reference mapped three rendered bytes onto five source bytes and produced a span
// covering characters nobody claimed.
func TestPartialEntityDecodeIsRejected(t *testing.T) {
	// No tags in the fixture: they contain literal '>' characters, which would
	// ground the claim for a reason that has nothing to do with the reference.
	body := []byte("&gt0;")
	ix := NewEvidenceIndex("h", body)

	if s, e, ok := ix.Locate(">"); ok {
		t.Errorf("Locate(%q) matched [%d,%d) = %q; a partial decode must not ground",
			">", s, e, body[s:e])
	}
}

// TestMatchesRespectCharacterBoundaries pins the other fuzz finding: the search is
// over bytes, and without an alignment check a claim could match the interior of a
// multi-byte character.
func TestMatchesRespectCharacterBoundaries(t *testing.T) {
	// "∾" is three bytes; its middle byte alone must not match.
	body := []byte("&ac;")
	ix := NewEvidenceIndex("h", body)

	middle := string([]byte{0x88})
	if s, e, ok := ix.Locate(middle); ok {
		t.Errorf("a fragment of a character matched [%d,%d) = %q", s, e, body[s:e])
	}

	// The whole character still grounds.
	if _, _, ok := ix.Locate("∾"); !ok {
		t.Error("the decoded character itself did not ground")
	}
}

// TestEvidenceIndexReusesItsProjection checks the reason the index exists: a page
// with many claims must not rebuild the rendering per claim.
func TestEvidenceIndexReusesItsProjection(t *testing.T) {
	body := []byte(strings.Repeat("<p>Some prose with &amp; in it, repeated.</p>", 200))
	ix := NewEvidenceIndex("h", body)

	for i := 0; i < 50; i++ {
		if _, _, ok := ix.Locate("Some prose with & in it, repeated."); !ok {
			t.Fatalf("iteration %d did not ground", i)
		}
	}
	if len(ix.markup.norm) == 0 {
		t.Error("the markup projection was never built")
	}
	if len(ix.markup.norm) != len(ix.markup.starts) || len(ix.markup.norm) != len(ix.markup.ends) {
		t.Errorf("projection is inconsistent: %d bytes, %d starts, %d ends",
			len(ix.markup.norm), len(ix.markup.starts), len(ix.markup.ends))
	}
}
