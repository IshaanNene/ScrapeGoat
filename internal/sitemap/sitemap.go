// Package sitemap discovers and parses sitemap.xml.
//
// Split out of the former internal/seo when the SEO auditor moved to contrib/:
// sitemap discovery is part of crawling — it is how a crawler finds URLs it would
// not reach by following links — whereas auditing a page's meta tags is a
// different product that happens to parse the same HTML.
package sitemap

import (
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/safety"
)

// --- Sitemap Crawler ---

// URL represents a URL entry from a sitemap.
type URL struct {
	Loc        string  `xml:"loc" json:"loc"`
	LastMod    string  `xml:"lastmod,omitempty" json:"lastmod,omitempty"`
	ChangeFreq string  `xml:"changefreq,omitempty" json:"changefreq,omitempty"`
	Priority   float64 `xml:"priority,omitempty" json:"priority,omitempty"`
}

// Sitemap represents a parsed sitemap.
type Sitemap struct {
	URLs     []URL `xml:"url" json:"urls"`
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap" json:"sitemaps"`
}

// Crawler fetches and parses sitemaps.
type Crawler struct {
	client *http.Client
	logger *slog.Logger
}

// NewCrawler creates a new sitemap crawler.
//
// The client is guarded: sitemap URLs come from the same untrusted places as crawl
// URLs (an MCP tool argument, a REST request body), so an unguarded client here
// would be an SSRF bypass around the fetcher's guard.
func New(logger *slog.Logger) *Crawler {
	return &Crawler{
		client: safety.Default().HTTPClient(30*time.Second, 10),
		logger: logger.With("component", "sitemap_crawler"),
	}
}

// Crawl fetches and parses a sitemap, recursively following sitemap indexes.
func (sc *Crawler) Crawl(sitemapURL string) ([]URL, error) {
	sc.logger.Info("crawling sitemap", "url", sitemapURL)

	resp, err := sc.client.Get(sitemapURL)
	if err != nil {
		return nil, fmt.Errorf("fetch sitemap: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read sitemap: %w", err)
	}

	var sitemap Sitemap
	if err := xml.Unmarshal(body, &sitemap); err != nil {
		return nil, fmt.Errorf("parse sitemap: %w", err)
	}

	var allURLs []URL
	allURLs = append(allURLs, sitemap.URLs...)

	// Recursively fetch sub-sitemaps
	for _, sub := range sitemap.Sitemaps {
		subURLs, err := sc.Crawl(sub.Loc)
		if err != nil {
			sc.logger.Warn("sub-sitemap error", "url", sub.Loc, "error", err)
			continue
		}
		allURLs = append(allURLs, subURLs...)
	}

	sc.logger.Info("sitemap crawled", "url", sitemapURL, "urls", len(allURLs))
	return allURLs, nil
}

// DiscoverSitemap finds the sitemap URL for a domain.
func (sc *Crawler) DiscoverSitemap(domain string) string {
	candidates := []string{
		"https://" + domain + "/sitemap.xml",
		"https://" + domain + "/sitemap_index.xml",
		"https://" + domain + "/sitemap.xml.gz",
	}

	for _, u := range candidates {
		resp, err := sc.client.Head(u)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return u
		}
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
	}
	return ""
}
