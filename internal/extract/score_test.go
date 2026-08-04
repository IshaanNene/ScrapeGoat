package extract

import (
	"fmt"
	"sort"
	"strings"
)

// Scoring.
//
// # Why token multiset F1
//
// Extraction is judged on the text produced, not the DOM nodes chosen: two
// extractors that select different nodes but yield the same prose are equally
// good. Comparing whole strings is too brittle (one stray "Read more" fails the
// document), and comparing sets loses repetition, so a document that emits the
// nav bar forty times would score the same as one that emits it once.
//
// A multiset — tokens with their counts — captures both. Precision is the share
// of emitted tokens that belong; recall is the share of wanted tokens that were
// emitted; F1 is their harmonic mean.
//
// # What each failure looks like
//
//	low precision, high recall  -> got the article plus the boilerplate
//	high precision, low recall  -> got a fragment, or missed the article entirely
//	both low                    -> selected the wrong block
//
// Reporting all three matters. An extractor that returns the whole page body
// scores perfect recall, and a single aggregate number would flatter it.

// Score is one document's result.
type Score struct {
	Precision float64
	Recall    float64
	F1        float64

	GotTokens  int
	WantTokens int
}

// tokenise splits on whitespace and lowercases, dropping punctuation-only tokens.
//
// Case and punctuation are normalised away deliberately: an extractor that keeps
// a trailing comma is not worse at extraction, and rewarding exact byte matching
// would measure formatting rather than selection.
func tokenise(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !isWordRune(r)
	})

	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func isWordRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r >= 0x00C0: // keep accented and non-Latin letters
		return true
	}
	return false
}

func counts(tokens []string) map[string]int {
	m := make(map[string]int, len(tokens))
	for _, t := range tokens {
		m[t]++
	}
	return m
}

// score compares extracted text against ground truth.
func score(got, want string) Score {
	gotTokens, wantTokens := tokenise(got), tokenise(want)
	gc, wc := counts(gotTokens), counts(wantTokens)

	// Multiset intersection: for each token, the smaller of the two counts.
	overlap := 0
	for tok, n := range gc {
		if m, ok := wc[tok]; ok {
			overlap += min(n, m)
		}
	}

	var s Score
	s.GotTokens, s.WantTokens = len(gotTokens), len(wantTokens)

	// A page with no main content is a real case — a link directory, a tag index —
	// and returning nothing for it is the correct answer, not a failure. Without
	// this, wantTokens is zero, recall is zero, and F1 is zero no matter what the
	// extractor does, so the tier could never be passed and the metric would be
	// punishing the right behaviour.
	if len(wantTokens) == 0 {
		if len(gotTokens) == 0 {
			s.Precision, s.Recall, s.F1 = 1, 1, 1
		}
		return s
	}

	if len(gotTokens) > 0 {
		s.Precision = float64(overlap) / float64(len(gotTokens))
	}
	if len(wantTokens) > 0 {
		s.Recall = float64(overlap) / float64(len(wantTokens))
	}
	if s.Precision+s.Recall > 0 {
		s.F1 = 2 * s.Precision * s.Recall / (s.Precision + s.Recall)
	}
	return s
}

// candidate is anything that turns a page into its main text. The benchmark
// compares implementations through this, so adding a contender needs no harness
// changes.
type candidate interface {
	Name() string
	Extract(html string) (string, error)
}

// Report aggregates scores across the corpus.
type Report struct {
	Extractor string
	ByTier    map[int][]Score
	All       []Score
}

func newReport(name string) *Report {
	return &Report{Extractor: name, ByTier: make(map[int][]Score)}
}

func (r *Report) add(tier int, s Score) {
	r.ByTier[tier] = append(r.ByTier[tier], s)
	r.All = append(r.All, s)
}

func mean(scores []Score, f func(Score) float64) float64 {
	if len(scores) == 0 {
		return 0
	}
	total := 0.0
	for _, s := range scores {
		total += f(s)
	}
	return total / float64(len(scores))
}

// Table renders the report as markdown, per tier and overall.
//
// Macro-averaged: every document counts equally, regardless of length. A
// micro-average would let the three longest pages dominate, which would hide a
// tier that fails completely.
func (r *Report) Table() string {
	var b strings.Builder

	fmt.Fprintf(&b, "### %s\n\n", r.Extractor)
	b.WriteString("| Tier | Markup | Docs | Precision | Recall | F1 |\n")
	b.WriteString("|---|---|---:|---:|---:|---:|\n")

	labels := map[int]string{
		1: "semantic (`<article>`)",
		2: "divs, conventional classes",
		3: "anonymous divs",
		4: "misleading classes",
		5: "comments longer than article",
		6: "short article, heavy chrome",
		7: "article split by ads",
		8: "no article at all",
	}

	tiers := make([]int, 0, len(r.ByTier))
	for t := range r.ByTier {
		tiers = append(tiers, t)
	}
	sort.Ints(tiers)

	for _, t := range tiers {
		s := r.ByTier[t]
		fmt.Fprintf(&b, "| %d | %s | %d | %.3f | %.3f | %.3f |\n",
			t, labels[t], len(s),
			mean(s, func(x Score) float64 { return x.Precision }),
			mean(s, func(x Score) float64 { return x.Recall }),
			mean(s, func(x Score) float64 { return x.F1 }))
	}

	fmt.Fprintf(&b, "| **all** | | **%d** | **%.3f** | **%.3f** | **%.3f** |\n",
		len(r.All),
		mean(r.All, func(x Score) float64 { return x.Precision }),
		mean(r.All, func(x Score) float64 { return x.Recall }),
		mean(r.All, func(x Score) float64 { return x.F1 }))

	return b.String()
}

// OverallF1 is the headline number, macro-averaged.
func (r *Report) OverallF1() float64 {
	return mean(r.All, func(x Score) float64 { return x.F1 })
}

// evaluate runs one extractor over the corpus.
func evaluate(e candidate, corpus []Document) *Report {
	rep := newReport(e.Name())
	for _, doc := range corpus {
		got, err := e.Extract(doc.HTML)
		if err != nil {
			// A failure is a zero, not a skip: an extractor that errors on half the
			// corpus should not be rewarded with a high average on the other half.
			rep.add(doc.Tier, Score{WantTokens: len(tokenise(doc.Want))})
			continue
		}
		rep.add(doc.Tier, score(got, doc.Want))
	}
	return rep
}
