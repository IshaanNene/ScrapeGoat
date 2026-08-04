package provenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func restrictedRecord(url string, noai bool, tdm *int, robots string) Record {
	r := sampleRecord(url)
	r.Signals.NoAI = noai
	r.Signals.TDMReservation = tdm
	if robots != "" {
		r.AIDirectives = SummariseDirectives(ParseRobots(robots))
	}
	return r
}

func TestComplianceTotalsAddUp(t *testing.T) {
	one, zero := 1, 0

	records := []Record{
		sampleRecord("https://a.example/1"),
		restrictedRecord("https://a.example/2", true, nil, ""),
		restrictedRecord("https://b.example/1", false, &one, ""),
		func() Record {
			r := sampleRecord("https://c.example/1")
			r.Signals.TDMReservation = &zero
			return r
		}(),
		func() Record {
			r := sampleRecord("https://d.example/1")
			r.RobotsAllowed = false
			return r
		}(),
	}

	rep := BuildComplianceReport(records, "crawl-1", "corpus.jsonl", 0)

	if rep.Totals.Records != 5 {
		t.Errorf("records = %d", rep.Totals.Records)
	}
	// The invariant a sceptical reader checks first.
	if got := rep.Totals.RobotsAllowed + rep.Totals.RobotsDisallowed; got != rep.Totals.Records {
		t.Errorf("robots allowed+disallowed = %d, want %d", got, rep.Totals.Records)
	}
	// And the second one: every record has exactly one TDM state.
	tdm := rep.Totals.TDMReserved + rep.Totals.TDMPermitted + rep.Totals.TDMUnstated
	if tdm != rep.Totals.Records {
		t.Errorf("TDM states sum to %d, want %d", tdm, rep.Totals.Records)
	}
	if rep.Totals.TDMReserved != 1 || rep.Totals.TDMPermitted != 1 || rep.Totals.TDMUnstated != 3 {
		t.Errorf("TDM breakdown = %d/%d/%d", rep.Totals.TDMReserved, rep.Totals.TDMPermitted, rep.Totals.TDMUnstated)
	}
	if rep.Totals.Sites != 4 {
		t.Errorf("sites = %d, want 4", rep.Totals.Sites)
	}
	if rep.Totals.Restricted != 2 {
		t.Errorf("restricted = %d, want 2", rep.Totals.Restricted)
	}
	if rep.Version != ComplianceReportVersion {
		t.Errorf("version = %d", rep.Version)
	}
}

// A page that said no twice has said no twice. Collapsing to one reason would
// understate what the source asked for.
func TestComplianceRecordsEveryReason(t *testing.T) {
	one := 1
	r := restrictedRecord("https://a.example/1", true, &one, "User-agent: GPTBot\nDisallow: /\n")
	r.Signals.NoImageAI = true

	rep := BuildComplianceReport([]Record{r}, "", "", 0)

	if len(rep.Restricted) != 1 {
		t.Fatalf("restricted = %d", len(rep.Restricted))
	}
	got := strings.Join(rep.Restricted[0].Reasons, ",")
	for _, want := range []string{ReasonNoAI, ReasonNoImageAI, ReasonTDMReservation, ReasonSiteWideAI} {
		if !strings.Contains(got, want) {
			t.Errorf("missing reason %q in %q", want, got)
		}
	}
}

// The list is what makes the report auditable. A number can be dismissed; a list
// can be checked against the corpus.
func TestComplianceListsRestrictedURLs(t *testing.T) {
	rep := BuildComplianceReport([]Record{
		restrictedRecord("https://a.example/z", true, nil, ""),
		restrictedRecord("https://a.example/a", true, nil, ""),
		sampleRecord("https://a.example/ok"),
	}, "", "", 0)

	if len(rep.Restricted) != 2 {
		t.Fatalf("restricted list = %d entries", len(rep.Restricted))
	}
	// Sorted, so two reports over one corpus are the same file and can be diffed.
	if rep.Restricted[0].URL > rep.Restricted[1].URL {
		t.Errorf("restricted list is not sorted: %v", rep.Restricted)
	}
	if rep.Restricted[0].ContentHash == "" {
		t.Error("restricted entries carry no content hash to check against the corpus")
	}
}

// A silently truncated audit is worse than a long one.
func TestComplianceWarnsWhenTruncated(t *testing.T) {
	var records []Record
	for i := 0; i < 10; i++ {
		records = append(records, restrictedRecord("https://a.example/"+itoa(i), true, nil, ""))
	}

	rep := BuildComplianceReport(records, "", "", 3)

	if len(rep.Restricted) != 3 {
		t.Errorf("listed %d, want 3", len(rep.Restricted))
	}
	if rep.Totals.Restricted != 10 {
		t.Errorf("total restricted = %d, want the true count of 10", rep.Totals.Restricted)
	}
	if !hasWarning(rep, "truncated") {
		t.Errorf("no truncation warning: %v", rep.Warnings)
	}
}

// The loudest possible signal that something is wrong.
func TestComplianceWarnsOnRobotsViolation(t *testing.T) {
	r := sampleRecord("https://a.example/1")
	r.RobotsAllowed = false

	rep := BuildComplianceReport([]Record{r}, "", "", 0)

	if !hasWarning(rep, "despite robots.txt") {
		t.Errorf("a robots violation produced no warning: %v", rep.Warnings)
	}
}

func TestComplianceCleanCrawlHasNoWarnings(t *testing.T) {
	rep := BuildComplianceReport([]Record{
		sampleRecord("https://a.example/1"),
		restrictedRecord("https://a.example/2", true, nil, ""),
	}, "", "", 0)

	if len(rep.Warnings) != 0 {
		t.Errorf("a clean crawl produced warnings: %v", rep.Warnings)
	}
}

