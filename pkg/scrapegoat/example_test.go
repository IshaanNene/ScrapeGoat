package scrapegoat_test

import (
	"fmt"

	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat"
)

// The callback API: register CSS selectors, build items as elements match.
func ExampleCrawler_OnHTML() {
	crawler := scrapegoat.NewCrawler(
		scrapegoat.WithConcurrency(5),
		scrapegoat.WithMaxDepth(2),
		scrapegoat.WithOutput("json", "./output"),
	)

	crawler.OnHTML("h1", func(e *scrapegoat.Element) {
		e.Item.Set("title", e.Text())
	})

	crawler.OnHTML("a[href]", func(e *scrapegoat.Element) {
		e.Follow(e.Attr("href"))
	})

	// crawler.Start("https://example.com")
	// crawler.Wait()

	fmt.Println(crawler != nil)
	// Output: true
}

// Item is a nameable type, so a consumer can write ordinary Go functions over the
// values the API hands back. Under the old internal/ placement you could call
// methods on an *Item but could not declare one, which blocked this entirely.
func ExampleItem() {
	item := scrapegoat.NewItem("https://example.com/product/1")
	item.Set("price", 42)

	withCurrency := func(i *scrapegoat.Item, code string) *scrapegoat.Item {
		i.Set("currency", code)
		return i
	}

	enriched := withCurrency(item, "GBP")
	fmt.Println(enriched.GetString("currency"))
	// Output: GBP
}

func ExampleNewRequest() {
	req, err := scrapegoat.NewRequest("https://example.com/page")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(req.URLString())
	// Output: https://example.com/page
}

// Option values are opaque, so they compose without exposing the internal
// configuration struct.
func ExampleOption() {
	opts := []scrapegoat.Option{
		scrapegoat.WithConcurrency(10),
		scrapegoat.WithMaxDepth(3),
		scrapegoat.WithRobotsRespect(true),
	}

	crawler := scrapegoat.NewCrawler(opts...)
	fmt.Println(crawler != nil)
	// Output: true
}
