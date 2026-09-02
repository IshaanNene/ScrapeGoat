// Package golden freezes what the crawl derives from a fixed set of real pages.
//
// The corpus in testdata/books-toscrape is a recorded fetch log: 17 fetches from
// books.toscrape.com, stored as content-addressed bodies with the responses that
// produced them. Replaying it is offline and deterministic — robots.txt is served
// from the log like everything else, and every timestamp on the output comes from
// the recording rather than from the clock.
//
// It exists as the safety net for the Item/Assertion migration. types.Item and
// provenance.Record are today two parallel models built by two paths that never
// meet; the plan is for Item to become a view over Assertion. The risk in that is
// not that it fails loudly, it is that it succeeds quietly and changes a value
// somewhere in the middle of a corpus nobody re-reads. So the current output of
// both paths is frozen here first, against real pages rather than hand-written
// HTML, and the migrated pipeline has to reproduce it byte for byte.
//
// These files pin current behaviour, not correct behaviour. Some of what they
// record is arguably wrong — that is the point. A change detector cannot tell you
// what the output should be, only that it moved. Regenerate deliberately:
//
//	go test ./tests/golden -update
package golden

import (
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetchlog"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

var update = flag.Bool("update", false, "rewrite golden files from current output")

const (
	corpusDir = "testdata/books-toscrape"

	// crawlID is fixed so the records do not change identity between runs. A real
	// crawl generates one; a golden corpus needs the same one every time.
	crawlID = "golden-books-toscrape"
)

// corpusRules are applied to every page in the corpus.
//
// Fixed across the corpus so a golden diff reflects a change in derivation rather
// than a change in what was asked for. Chosen to cover the derivation paths that
// have to carry evidence spans once assertions land: element text, an attribute, a
// selector matching many elements, and one matching nothing — because "the field is
// absent" is a derivation outcome too, and a migration that silently starts
// emitting empty strings instead would pass a test that only checked the hits.
var corpusRules = []config.ParseRule{
	{Name: "title", Selector: "title", Type: "css"},
	{Name: "product_title", Selector: ".product_main h1", Type: "css"},
	{Name: "price", Selector: ".price_color", Type: "css"},
	{Name: "availability", Selector: ".availability", Type: "css"},
	{Name: "breadcrumb", Selector: ".breadcrumb li", Type: "css"},
	{Name: "book_links", Selector: "h3 a", Type: "css", Attribute: "href"},
	{Name: "absent", Selector: ".definitely-not-present", Type: "css"},
}

// goldenItem is types.Item in a stable, diffable shape.
type goldenItem struct {
	URL        string         `json:"url"`
	SpiderName string         `json:"spider_name,omitempty"`
	Depth      int            `json:"depth"`
	Timestamp  string         `json:"timestamp"`
	Checksum   string         `json:"checksum,omitempty"`
	Fields     map[string]any `json:"fields"`
}

// goldenRecord is provenance.Record reduced to the fields a migration could move.
//
// Not the whole struct: text runs to kilobytes per page and would make the golden
// file unreadable and its diffs useless. Text is pinned by length and by its first
// and last words instead, which moves whenever extraction moves without burying the
// rest of the comparison.
type goldenRecord struct {
	URL                  string   `json:"url"`
	CanonicalURL         string   `json:"canonical_url,omitempty"`
	ContentHash          string   `json:"content_hash"`
	FetchedAt            string   `json:"fetched_at"`
	StatusCode           int      `json:"status_code"`
	MIMEType             string   `json:"mime_type,omitempty"`
	FinalURL             string   `json:"final_url,omitempty"`
	ETag                 string   `json:"etag,omitempty"`
	LastModified         string   `json:"last_modified,omitempty"`
	Title                string   `json:"title,omitempty"`
	TextLen              int      `json:"text_len"`
	TextHead             string   `json:"text_head,omitempty"`
	TextTail             string   `json:"text_tail,omitempty"`
	ExtractionConfidence float64  `json:"extraction_confidence,omitempty"`
	RobotsAllowed        bool     `json:"robots_allowed"`
	Restrictive          bool     `json:"restrictive"`
	AgentsBlocked        []string `json:"agents_blocked,omitempty"`
	Licence              string   `json:"licence,omitempty"`
	CrawlID              string   `json:"crawl_id,omitempty"`
}

// TestGoldenItems pins what the parser derives from the corpus.
//
// When Item becomes a view over Assertion, this file is what the derived items
// must still equal. Nothing about the assertion path needs to exist for the
// comparison to be written — that is the point of writing it now.
func TestGoldenItems(t *testing.T) {
	items, _, _ := replayCorpus(t)

	got := make([]goldenItem, 0, len(items))
	for _, it := range items {
		got = append(got, goldenItem{
			URL:        it.URL,
			SpiderName: it.SpiderName,
			Depth:      it.Depth,
			Timestamp:  it.Timestamp.UTC().Format(time.RFC3339Nano),
			Checksum:   it.Checksum,
			Fields:     it.Fields,
		})
	}
	// Replay order depends on worker scheduling even at concurrency 1, because the
	// frontier is a priority queue. Sort on something intrinsic to the item.
	sort.Slice(got, func(i, j int) bool {
		if got[i].URL != got[j].URL {
			return got[i].URL < got[j].URL
		}
		return got[i].Depth < got[j].Depth
	})

	compareGolden(t, "items.golden.json", got)
}

// TestGoldenRecords pins the provenance side of the same replay.
//
// The two halves are frozen from one run rather than two, so that a change which
// moves an item and its record together is still visible as one diff.
func TestGoldenRecords(t *testing.T) {
	_, records, _ := replayCorpus(t)

	got := make([]goldenRecord, 0, len(records))
	for _, r := range records {
		g := goldenRecord{
			URL:                  r.URL,
			CanonicalURL:         r.CanonicalURL,
			ContentHash:          r.ContentHash,
			FetchedAt:            r.FetchedAt.UTC().Format(time.RFC3339Nano),
			StatusCode:           r.StatusCode,
			MIMEType:             r.MIMEType,
			FinalURL:             r.FinalURL,
			ETag:                 r.ETag,
			LastModified:         r.LastModified,
			Title:                r.Title,
			TextLen:              len(r.Text),
			TextHead:             head(r.Text, 60),
			TextTail:             tail(r.Text, 60),
			ExtractionConfidence: r.ExtractionConfidence,
			RobotsAllowed:        r.RobotsAllowed,
			Restrictive:          r.Restrictive(),
			Licence:              r.Signals.Licence,
			CrawlID:              r.CrawlID,
		}
		if r.AIDirectives != nil {
			g.AgentsBlocked = r.AIDirectives.AgentsBlocked
		}
		got = append(got, g)
	}
	sort.Slice(got, func(i, j int) bool {
		if got[i].URL != got[j].URL {
			return got[i].URL < got[j].URL
		}
		return got[i].ContentHash < got[j].ContentHash
	})

	compareGolden(t, "records.golden.json", got)
}

// goldenAssertion summarises the claims one derivation made about one field.
//
// A summary rather than a row per value, because the values themselves are already
// pinned by items.golden.json — Item is the projection of these assertions, so
// duplicating them here would double the size of the fixtures to assert the same
// thing twice, and make both diffs harder to read. What is only visible on this
// side is which method produced the field, at what version, how many values it
// matched, and which observation it attaches to.
type goldenAssertion struct {
	SourceURL       string `json:"source_url"`
	Field           string `json:"field"`
	Method          string `json:"method"`
	MethodVersion   string `json:"method_version"`
	Values          int    `json:"values"`
	ObservationHash string `json:"observation_hash"`
	Validated       bool   `json:"validated"`
	Unsupported     bool   `json:"unsupported,omitempty"`
}

// TestGoldenAssertions pins the new shape as well as the old one.
//
// The projection test proves the item agrees with the assertions; it cannot prove
// the assertions are right, because it compares them against something derived
// from the same data. This pins what a claim says about itself — which selector,
// at what version — so that a change to a method string, which no other test would
// notice, shows up as a diff.
func TestGoldenAssertions(t *testing.T) {
	_, _, assertions := replayCorpus(t)

	type key struct{ url, field string }
	summary := map[key]*goldenAssertion{}
	for _, a := range assertions {
		k := key{a.SourceURL, a.Field}
		g, ok := summary[k]
		if !ok {
			g = &goldenAssertion{
				SourceURL:       a.SourceURL,
				Field:           a.Field,
				Method:          a.Method,
				MethodVersion:   a.MethodVersion,
				ObservationHash: a.Evidence.ObservationHash,
				Validated:       a.Validated,
				Unsupported:     a.Unsupported,
			}
			summary[k] = g
		}
		g.Values++
		// Two methods claiming one field would mean dropShadowed let a collision
		// through, which is the bug that turns a scalar into a list.
		if g.Method != a.Method {
			t.Errorf("%s: field %q claimed by both %q and %q", a.SourceURL, a.Field, g.Method, a.Method)
		}
	}

	got := make([]goldenAssertion, 0, len(summary))
	for _, g := range summary {
		got = append(got, *g)
	}
	sort.SliceStable(got, func(i, j int) bool {
		if got[i].SourceURL != got[j].SourceURL {
			return got[i].SourceURL < got[j].SourceURL
		}
		return got[i].Field < got[j].Field
	})

	compareGolden(t, "assertions.golden.json", got)
}

// TestCorpusIsIntact checks the fixture before anything is concluded from it.
//
// A golden file compared against a corrupted corpus is a test that fails for a
// reason nobody will guess. Content-addressed storage makes the check cheap, so
// there is no reason to skip it.
func TestCorpusIsIntact(t *testing.T) {
	store, err := fetchlog.NewStore(corpusDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	entries, err := fetchlog.ReadLog(corpusDir)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the corpus is empty; testdata/books-toscrape is missing or truncated")
	}

	for _, e := range entries {
		if e.Digest == "" {
			continue // a failed fetch stores no body
		}
		body, err := store.Get(e.Digest)
		if err != nil {
			t.Errorf("%s: body %s is missing from the store: %v", e.URL, e.Digest, err)
			continue
		}
		if got := fetchlog.Digest(body); got != e.Digest {
			t.Errorf("%s: body hashes to %s but is filed under %s", e.URL, got, e.Digest)
		}
	}
}

// replayCorpus runs the current pipeline over the frozen log and returns what it
// derived. Offline: the player serves every fetch, robots.txt included.
func replayCorpus(t *testing.T) ([]*types.Item, []provenance.Record, []provenance.Assertion) {
	t.Helper()

	manifest, err := fetchlog.ReadManifest(corpusDir)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}

	cfg := config.DefaultConfig()
	if len(manifest.Config) > 0 {
		if err := json.Unmarshal(manifest.Config, cfg); err != nil {
			t.Fatalf("parse recorded config: %v", err)
		}
	}
	// One worker: the derivation is what is under test, not the scheduler, and a
	// single worker removes a source of run-to-run variation that would otherwise
	// have to be sorted away.
	cfg.Engine.Concurrency = 1
	cfg.Engine.PolitenessDelay = 0
	cfg.Parser.Rules = corpusRules
	cfg.Storage.OutputPath = t.TempDir()

	player, err := fetchlog.NewPlayer(corpusDir)
	if err != nil {
		t.Fatalf("open player: %v", err)
	}
	defer player.Close()

	eng := engine.New(cfg, testLogger())
	eng.SetFetcher("http", player)
	eng.SetParser(parser.NewCompositeParser(testLogger()))

	sink := &recordSink{}
	eng.SetCorpusWriter(sink, crawlID)

	claims := &assertionSink{}
	eng.SetAssertionWriter(claims)

	results := eng.ResultsChan()

	for _, seed := range manifest.Seeds {
		if err := eng.AddSeed(seed); err != nil {
			t.Fatalf("seed %s: %v", seed, err)
		}
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}

	var items []*types.Item
	done := make(chan struct{})
	go func() {
		defer close(done)
		for it := range results {
			items = append(items, it)
		}
	}()

	eng.Wait()
	<-done

	if len(items) == 0 {
		t.Fatal("replay produced no items; the corpus or the pipeline is broken")
	}
	return items, sink.all(), claims.all()
}

