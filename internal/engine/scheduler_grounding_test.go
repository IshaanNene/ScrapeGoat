package engine

import (
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
)

// TestGroundingSkipsStructuredValues covers the third state.
//
// A JSON-LD graph or an OpenGraph set is a parsed object, not a run of source
// text. Running it through a text search would mark every one of them unsupported
// for structural reasons, and a flag that fires on a whole category of values
// stops being a signal — "unsupported" has to mean a derivation made a claim its
// own bytes do not carry.
//
// So they are left unattempted: neither validated nor unsupported, which is the
// honest reading that nobody checked.
func TestGroundingSkipsStructuredValues(t *testing.T) {
	body := []byte(`<script type="application/ld+json">{"name":"A Book"}</script>`)
	index := provenance.NewEvidenceIndex("abc123", body)

	a := provenance.Assertion{
		Field:      "json_ld",
		Value:      map[string]any{"name": "A Book"},
		Method:     "structured:jsonld",
		Confidence: 1,
	}
	groundAssertion(index, &a)

	if a.Validated {
		t.Error("a structured value was reported as grounded")
	}
	if a.Unsupported {
		t.Error("a structured value was marked unsupported; it was never attempted")
	}
	if a.Confidence != 1 {
		t.Errorf("Confidence = %v, want it untouched: nothing was learned either way", a.Confidence)
	}
	if a.Evidence.ObservationHash != "abc123" {
		t.Errorf("ObservationHash = %q; an unattempted claim still belongs to its observation",
			a.Evidence.ObservationHash)
	}
	if !a.Evidence.Empty() {
		t.Errorf("Evidence = %+v, want no range", a.Evidence)
	}
}

// TestGroundingZeroesConfidenceOnFailure pins the collapse. A value the bytes do
// not support must not keep the confidence its deriver claimed, or a downstream
// filter reading only Confidence would treat it as trustworthy.
func TestGroundingZeroesConfidenceOnFailure(t *testing.T) {
	index := provenance.NewEvidenceIndex("abc123", []byte("<p>nothing relevant</p>"))

	a := provenance.Assertion{
		Field:      "summary",
		Value:      "a sentence that is not on the page",
		Method:     "model:test",
		Confidence: 0.95,
	}
	groundAssertion(index, &a)

	if a.Validated {
		t.Error("Validated = true for text that is not in the body")
	}
	if !a.Unsupported {
		t.Error("Unsupported = false; an attempted claim that failed must say so")
	}
	if a.Confidence != 0 {
		t.Errorf("Confidence = %v, want 0", a.Confidence)
	}
	if a.Value != "a sentence that is not on the page" {
		t.Error("the value was dropped rather than recorded and flagged")
	}
}
