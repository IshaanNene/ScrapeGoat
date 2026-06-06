// Search Engine Crawler — indexes pages with full text, metadata, and link graph
// Produces a search-engine-style index: title, URL, body text, meta, headings, links
package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"time"

	scrapegoat "github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat"
	"github.com/PuerkitoBio/goquery"
)

func main() {
	fmt.Println("🔍 ScrapeGoat Search Engine Crawler")
	fmt.Println("   Indexing pages: title, body text, headings, meta, link graph")
	fmt.Println()

	if len(os.Args) < 2 {
		fmt.Println("Usage: go run ./examples/search/ <url> [url2] [url3] ...")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  go run ./examples/search/ https://go.dev")
		fmt.Println("  go run ./examples/search/ https://en.wikipedia.org/wiki/Web_scraping")
		os.Exit(1)
	}

	seeds := os.Args[1:]

	crawler := scrapegoat.NewCrawler(
		scrapegoat.WithConcurrency(10),
		scrapegoat.WithMaxDepth(3),
		scrapegoat.WithDelay(200*time.Millisecond),
		scrapegoat.WithOutput("jsonl", "./output/search_index"),
		scrapegoat.WithMaxRequests(500),
		scrapegoat.WithRobotsRespect(true),
	)

	// Follow all internal links for discovery
	crawler.OnHTML("a[href]", func(e *scrapegoat.Element) {
		href := e.Attr("href")
		if href != "" && !strings.HasPrefix(href, "#") && !strings.HasPrefix(href, "javascript:") && !strings.HasPrefix(href, "mailto:") {
			e.Follow(href)
		}
	})

	// Index the entire page
	crawler.OnHTML("html", func(e *scrapegoat.Element) {
		url := e.Response.Request.URLString()

		// Title
		title := strings.TrimSpace(e.Selection.Find("title").First().Text())

		// Meta description
		desc, _ := e.Selection.Find("meta[name='description']").Attr("content")

		// Meta keywords
		keywords, _ := e.Selection.Find("meta[name='keywords']").Attr("content")

		// Canonical URL
		canonical, _ := e.Selection.Find("link[rel='canonical']").Attr("href")

		// Language
		lang, _ := e.Selection.Find("html").Attr("lang")

		// Headings
		var h1s, h2s, h3s []string
		e.Selection.Find("h1").Each(func(_ int, s *goquery.Selection) {
			if t := strings.TrimSpace(s.Text()); t != "" {
				h1s = append(h1s, t)
			}
		})
		e.Selection.Find("h2").Each(func(_ int, s *goquery.Selection) {
			if t := strings.TrimSpace(s.Text()); t != "" {
				h2s = append(h2s, t)
			}
		})
		e.Selection.Find("h3").Each(func(_ int, s *goquery.Selection) {
			if t := strings.TrimSpace(s.Text()); t != "" {
				h3s = append(h3s, t)
			}
		})

		// Cleaned body text — remove non-content elements
		bodyClone := e.Selection.Find("body").Clone()
		bodyClone.Find("script, style, nav, footer, header, aside, .sidebar, .menu, .nav, .cookie, .popup").Remove()
		bodyText := strings.TrimSpace(bodyClone.Text())
		fields := strings.Fields(bodyText)
		bodyText = strings.Join(fields, " ")
		if len(bodyText) > 5000 {
			bodyText = bodyText[:5000] + "..."
		}
		wordCount := len(fields)

		// Outbound links count
		linkCount := e.Selection.Find("a[href]").Length()

		// Image count
		imgCount := e.Selection.Find("img").Length()

		// Content hash for deduplication
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(bodyText)))[:16]

		if title != "" || wordCount > 10 {
			e.Item.Set("url", url)
			e.Item.Set("title", title)
			e.Item.Set("description", desc)
			e.Item.Set("keywords", keywords)
			e.Item.Set("canonical", canonical)
			e.Item.Set("language", lang)
			e.Item.Set("h1", h1s)
			e.Item.Set("h2", h2s)
			e.Item.Set("h3", h3s)
			e.Item.Set("body_text", bodyText)
			e.Item.Set("word_count", wordCount)
			e.Item.Set("outbound_links", linkCount)
			e.Item.Set("images", imgCount)
			e.Item.Set("content_hash", hash)
			e.Item.Set("indexed_at", time.Now().Format(time.RFC3339))
		}
	})

	fmt.Printf("   Seeds: %v\n", seeds)
	fmt.Printf("   Config: 10 workers, depth 3, max 500 pages\n\n")

	if err := crawler.Start(seeds...); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	crawler.Wait()

	stats := crawler.Stats()
	fmt.Printf("\n✅ Search index built!\n")
	fmt.Printf("   Pages indexed: %v\n", stats["items_scraped"])
	fmt.Printf("   Pages crawled: %v\n", stats["requests_sent"])
	fmt.Printf("   Data downloaded: %v bytes\n", stats["bytes_downloaded"])
	fmt.Printf("   Output: ./output/search_index/results.jsonl\n")
}