func compareGolden(t *testing.T, name string, got any) {
	t.Helper()

	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	encoded = append(encoded, '\n')

	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("updated %s (%d bytes)", path, len(encoded))
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./tests/golden -update)", path, err)
	}
	if string(encoded) != string(want) {
		t.Errorf("%s differs from the frozen output.\n"+
			"If the change is intended, review the diff and re-run with -update.\n"+
			"first difference at byte %d", name, firstDiff(string(want), string(encoded)))
	}
}

func firstDiff(a, b string) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func tail(s string, n int) string {
	if len(s) <= n {
		return ""
	}
	return s[len(s)-n:]
}

// recordSink collects the provenance records written during a replay.
type recordSink struct {
	mu      sync.Mutex
	records []provenance.Record
}

func (s *recordSink) Write(r provenance.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
	return nil
}
func (s *recordSink) Stats() (int64, int64) { return int64(len(s.records)), 0 }
func (s *recordSink) Path() string          { return "" }
func (s *recordSink) Close() error          { return nil }

func (s *recordSink) all() []provenance.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provenance.Record, len(s.records))
	copy(out, s.records)
	return out
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// assertionSink collects the claims written during a replay.
type assertionSink struct {
	mu         sync.Mutex
	assertions []provenance.Assertion
}

func (s *assertionSink) Write(a provenance.Assertion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assertions = append(s.assertions, a)
	return nil
}
func (s *assertionSink) Stats() (int64, int64) { return int64(len(s.assertions)), 0 }
func (s *assertionSink) Path() string          { return "" }
func (s *assertionSink) Close() error          { return nil }

