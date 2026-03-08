// Example: Using the Spider interface for structured, Scrapy-style crawling.
//
// This demonstrates ScrapeGoat's declarative Spider interface where you
// define StartURLs() and Parse() to create a clean, structured crawler.
//
// Run: go run ./examples/spider/
package main

import (
	"fmt"
	"log"

	scrapegoat "github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat"
	"github.com/PuerkitoBio/goquery"
)

// QuotesSpider extracts quotes from quotes.toscrape.com
type QuotesSpider struct{}

func (s *QuotesSpider) Name() string { return "quotes" }

func (s *QuotesSpider) StartURLs() []string {
	return []string{"https://quotes.toscrape.com"}
}

func (s *QuotesSpider) Parse(resp *scrapegoat.Response) (*scrapegoat.SpiderResult, error) {
	result := &scrapegoat.SpiderResult{}

	// Extract quotes
	resp.Doc.Find(".quote").Each(func(i int, sel *goquery.Selection) {
		item := scrapegoat.NewItem(resp.URL)
		item.Set("quote", sel.Find(".text").Text())
		item.Set("author", sel.Find(".author").Text())

		// Extract tags
		var tags []string
		sel.Find(".tag").Each(func(j int, tag *goquery.Selection) {
			tags = append(tags, tag.Text())
		})
		item.Set("tags", tags)

		result.Items = append(result.Items, item)
	})

	// Follow pagination
	resp.Doc.Find("li.next a[href]").Each(func(i int, sel *goquery.Selection) {
		if href, ok := sel.Attr("href"); ok {
			result.Follow = append(result.Follow, "https://quotes.toscrape.com"+href)
		}
	})

	return result, nil
}

func main() {
	fmt.Println("ScrapeGoat Spider Example — Quotes")
	fmt.Println("===================================")

	err := scrapegoat.RunSpider(&QuotesSpider{},
		scrapegoat.WithConcurrency(3),
		scrapegoat.WithMaxDepth(5),
		scrapegoat.WithOutput("json", "./output/spider-quotes"),
	)
	if err != nil {
		log.Fatal(err)
	}
}
