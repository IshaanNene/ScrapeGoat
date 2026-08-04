package provenance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ComplianceReportVersion is the version of the report shape. Versioned for the
// same reason the record is: a report is an artefact someone keeps, and it has to
// stay legible after the code that wrote it has moved on.
const ComplianceReportVersion = 1

// ComplianceReport is a machine-readable account of what a crawl respected, what
// it skipped, and why.
//
// The point is auditability, so it is written to be read adversarially. Every
// number here is one a sceptical reader might check against the corpus, and the
// figures are derived from the corpus rather than tallied independently — a
// report that counted separately could disagree with the data it describes, and
// then neither would be worth anything.
//
// It records what happened. It does not certify that what happened was right: no
// field in here says "compliant", because that is a judgement about a particular
// use in a particular jurisdiction, and a crawler is not in a position to make it.
type ComplianceReport struct {
	Version int `json:"version"`

	GeneratedAt time.Time `json:"generated_at"`
	CrawlID     string    `json:"crawl_id,omitempty"`
	Corpus      string    `json:"corpus,omitempty"`

	// Tool identifies what produced the report, since a report outlives the run.
	Tool string `json:"tool"`

	Totals ComplianceTotals `json:"totals"`

	// Restricted is the part that matters: every record whose source asked to be
	// left out, listed rather than counted. A number can be dismissed; a list can
	// be checked, and the person defending the dataset needs to be able to say
	// exactly which pages are in question.
	Restricted []RestrictedRecord `json:"restricted,omitempty"`

	// SitesBlockingAI groups the AI directives by host, because a corpus of a
	// hundred thousand pages from one site should not produce a hundred thousand
	// identical lines.
	SitesBlockingAI []SiteDirectives `json:"sites_blocking_ai,omitempty"`

	Licences map[string]int `json:"licences,omitempty"`

	// Warnings names anything that should not have happened. Empty is the expected
	// state, and a non-empty list is the reason to read the report at all.
	Warnings []string `json:"warnings,omitempty"`
}

// ComplianceTotals is the summary a reader sees first.
type ComplianceTotals struct {
	Records int `json:"records"`
	Sites   int `json:"sites"`

	// RobotsAllowed and RobotsDisallowed must sum to Records. Disallowed should be
	// zero for any crawl that respected robots.txt; it is reported rather than
	// assumed, because the assumption is exactly what an audit exists to test.
	RobotsAllowed    int `json:"robots_allowed"`
	RobotsDisallowed int `json:"robots_disallowed"`

	// Restricted counts records whose source asked to be excluded from AI training,
	// by page signal or by a site-wide directive.
	Restricted int `json:"restricted"`

	// The breakdown, which overlaps: one page can carry several of these.
	NoAI              int `json:"noai"`
	NoImageAI         int `json:"noimageai"`
	TDMReserved       int `json:"tdm_reserved"`
	TDMPermitted      int `json:"tdm_permitted"`
	TDMUnstated       int `json:"tdm_unstated"`
	SiteWideAIBlocked int `json:"site_wide_ai_blocked"`

	Licensed          int `json:"licensed"`
	RobotsTxtSeen     int `json:"robots_txt_seen"`
	RobotsTxtNotFound int `json:"robots_txt_not_found"`
}

// RestrictedRecord names one page whose source asked to be left out, and why.
type RestrictedRecord struct {
	URL string `json:"url"`

	// Reasons is every applicable ground, not just the first. A page carrying both
	// a noai tag and a TDM reservation has said no twice, and collapsing that to
	// one reason would understate it.
	Reasons []string `json:"reasons"`

	ContentHash string `json:"content_hash,omitempty"`
}

// SiteDirectives is what one host's robots.txt said to AI crawlers.
type SiteDirectives struct {
	Host           string   `json:"host"`
	AgentsBlocked  []string `json:"agents_blocked,omitempty"`
	VendorsBlocked []string `json:"vendors_blocked,omitempty"`
	Records        int      `json:"records"`
}

// Reasons a record can be restricted. Stable strings, because a report is
// consumed by machines as well as read by people.
const (
	ReasonNoAI           = "page-noai"
	ReasonNoImageAI      = "page-noimageai"
	ReasonTDMReservation = "tdm-reservation"
	ReasonSiteWideAI     = "robots-blocks-ai-crawlers"
)