func (s *assertionSink) all() []provenance.Assertion {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]provenance.Assertion, len(s.assertions))
	copy(out, s.assertions)
	return out
}

// TestItemsAreExactlyTheAssertionProjection is the equivalence check the whole
// migration rests on.
//
// Item is supposed to be a view over the assertions, not a second thing produced
// beside them. If the two ever disagree, the corpus and the extracted data are
// describing different crawls, and the disagreement would be invisible — both
// files would look fine on their own.
//
// So the assertions are projected back into items here and compared against the
// items the crawl actually emitted, across every page in the corpus.
func TestItemsAreExactlyTheAssertionProjection(t *testing.T) {
	items, _, assertions := replayCorpus(t)
	if len(assertions) == 0 {
		t.Fatal("replay produced no assertions; dual-write is not happening")
	}

	// Only the rule-based derivations. The main-content extractor also makes
	// claims, but they describe the record's own text and title columns rather
	// than the item's extracted fields, and the item has never carried them —
	// projecting them in would assert that every consumer's output should suddenly
	// gain the article body. The distinction goes away in the cutover, when the
	// item does.
	byURL := map[string][]provenance.Assertion{}
	for _, a := range assertions {
		if strings.HasPrefix(a.Method, "density:") {
			continue
		}
		byURL[a.SourceURL] = append(byURL[a.SourceURL], a)
	}

	emitted := map[string]*types.Item{}
	for _, it := range items {
		emitted[it.URL] = it
	}

	for url, item := range emitted {
		projected := parser.ItemFromAssertions(url, byURL[url])
		if projected == nil {
			t.Errorf("%s: emitted an item but its assertions project to nothing", url)
			continue
		}
		if !reflect.DeepEqual(projected.Fields, item.Fields) {
			t.Errorf("%s: the assertion projection does not match the emitted item\nprojected: %#v\nemitted:   %#v",
				url, projected.Fields, item.Fields)
		}
	}

	for url := range byURL {
		if _, ok := emitted[url]; !ok {
			t.Errorf("%s: produced assertions but no item", url)
		}
	}
}

