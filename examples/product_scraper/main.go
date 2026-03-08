// Product Scraper Example
//
// Demonstrates scraping product listings with prices, images, and descriptions
// using the ScrapeGoat Spider interface.
//
// Usage:
//
//	go run main.go
package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/PuerkitoBio/goquery"

	scrapegoat "github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat"
)

// ProductSpider scrapes product information from books.toscrape.com.
type ProductSpider struct{}

func (s *ProductSpider) Name() string { return "product_scraper" }

func (s *ProductSpider) StartURLs() []string {
	return []string{
		"https://books.toscrape.com/catalogue/page-1.html",
	}
}

func (s *ProductSpider) Parse(resp *scrapegoat.Response) (*scrapegoat.SpiderResult, error) {
	result := &scrapegoat.SpiderResult{}

	// Extract product cards
	resp.Doc.Find("article.product_pod").Each(func(i int, s *goquery.Selection) {
		item := scrapegoat.NewItem(resp.URL)

		// Product name
		item.Set("name", strings.TrimSpace(s.Find("h3 a").AttrOr("title", "")))

		// Price
		item.Set("price", strings.TrimSpace(s.Find(".price_color").Text()))

		// Availability
		item.Set("availability", strings.TrimSpace(s.Find(".availability").Text()))

		// Image URL
		if imgSrc, ok := s.Find("img").Attr("src"); ok {
			if !strings.HasPrefix(imgSrc, "http") {
				imgSrc = "https://books.toscrape.com/catalogue/" + imgSrc
			}
			item.Set("image_url", imgSrc)
		}

		// Product detail URL
		if href, ok := s.Find("h3 a").Attr("href"); ok {
			if !strings.HasPrefix(href, "http") {
				href = "https://books.toscrape.com/catalogue/" + href
			}
			item.Set("detail_url", href)
		}

		// Rating
		ratingMap := map[string]int{
			"one": 1, "two": 2, "three": 3, "four": 4, "five": 5,
		}
		s.Find(".star-rating").Each(func(j int, star *goquery.Selection) {
			for className, rating := range ratingMap {
				if star.HasClass(strings.Title(className)) {
					item.Set("rating", rating)
				}
			}
		})

		result.Items = append(result.Items, item)
	})

	// Follow pagination
	resp.Doc.Find("li.next a[href]").Each(func(i int, s *goquery.Selection) {
		if href, ok := s.Attr("href"); ok {
			if !strings.HasPrefix(href, "http") {
				href = "https://books.toscrape.com/catalogue/" + href
			}
			result.Follow = append(result.Follow, href)
		}
	})

	return result, nil
}

func main() {
	fmt.Println("🛒 ScrapeGoat Product Scraper Example")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("Scraping product listings from books.toscrape.com...")
	fmt.Println()

	err := scrapegoat.RunSpider(&ProductSpider{},
		scrapegoat.WithConcurrency(3),
		scrapegoat.WithMaxDepth(2),
		scrapegoat.WithOutput("jsonl", "./output"),
		scrapegoat.WithMaxRequests(50),
	)
	if err != nil {
		log.Fatalf("Spider failed: %v", err)
	}

	fmt.Println("\n✅ Product scraping complete! Check ./output/ for results.")
}
