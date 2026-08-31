package parser

import (
	"sort"

	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// Derivation method versions.
//
// Bump one when the extraction it names would produce a different value for the
// same rule on the same page. A corpus assembled across such a change otherwise has
// two kinds of row in it with no way to tell them apart, which is the thing method
// versioning exists to prevent — the values are not wrong, they are answers to
// slightly different questions.
// deterministicConfidence is what a selector, an XPath expression or a regex
// reports.
//
// It says the method has no uncertainty about what it matched: run the same
// selector over the same bytes and you get the same string, every time. It is not
// a claim that the selector was pointed at the right thing — no extractor can know
// that, and reading it as an accuracy score would be reading it as something it
// cannot be. Grounding is what turns it into a number worth anything: a value the
// evidence cannot support has its confidence zeroed regardless of what produced it.
const deterministicConfidence = 1.0

const (
	cssVersion        = "1"
	xpathVersion      = "1"
	regexVersion      = "1"
	structuredVersion = "1"
)

// valueAssertions turns the values one rule matched into one assertion each.
//
// One per value rather than one per rule holding a list, because an assertion is a
// claim and each matched element is a separate claim with its own place in the
// document. A list of twenty prices supported by a single span pointing at the first
// of them would be a citation that does not check out for nineteen of its values.
//
// Index records ordinality so the projection back to Item can restore the order
// after a round trip through a corpus file, where rows carry no inherent order.
func valueAssertions(field, method, version string, values []string) []provenance.Assertion {
	if len(values) == 0 {
		return nil
	}
	out := make([]provenance.Assertion, 0, len(values))
	for i, v := range values {
		out = append(out, provenance.Assertion{
			SchemaVersion: provenance.SchemaVersion,
			Field:         field,
			Value:         v,
			Index:         i,
			Method:        method,
			MethodVersion: version,
			Confidence:    deterministicConfidence,
		})
	}
	return out
}

// singleAssertion records a field whose derivation already decided on one value.
//
// Used where the extraction is not "match a selector n times" — a page's JSON-LD
// block, its OpenGraph set — and where the value is not necessarily a string.
func singleAssertion(field, method, version string, value any) provenance.Assertion {
	return provenance.Assertion{
		SchemaVersion: provenance.SchemaVersion,
		Field:         field,
		Value:         value,
		Method:        method,
		MethodVersion: version,
		Confidence:    deterministicConfidence,
	}
}

// ItemFromAssertions projects a set of assertions into the legacy Item shape.
//
// Item is a view over assertions rather than a second thing produced alongside
// them. That is the whole point of the change: there is one derivation, and Item is
// one way of looking at it. Everything Item cannot represent — which method
// produced a value, at what version, supported by which bytes — is not lost, it is
// simply not visible through this particular window.
//
// The collapse rules reproduce what the parsers used to do directly, because the
// corpus in tests/golden pins that behaviour and a migration that quietly changed a
// field's type would be exactly the failure this whole exercise is guarding
// against:
//
//   - one assertion for a field  -> the value itself
//   - several                    -> a slice in Index order, []string when every
//     value is a string, which is what a selector matching many elements produced
//     before and what anything reading the output will already expect
//   - none                       -> the field is absent, not empty
func ItemFromAssertions(sourceURL string, assertions []provenance.Assertion) *types.Item {
	if len(assertions) == 0 {
		return nil
	}

	item := types.NewItem(sourceURL)

	byField := map[string][]provenance.Assertion{}
	for _, a := range assertions {
		byField[a.Field] = append(byField[a.Field], a)
	}

	for field, group := range byField {
		if len(group) == 1 {
			item.Set(field, group[0].Value)
			continue
		}

		// Sort by Index, not by arrival. Assertions read back from a corpus file
		// arrive in whatever order the reader yields them, and a field whose values
		// silently reordered between the crawl and the read would be a different
		// answer wearing the same name.
		sort.SliceStable(group, func(i, j int) bool { return group[i].Index < group[j].Index })

		allStrings := true
		for _, a := range group {
			if _, ok := a.Value.(string); !ok {
				allStrings = false
				break
			}
		}

		if allStrings {
			vals := make([]string, 0, len(group))
			for _, a := range group {
				vals = append(vals, a.Value.(string))
			}
			item.Set(field, vals)
			continue
		}

		vals := make([]any, 0, len(group))
		for _, a := range group {
			vals = append(vals, a.Value)
		}
		item.Set(field, vals)
	}

	if len(item.Fields) == 0 {
		return nil
	}
	return item
}