// TestEveryAssertionNamesItsObservation checks the join.
//
// An assertion whose evidence names no observation is a value with no stated
// source, which is precisely what types.Item was and what this model replaces. The
// hash must also be one the corpus actually contains, or the two tables do not
// join and the citation points at nothing.
func TestEveryAssertionNamesItsObservation(t *testing.T) {
	_, records, assertions := replayCorpus(t)

	known := map[string]bool{}
	for _, r := range records {
		known[r.ContentHash] = true
		// The observation view has to agree with the record it came from, or the
		// join key means two different things depending on which shape you read.
		if got := r.Observation().Hash; got != r.ContentHash {
			t.Errorf("%s: Observation().Hash = %q, record ContentHash = %q", r.URL, got, r.ContentHash)
		}
	}

	for _, a := range assertions {
		if a.Evidence.ObservationHash == "" {
			t.Errorf("assertion %q on %s names no observation", a.Field, a.SourceURL)
			continue
		}
		if !known[a.Evidence.ObservationHash] {
			t.Errorf("assertion %q on %s names observation %s, which is not in the corpus",
				a.Field, a.SourceURL, a.Evidence.ObservationHash)
		}
	}
}

// TestAssertionsCarryTheirMethod checks that a claim says how it was made.
//
// Without it an assertion is an Item field wearing a longer struct: the point of
// the model is that a consumer can tell a CSS selector result from a model's guess
// and treat them differently.
func TestAssertionsCarryTheirMethod(t *testing.T) {
	_, _, assertions := replayCorpus(t)

	methods := map[string]int{}
	for _, a := range assertions {
		if a.Method == "" {
			t.Errorf("assertion %q on %s has no method", a.Field, a.SourceURL)
		}
		if a.MethodVersion == "" {
			t.Errorf("assertion %q on %s has no method version", a.Field, a.SourceURL)
		}
		methods[strings.SplitN(a.Method, ":", 2)[0]]++
	}

	// The corpus exercises selector rules and the page's own declarations; if one
	// of those stopped producing assertions the projection test above would still
	// pass, because it compares against an item derived from the same gap.
	for _, want := range []string{"css", "structured"} {
		if methods[want] == 0 {
			t.Errorf("no assertions were produced by %q derivations; got %v", want, methods)
		}
	}
}