func TestComplianceWarnsOnEmptyCorpus(t *testing.T) {
	rep := BuildComplianceReport(nil, "", "", 0)
	if !hasWarning(rep, "empty") {
		t.Errorf("an empty corpus produced no warning: %v", rep.Warnings)
	}
}

// A hundred thousand pages from one site must not produce a hundred thousand
// identical lines.
func TestComplianceGroupsSitesBlockingAI(t *testing.T) {
	var records []Record
	for i := 0; i < 50; i++ {
		records = append(records, restrictedRecord(
			"https://blocked.example/"+itoa(i), false, nil, "User-agent: GPTBot\nDisallow: /\n"))
	}
	records = append(records, restrictedRecord(
		"https://other.example/1", false, nil, "User-agent: CCBot\nDisallow: /\n"))

	rep := BuildComplianceReport(records, "", "", 0)

	if len(rep.SitesBlockingAI) != 2 {
		t.Fatalf("sites = %d, want 2: %+v", len(rep.SitesBlockingAI), rep.SitesBlockingAI)
	}
	// Sorted by host, so the report diffs cleanly.
	if rep.SitesBlockingAI[0].Host != "blocked.example" {
		t.Errorf("sites not sorted: %v", rep.SitesBlockingAI)
	}
	if rep.SitesBlockingAI[0].Records != 50 {
		t.Errorf("record count for the blocked site = %d, want 50", rep.SitesBlockingAI[0].Records)
	}
	if len(rep.SitesBlockingAI[0].VendorsBlocked) != 1 || rep.SitesBlockingAI[0].VendorsBlocked[0] != "OpenAI" {
		t.Errorf("vendors = %v", rep.SitesBlockingAI[0].VendorsBlocked)
	}
}

// A record with no robots.txt must not be counted as having seen one.
func TestComplianceDistinguishesRobotsPresence(t *testing.T) {
	rep := BuildComplianceReport([]Record{
		sampleRecord("https://none.example/1"), // no AIDirectives at all
		func() Record {
			r := sampleRecord("https://empty.example/1")
			r.AIDirectives = SummariseDirectives(ParseRobots(""))
			return r
		}(),
	}, "", "", 0)

	if rep.Totals.RobotsTxtSeen != 1 {
		t.Errorf("robots seen = %d, want 1", rep.Totals.RobotsTxtSeen)
	}
	if rep.Totals.RobotsTxtNotFound != 1 {
		t.Errorf("robots not found = %d, want 1", rep.Totals.RobotsTxtNotFound)
	}
}

func TestComplianceReportRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compliance.json")
	one := 1

	rep := BuildComplianceReport([]Record{
		restrictedRecord("https://a.example/1", true, &one, "User-agent: GPTBot\nDisallow: /\n"),
		sampleRecord("https://a.example/2"),
	}, "crawl-1", "corpus.parquet", 0)

	if err := WriteComplianceReport(path, rep); err != nil {
		t.Fatalf("WriteComplianceReport: %v", err)
	}

	back, err := ReadComplianceReport(path)
	if err != nil {
		t.Fatalf("ReadComplianceReport: %v", err)
	}

	if back.Version != rep.Version || back.CrawlID != "crawl-1" || back.Corpus != "corpus.parquet" {
		t.Errorf("identity did not survive: %+v", back)
	}
	if back.Totals.Records != 2 || back.Totals.Restricted != 1 {
		t.Errorf("totals did not survive: %+v", back.Totals)
	}
	// Three grounds: the noai tag, the TDM reservation, and the site-wide block.
	if len(back.Restricted) != 1 || len(back.Restricted[0].Reasons) != 3 {
		t.Errorf("restricted entries did not survive: %+v", back.Restricted)
	}
	if len(back.SitesBlockingAI) != 1 {
		t.Errorf("site directives did not survive: %+v", back.SitesBlockingAI)
	}

	// No temp file left behind.
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("a .tmp report survived")
	}
}

// Two reports over the same corpus must be the same file, or the artefact cannot
// be diffed — which is most of what anyone does with an audit.
func TestComplianceReportIsStable(t *testing.T) {
	records := []Record{
		restrictedRecord("https://z.example/1", true, nil, "User-agent: GPTBot\nDisallow: /\n"),
		restrictedRecord("https://a.example/1", true, nil, "User-agent: CCBot\nDisallow: /\n"),
		sampleRecord("https://m.example/1"),
	}

	first := BuildComplianceReport(records, "c", "p", 0)
	second := BuildComplianceReport(records, "c", "p", 0)

	if len(first.Restricted) != len(second.Restricted) {
		t.Fatal("restricted lists differ in length")
	}
	for i := range first.Restricted {
		if first.Restricted[i].URL != second.Restricted[i].URL {
			t.Errorf("restricted order differs at %d: %s vs %s",
				i, first.Restricted[i].URL, second.Restricted[i].URL)
		}
	}
	for i := range first.SitesBlockingAI {
		if first.SitesBlockingAI[i].Host != second.SitesBlockingAI[i].Host {
			t.Errorf("site order differs at %d", i)
		}
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://example.com/a/b?c=d": "example.com",
		"http://example.com":          "example.com",
		"https://example.com:8080/x":  "example.com:8080",
		"example.com/x":               "example.com",
		"":                            "",
	}
	for in, want := range cases {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func hasWarning(r ComplianceReport, substr string) bool {
	for _, w := range r.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
