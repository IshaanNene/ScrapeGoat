package extract

import (
	"os"
	"testing"
)

// TestExtractionBenchmark is the evaluation harness.
//
//	go test ./internal/extract -run TestExtractionBenchmark -v
//
// Prints a markdown table per extractor. The numbers in docs/EXTRACTION.md come
// from this and nowhere else, so they can be reproduced by anyone.
func TestExtractionBenchmark(t *testing.T) {
	corpus := buildCorpus()
	t.Logf("corpus: %d documents across %d tiers", len(corpus), 4)

	candidates := []candidate{
		wholeBodyExtractor{},
		paragraphExtractor{},
		selectorExtractor{},
		densityExtractor{},
	}

	var out string
	for _, e := range candidates {
		rep := evaluate(e, corpus)
		out += rep.Table() + "\n"
		t.Logf("\n%s", rep.Table())
	}

	if path := os.Getenv("SCRAPEGOAT_BENCH_OUT"); path != "" {
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}
		t.Logf("wrote %s", path)
	}
}

// TestCorpusIsWellFormed guards the benchmark itself. A corpus whose ground truth
// does not appear in its own HTML would make every extractor look bad for the
// wrong reason, and a corpus where boilerplate and content share vocabulary would
// make precision unmeasurable.
func TestCorpusIsWellFormed(t *testing.T) {
	corpus := buildCorpus()

	if len(corpus) == 0 {
		t.Fatal("empty corpus")
	}

	for _, doc := range corpus {
		// Tier 8 is the no-content page: empty ground truth is the point of it, so
		// the checks below do not apply. What matters instead is that the page
		// really has no article to find.
		if doc.Tier == 8 {
			if doc.Want != "" {
				t.Errorf("%s: the no-content tier must have empty ground truth", doc.Name)
			}
			whole, err := wholeBodyExtractor{}.Extract(doc.HTML)
			if err != nil {
				t.Fatalf("%s: %v", doc.Name, err)
			}
			if len(tokenise(whole)) == 0 {
				t.Errorf("%s: page is entirely empty, which tests nothing — it should "+
					"be full of boilerplate with no article", doc.Name)
			}
			continue
		}

		if doc.Want == "" {
			t.Errorf("%s: empty ground truth", doc.Name)
			continue
		}

		// Ground truth must be recoverable: a perfect extractor has to be able to
		// score 1.0, or the benchmark measures the corpus rather than the tools.
		if s := score(normaliseHTMLText(t, doc.HTML), doc.Want); s.Recall < 0.999 {
			t.Errorf("%s: ground truth is not fully present in the HTML (recall %.3f)",
				doc.Name, s.Recall)
		}

		whole, err := wholeBodyExtractor{}.Extract(doc.HTML)
		if err != nil {
			t.Fatalf("%s: %v", doc.Name, err)
		}
		contentShare := score(whole, doc.Want).Precision

		// Most tiers are hard because boilerplate dominates, so a page that is
		// mostly content would not be testing anything. Tier 7 is exempt: its
		// difficulty is that the article is *split across containers*, not that it
		// is buried, so a high content share there is expected.
		if doc.Tier != 7 && contentShare > 0.6 {
			t.Errorf("%s: page is %.0f%% content — too easy to be a useful test",
				doc.Name, contentShare*100)
		}
	}
}

// TestPerfectExtractorScoresOne pins the metric itself. If ground truth in and
// ground truth out does not score 1.0, the scoring is wrong and every other
// number in the report is meaningless.
func TestPerfectExtractorScoresOne(t *testing.T) {
	for _, doc := range buildCorpus() {
		s := score(doc.Want, doc.Want)
		if s.F1 < 0.9999 {
			t.Fatalf("%s: identical text scored F1 %.4f, want 1.0", doc.Name, s.F1)
		}
	}
}

// TestScoringDistinguishesFailureModes checks that precision and recall move
// independently, so the report can say *how* an extractor failed.
func TestScoringDistinguishesFailureModes(t *testing.T) {
	want := "the article body says something specific"

	tests := []struct {
		name           string
		got            string
		wantHighPrec   bool
		wantHighRecall bool
	}{
		{"exact", want, true, true},
		{"content plus boilerplate", want + " home about contact subscribe cookies footer terms privacy", false, true},
		{"fragment only", "the article body", true, false},
		{"wrong block entirely", "home about contact subscribe", false, false},
		{"empty", "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := score(tt.got, want)
			highP, highR := s.Precision > 0.75, s.Recall > 0.75

			if highP != tt.wantHighPrec {
				t.Errorf("precision %.3f: high=%v, want high=%v", s.Precision, highP, tt.wantHighPrec)
			}
			if highR != tt.wantHighRecall {
				t.Errorf("recall %.3f: high=%v, want high=%v", s.Recall, highR, tt.wantHighRecall)
			}
		})
	}
}

// normaliseHTMLText returns all text in the document, for the recoverability check.
func normaliseHTMLText(t *testing.T, html string) string {
	t.Helper()
	got, err := wholeBodyExtractor{}.Extract(html)
	if err != nil {
		t.Fatalf("parse corpus html: %v", err)
	}
	return got
}
