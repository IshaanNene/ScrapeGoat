// LinkedIn Public Profile Scraper Example
//
// Demonstrates scraping publicly available LinkedIn profile information.
// This is for educational purposes — always respect robots.txt and ToS.
//
// Usage:
//
//	go run main.go
package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	scrapegoat "github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat"
)

// LinkedInSpider scrapes public LinkedIn profile data.
// Note: This scrapes a demo/example page format. Real LinkedIn scraping
// requires authentication and must comply with their Terms of Service.
type LinkedInSpider struct{}

func (s *LinkedInSpider) Name() string { return "linkedin_scraper" }

func (s *LinkedInSpider) StartURLs() []string {
	// Example: Use a placeholder page that mimics LinkedIn's structure
	return []string{
		"https://quotes.toscrape.com/",
	}
}

func (s *LinkedInSpider) Parse(resp *scrapegoat.Response) (*scrapegoat.SpiderResult, error) {
	result := &scrapegoat.SpiderResult{}

	// Simulate extracting profile-like data from a page
	// In a real LinkedIn scraper, you'd parse profile sections:
	// - Name, Headline, Location
	// - Experience entries
	// - Education entries
	// - Skills

	// Extract author quotes as "profile" entries (demo)
	resp.Doc.Find(".quote").Each(func(i int, s *goquery.Selection) {
		item := scrapegoat.NewItem(resp.URL)

		// Map quote data to profile-like structure
		name := strings.TrimSpace(s.Find(".author").Text())
		item.Set("name", name)
		item.Set("headline", strings.TrimSpace(s.Find(".text").Text()))

		// Extract tags as "skills"
		var skills []string
		s.Find(".tags .tag").Each(func(j int, tag *goquery.Selection) {
			skills = append(skills, strings.TrimSpace(tag.Text()))
		})
		item.Set("skills", strings.Join(skills, ", "))

		// Profile URL
		if href, ok := s.Find("a").Attr("href"); ok {
			item.Set("profile_url", href)
		}

		result.Items = append(result.Items, item)
	})

	// Follow pagination
	resp.Doc.Find("li.next a[href]").Each(func(i int, s *goquery.Selection) {
		if href, ok := s.Attr("href"); ok {
			result.Follow = append(result.Follow, href)
		}
	})

	return result, nil
}

func main() {
	fmt.Println("💼 ScrapeGoat LinkedIn-Style Profile Scraper Example")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()
	fmt.Println("⚠️  Note: This is a DEMO using quotes.toscrape.com as a stand-in.")
	fmt.Println("    Real LinkedIn scraping requires authentication and ToS compliance.")
	fmt.Println()

	err := scrapegoat.RunSpider(&LinkedInSpider{},
		scrapegoat.WithConcurrency(2),
		scrapegoat.WithMaxDepth(2),
		scrapegoat.WithOutput("json", "./output"),
		scrapegoat.WithDelay(2*time.Second),
		scrapegoat.WithMaxRequests(20),
	)
	if err != nil {
		log.Fatalf("Spider failed: %v", err)
	}

	fmt.Println("\n✅ Profile scraping complete! Check ./output/ for results.")
}
