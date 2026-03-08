package scrapegoat

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"

	"github.com/PuerkitoBio/goquery"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/storage"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// Spider is the Scrapy-style interface for defining crawlers declaratively.
// Users implement this interface to define seed URLs and parsing logic.
//
// Example:
//
//	type QuotesSpider struct{}
//
//	func (s *QuotesSpider) Name() string { return "quotes" }
//
//	func (s *QuotesSpider) StartURLs() []string {
//	    return []string{"https://quotes.toscrape.com"}
//	}
//
//	func (s *QuotesSpider) Parse(resp *scrapegoat.Response) (*scrapegoat.SpiderResult, error) {
//	    result := &scrapegoat.SpiderResult{}
//	    resp.Doc.Find(".quote").Each(func(i int, s *goquery.Selection) {
//	        item := scrapegoat.NewItem(resp.URL)
//	        item.Set("quote", s.Find(".text").Text())
//	        item.Set("author", s.Find(".author").Text())
//	        result.Items = append(result.Items, item)
//	    })
//	    resp.Doc.Find("li.next a[href]").Each(func(i int, s *goquery.Selection) {
//	        if href, ok := s.Attr("href"); ok {
//	            result.Follow = append(result.Follow, href)
//	        }
//	    })
//	    return result, nil
//	}
type Spider interface {
	// Name returns the spider's identifier. Used in logs and output metadata.
	Name() string

	// StartURLs returns the initial seed URLs to crawl.
	StartURLs() []string

	// Parse is called for every response. Returns extracted items and follow-up URLs.
	Parse(resp *Response) (*SpiderResult, error)
}

// Response wraps a fetched page with convenience methods for parsing.
type Response struct {
	// URL is the page's URL.
	URL string

	// StatusCode is the HTTP status code.
	StatusCode int

	// Doc is the parsed goquery document for CSS selector queries.
	Doc *goquery.Document

	// Body is the raw response body.
	Body []byte

	// internal response reference
	raw *types.Response
}

// SpiderResult holds the output of a Spider.Parse call.
type SpiderResult struct {
	// Items are the scraped data items.
	Items []*types.Item

	// Follow contains URLs to crawl next (relative or absolute).
	Follow []string
}

// NewItem creates a new empty Item with the given source URL.
// Convenience wrapper for use in Spider.Parse implementations.
func NewItem(url string) *types.Item {
	return types.NewItem(url)
}

// RunSpider runs a Spider with the given options.
// This is the Scrapy-style entry point: define a Spider, call RunSpider.
//
// Example:
//
//	err := scrapegoat.RunSpider(&QuotesSpider{},
//	    scrapegoat.WithConcurrency(5),
//	    scrapegoat.WithMaxDepth(3),
//	    scrapegoat.WithOutput("json", "./output"),
//	)
func RunSpider(spider Spider, opts ...Option) error {
	cfg := config.DefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	level := slog.LevelInfo
	if cfg.Logging.Level == "debug" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	logger = logger.With("spider", spider.Name())

	// Build engine
	eng := engine.New(cfg, logger)

	// Setup fetcher
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, logger)
	if err != nil {
		return fmt.Errorf("create fetcher: %w", err)
	}
	eng.SetFetcher("http", httpFetcher)

	// Setup parser for link discovery
	compositeParser := parser.NewCompositeParser(logger)
	eng.SetParser(compositeParser)

	// Setup pipeline
	pipe := pipeline.New(logger)
	pipe.Use(&pipeline.TrimMiddleware{})
	eng.SetPipeline(pipe)

	// Setup storage
	store, err := storage.NewFileStorage(cfg.Storage.Type, cfg.Storage.OutputPath, logger)
	if err != nil {
		return fmt.Errorf("create storage: %w", err)
	}
	eng.SetStorage(store)

	// Register the spider's Parse method as a response callback
	eng.OnResponse(spider.Name(), func(rawResp *types.Response) ([]*types.Item, []*types.Request, error) {
		doc, err := rawResp.Document()
		if err != nil {
			return nil, nil, err
		}

		resp := &Response{
			URL:        rawResp.Request.URLString(),
			StatusCode: rawResp.StatusCode,
			Doc:        doc,
			Body:       rawResp.Body,
			raw:        rawResp,
		}

		result, err := spider.Parse(resp)
		if err != nil {
			return nil, nil, err
		}
		if result == nil {
			return nil, nil, nil
		}

		// Tag items with spider name
		for _, item := range result.Items {
			item.SpiderName = spider.Name()
		}

		// Convert follow URLs to requests
		var newReqs []*types.Request
		for _, followURL := range result.Follow {
			req, err := types.NewRequest(followURL)
			if err != nil {
				// Try resolving as relative URL against current page
				if baseURL := rawResp.FinalURL; baseURL != "" {
					if base, bErr := url.Parse(baseURL); bErr == nil {
						if ref, rErr := url.Parse(followURL); rErr == nil {
							resolved := base.ResolveReference(ref).String()
							req, err = types.NewRequest(resolved)
						}
					}
				}
				if err != nil {
					continue
				}
			}
			newReqs = append(newReqs, req)
		}

		return result.Items, newReqs, nil
	})

	// Add seed URLs
	seeds := spider.StartURLs()
	var seedsAdded int
	for _, u := range seeds {
		if err := eng.AddSeed(u); err != nil {
			logger.Warn("seed skipped", "url", u, "reason", err)
		} else {
			seedsAdded++
		}
	}
	if seedsAdded == 0 && len(seeds) > 0 {
		return fmt.Errorf("all %d seed(s) were filtered or blocked", len(seeds))
	}

	// Start and wait
	if err := eng.Start(); err != nil {
		return fmt.Errorf("start engine: %w", err)
	}
	eng.Wait()

	stats := eng.Stats().Snapshot()
	logger.Info("spider complete",
		"requests", stats["requests_sent"],
		"items", stats["items_scraped"],
	)

	return nil
}