// BuildComplianceReport derives a report from a corpus.
//
// maxListed caps the Restricted list so a report over a very large corpus stays a
// document rather than a second copy of the data; zero means no cap. When the cap
// bites, a warning says so, because a silently truncated audit is worse than a
// long one.
func BuildComplianceReport(records []Record, crawlID, corpusPath string, maxListed int) ComplianceReport {
	report := ComplianceReport{
		Version:     ComplianceReportVersion,
		GeneratedAt: time.Now().UTC(),
		CrawlID:     crawlID,
		Corpus:      corpusPath,
		Tool:        "scrapegoat",
		Licences:    map[string]int{},
	}

	sites := map[string]*SiteDirectives{}
	hosts := map[string]bool{}

	for _, r := range records {
		report.Totals.Records++

		host := hostOf(r.URL)
		if host != "" {
			hosts[host] = true
		}

		if r.RobotsAllowed {
			report.Totals.RobotsAllowed++
		} else {
			report.Totals.RobotsDisallowed++
		}

		switch {
		case r.AIDirectives == nil:
			report.Totals.RobotsTxtNotFound++
		case r.AIDirectives.RobotsPresent:
			report.Totals.RobotsTxtSeen++
		default:
			report.Totals.RobotsTxtNotFound++
		}

		if r.Signals.Licence != "" {
			report.Totals.Licensed++
			report.Licences[r.Signals.Licence]++
		}

		switch {
		case r.Signals.TDMReservation == nil:
			report.Totals.TDMUnstated++
		case *r.Signals.TDMReservation == 1:
			report.Totals.TDMReserved++
		default:
			report.Totals.TDMPermitted++
		}

		var reasons []string
		if r.Signals.NoAI {
			report.Totals.NoAI++
			reasons = append(reasons, ReasonNoAI)
		}
		if r.Signals.NoImageAI {
			report.Totals.NoImageAI++
			reasons = append(reasons, ReasonNoImageAI)
		}
		if r.Signals.TDMReservation != nil && *r.Signals.TDMReservation == 1 {
			reasons = append(reasons, ReasonTDMReservation)
		}

		if r.AIDirectives != nil && len(r.AIDirectives.AgentsBlocked) > 0 {
			report.Totals.SiteWideAIBlocked++
			reasons = append(reasons, ReasonSiteWideAI)

			if host != "" {
				s, ok := sites[host]
				if !ok {
					s = &SiteDirectives{
						Host:           host,
						AgentsBlocked:  r.AIDirectives.AgentsBlocked,
						VendorsBlocked: r.AIDirectives.VendorsBlocked,
					}
					sites[host] = s
				}
				s.Records++
			}
		}

		if len(reasons) > 0 {
			report.Totals.Restricted++
			if maxListed == 0 || len(report.Restricted) < maxListed {
				report.Restricted = append(report.Restricted, RestrictedRecord{
					URL:         r.URL,
					Reasons:     reasons,
					ContentHash: r.ContentHash,
				})
			}
		}
	}

	report.Totals.Sites = len(hosts)

	for _, s := range sites {
		report.SitesBlockingAI = append(report.SitesBlockingAI, *s)
	}
	sort.Slice(report.SitesBlockingAI, func(i, j int) bool {
		return report.SitesBlockingAI[i].Host < report.SitesBlockingAI[j].Host
	})

	// Sorted so that two reports over the same corpus are identical files. An audit
	// artefact that differed run to run would be impossible to diff, which is most
	// of what anyone wants to do with one.
	sort.Slice(report.Restricted, func(i, j int) bool {
		return report.Restricted[i].URL < report.Restricted[j].URL
	})

	report.Warnings = complianceWarnings(report, maxListed)
	return report
}

// complianceWarnings names anything that should not have happened.
func complianceWarnings(r ComplianceReport, maxListed int) []string {
	var w []string

	if n := r.Totals.RobotsDisallowed; n > 0 {
		w = append(w, fmt.Sprintf(
			"%d records were fetched despite robots.txt disallowing them — the crawl did not respect robots.txt, or the records are mislabelled", n))
	}

	if maxListed > 0 && r.Totals.Restricted > len(r.Restricted) {
		w = append(w, fmt.Sprintf(
			"restricted list truncated to %d of %d; re-run with a higher limit for the full list",
			len(r.Restricted), r.Totals.Restricted))
	}

	if r.Totals.Records == 0 {
		w = append(w, "the corpus is empty")
	}

	return w
}

// hostOf extracts the host from a URL without failing on a malformed one — a
// report should degrade rather than refuse.
func hostOf(rawURL string) string {
	rest := rawURL
	for _, prefix := range []string{"https://", "http://"} {
		if len(rest) >= len(prefix) && rest[:len(prefix)] == prefix {
			rest = rest[len(prefix):]
			break
		}
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' || rest[i] == '?' || rest[i] == '#' {
			return rest[:i]
		}
	}
	return rest
}

// WriteComplianceReport writes a report as indented JSON.
func WriteComplianceReport(path string, r ComplianceReport) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("provenance: create report dir: %w", err)
		}
	}

	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("provenance: encode report: %w", err)
	}
	b = append(b, '\n')

	// Temp-and-rename, as elsewhere: a half-written audit artefact that looks whole
	// is worse than none.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("provenance: write report: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("provenance: commit report: %w", err)
	}
	return nil
}

// ReadComplianceReport loads a report.
func ReadComplianceReport(path string) (ComplianceReport, error) {
	var r ComplianceReport

	b, err := os.ReadFile(path)
	if err != nil {
		return r, fmt.Errorf("provenance: read report: %w", err)
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("provenance: parse report: %w", err)
	}
	return r, nil
}
