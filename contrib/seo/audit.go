package seo

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// --- Meta Tag Auditor ---

// MetaAuditResult holds an SEO audit for a page.
type MetaAuditResult struct {
	URL    string            `json:"url"`
	Score  int               `json:"score"` // 0-100
	Issues []AuditIssue      `json:"issues"`
	Tags   map[string]string `json:"tags"`
}

// AuditIssue represents a single SEO issue.
type AuditIssue struct {
	Severity string `json:"severity"` // error, warning, info
	Category string `json:"category"`
	Message  string `json:"message"`
}

// MetaAuditor audits pages for SEO best practices.
type MetaAuditor struct {
	logger *slog.Logger
}

// NewMetaAuditor creates a new meta tag auditor.
func NewMetaAuditor(logger *slog.Logger) *MetaAuditor {
	return &MetaAuditor{logger: logger.With("component", "meta_auditor")}
}

// Audit performs an SEO audit on a response.
func (ma *MetaAuditor) Audit(resp *types.Response) (*MetaAuditResult, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, err
	}

	result := &MetaAuditResult{
		URL:  resp.Request.URLString(),
		Tags: make(map[string]string),
	}

	score := 100

	// Title
	title := strings.TrimSpace(doc.Find("title").First().Text())
	result.Tags["title"] = title
	switch {
	case title == "":
		result.Issues = append(result.Issues, AuditIssue{"error", "title", "Missing title tag"})
		score -= 20
	case len(title) > 60:
		result.Issues = append(result.Issues, AuditIssue{"warning", "title", fmt.Sprintf("Title too long (%d chars, max 60)", len(title))})
		score -= 5
	case len(title) < 10:
		result.Issues = append(result.Issues, AuditIssue{"warning", "title", "Title too short"})
		score -= 5
	}

	// Meta description
	desc := metaContent(doc, "description")
	result.Tags["description"] = desc
	if desc == "" {
		result.Issues = append(result.Issues, AuditIssue{"error", "description", "Missing meta description"})
		score -= 15
	} else if len(desc) > 160 {
		result.Issues = append(result.Issues, AuditIssue{"warning", "description", fmt.Sprintf("Description too long (%d chars, max 160)", len(desc))})
		score -= 5
	}

	// Canonical
	canonical, _ := doc.Find(`link[rel="canonical"]`).Attr("href")
	result.Tags["canonical"] = canonical
	if canonical == "" {
		result.Issues = append(result.Issues, AuditIssue{"warning", "canonical", "Missing canonical URL"})
		score -= 5
	}

	// H1 tag
	h1Count := doc.Find("h1").Length()
	if h1Count == 0 {
		result.Issues = append(result.Issues, AuditIssue{"error", "headings", "Missing H1 tag"})
		score -= 10
	} else if h1Count > 1 {
		result.Issues = append(result.Issues, AuditIssue{"warning", "headings", fmt.Sprintf("Multiple H1 tags (%d)", h1Count)})
		score -= 5
	}

	// OpenGraph
	ogTitle := metaProperty(doc, "og:title")
	result.Tags["og:title"] = ogTitle
	if ogTitle == "" {
		result.Issues = append(result.Issues, AuditIssue{"info", "opengraph", "Missing og:title"})
		score -= 3
	}

	ogImage := metaProperty(doc, "og:image")
	result.Tags["og:image"] = ogImage
	if ogImage == "" {
		result.Issues = append(result.Issues, AuditIssue{"info", "opengraph", "Missing og:image"})
		score -= 3
	}

	// Images without alt text
	imgNoAlt := 0
	doc.Find("img").Each(func(i int, sel *goquery.Selection) {
		alt, exists := sel.Attr("alt")
		if !exists || strings.TrimSpace(alt) == "" {
			imgNoAlt++
		}
	})
	if imgNoAlt > 0 {
		result.Issues = append(result.Issues, AuditIssue{"warning", "images", fmt.Sprintf("%d images without alt text", imgNoAlt)})
		score -= min(imgNoAlt*2, 10)
	}

	// Robots
	robots := metaContent(doc, "robots")
	result.Tags["robots"] = robots
	if strings.Contains(robots, "noindex") {
		result.Issues = append(result.Issues, AuditIssue{"warning", "robots", "Page is set to noindex"})
	}

	// Viewport
	viewport := metaContent(doc, "viewport")
	result.Tags["viewport"] = viewport
	if viewport == "" {
		result.Issues = append(result.Issues, AuditIssue{"warning", "mobile", "Missing viewport meta tag"})
		score -= 5
	}

	if score < 0 {
		score = 0
	}
	result.Score = score

	return result, nil
}

// --- Backlink Analyzer ---

// Backlink represents a discovered backlink.
type Backlink struct {
	SourceURL  string `json:"source_url"`
	TargetURL  string `json:"target_url"`
	AnchorText string `json:"anchor_text"`
	NoFollow   bool   `json:"nofollow"`
	External   bool   `json:"external"`
}

// ExtractBacklinks extracts all outgoing links from a page.
func ExtractBacklinks(resp *types.Response) ([]Backlink, error) {
	doc, err := resp.Document()
	if err != nil {
		return nil, err
	}

	sourceURL := resp.Request.URLString()
	sourceParsed, _ := url.Parse(sourceURL)

	var backlinks []Backlink

	doc.Find("a[href]").Each(func(i int, sel *goquery.Selection) {
		href, _ := sel.Attr("href")
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") {
			return
		}

		parsed, err := url.Parse(href)
		if err != nil {
			return
		}
		resolved := sourceParsed.ResolveReference(parsed)

		rel, _ := sel.Attr("rel")
		nofollow := strings.Contains(rel, "nofollow")
		external := resolved.Host != sourceParsed.Host

		backlinks = append(backlinks, Backlink{
			SourceURL:  sourceURL,
			TargetURL:  resolved.String(),
			AnchorText: strings.TrimSpace(sel.Text()),
			NoFollow:   nofollow,
			External:   external,
		})
	})

	return backlinks, nil
}

// --- Price Tracker ---

// PricePoint represents a price observation.
type PricePoint struct {
	URL       string    `json:"url"`
	ProductID string    `json:"product_id"`
	Price     string    `json:"price"`
	Currency  string    `json:"currency"`
	Available bool      `json:"available"`
	Timestamp time.Time `json:"timestamp"`
}

func metaContent(doc *goquery.Document, name string) string {
	content, _ := doc.Find(fmt.Sprintf(`meta[name="%s"]`, name)).Attr("content")
	return content
}

func metaProperty(doc *goquery.Document, property string) string {
	content, _ := doc.Find(fmt.Sprintf(`meta[property="%s"]`, property)).Attr("content")
	return content
}