// TestEvidenceSpansReVerifyOffline is the promise the model is sold on.
//
// A corpus row says "this value, from these bytes, at this range". The claim is
// that anyone holding the fetch log can check that years later without the
// extractor, the model, or the network. So this does exactly that: for every
// grounded claim in the corpus, it reads the stored body by hash, cuts the span,
// and checks the cut renders to the value.
//
// A span that does not is worse than no span. It is a citation that looks like
// evidence and points at the wrong text, and nothing downstream could tell.
func TestEvidenceSpansReVerifyOffline(t *testing.T) {
	_, _, assertions := replayCorpus(t)

	store, err := fetchlog.NewStore(corpusDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	checked := 0
	for _, a := range assertions {
		if !a.Validated {
			continue
		}
		value, ok := a.Value.(string)
		if !ok {
			t.Errorf("%s: %q is validated but its value is %T, not a string", a.SourceURL, a.Field, a.Value)
			continue
		}

		body, err := store.Get(a.Evidence.ObservationHash)
		if err != nil {
			t.Errorf("%s: %q names observation %s, which the store does not hold: %v",
				a.SourceURL, a.Field, a.Evidence.ObservationHash, err)
			continue
		}

		s, e := a.Evidence.ByteStart, a.Evidence.ByteEnd
		if s < 0 || e <= s || e > len(body) {
			t.Errorf("%s: %q has span [%d,%d) against a %d-byte body", a.SourceURL, a.Field, s, e, len(body))
			continue
		}

		if !provenance.SpanSupports(body[s:e], value) {
			t.Errorf("%s: %q claims %q but its span cuts %q, which does not render to it",
				a.SourceURL, a.Field, truncate(value, 60), truncate(string(body[s:e]), 90))
			continue
		}
		checked++
	}

	if checked == 0 {
		t.Fatal("no grounded claims to verify; evidence spans are not being written")
	}
	t.Logf("re-verified %d evidence spans against the stored bodies", checked)
}

// TestEveryDerivationPathIsGrounded guards the property step by step: it is not
// enough that most claims carry evidence, because the ones that quietly stopped
// would be exactly the ones nobody looks at.
func TestEveryDerivationPathIsGrounded(t *testing.T) {
	_, _, assertions := replayCorpus(t)

	type stat struct{ grounded, total int }
	byFamily := map[string]*stat{}
	for _, a := range assertions {
		family := strings.SplitN(a.Method, ":", 2)[0]
		s, ok := byFamily[family]
		if !ok {
			s = &stat{}
			byFamily[family] = s
		}
		s.total++
		if a.Validated {
			s.grounded++
		}
	}

	// Every derivation the corpus exercises. A selector result is as traceable as
	// anything else, which is the whole point of doing this for all of them rather
	// than only the ones a model touched.
	for _, family := range []string{"css", "structured", "density"} {
		s := byFamily[family]
		if s == nil || s.total == 0 {
			t.Errorf("no %q derivations in the corpus; this path is untested", family)
			continue
		}
		if s.grounded != s.total {
			t.Errorf("%s: %d of %d claims carry evidence", family, s.grounded, s.total)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
