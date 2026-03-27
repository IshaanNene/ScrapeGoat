// Package scrapegoat provides a public SDK for embedding ScrapeGoat as a library.
//
// Example usage:
//
//	crawler := scrapegoat.NewCrawler(
//	    scrapegoat.WithConcurrency(5),
//	    scrapegoat.WithMaxDepth(3),
//	    scrapegoat.WithOutput("json", "./output"),
//	)
//
//	crawler.OnHTML("h1", func(e *scrapegoat.Element) {
//	    e.Item.Set("title", e.Text())
//	})
//
//	crawler.OnHTML("a[href]", func(e *scrapegoat.Element) {
//	    e.Request.Follow(e.Attr("href"))
//	})
//
//	crawler.Start("https://example.com")
//	crawler.Wait()
package scrapegoat

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/storage"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// Crawler is the high-level API for using ScrapeGoat as a library.
type Crawler struct {
	cfg       *config.Config
	engine    *engine.Engine
	logger    *slog.Logger
	htmlRules map[string]HTMLCallback
}

// HTMLCallback is called for each element matching a CSS selector.
type HTMLCallback func(e *Element)

// Element represents a matched DOM element in a callback.
type Element struct {
	// Selection is the goquery selection.
	Selection *goquery.Selection

	// Item is the item being built for this page.
	Item *types.Item

	// Response is the page response.
	Response *types.Response

	// NewRequests collects follow-up URLs.
	NewRequests []*types.Request
}

// Text returns the text content of the element.
func (e *Element) Text() string {
	return e.Selection.Text()
}

// Attr returns the value of the given attribute.
func (e *Element) Attr(name string) string {
	val, _ := e.Selection.Attr(name)
	return val
}

// HTML returns the inner HTML of the element.
func (e *Element) HTML() string {
	html, _ := e.Selection.Html()
	return html
}

// Follow adds a URL to be crawled.
func (e *Element) Follow(rawURL string) {
	req, err := types.NewRequest(rawURL)
	if err != nil {
		return
	}
	e.NewRequests = append(e.NewRequests, req)
}

// Option configures a Crawler.
type Option func(*config.Config)

// WithConcurrency sets the number of concurrent workers.
func WithConcurrency(n int) Option {
	return func(c *config.Config) { c.Engine.Concurrency = n }
}

// WithMaxDepth sets the maximum crawl depth.
func WithMaxDepth(depth int) Option {
	return func(c *config.Config) { c.Engine.MaxDepth = depth }
}

// WithDelay sets the politeness delay between requests.
func WithDelay(d time.Duration) Option {
	return func(c *config.Config) { c.Engine.PolitenessDelay = d }
}

// WithOutput sets the output format and path.
func WithOutput(format, path string) Option {
	return func(c *config.Config) {
		c.Storage.Type = format
		c.Storage.OutputPath = path
	}
}

// WithUserAgent sets a custom User-Agent.
func WithUserAgent(ua string) Option {
	return func(c *config.Config) { c.Engine.UserAgents = []string{ua} }
}

// WithAllowedDomains restricts crawling to the given domains.
func WithAllowedDomains(domains ...string) Option {
	return func(c *config.Config) { c.Engine.AllowedDomains = domains }
}

// WithProxy enables proxy rotation with the given proxy URLs.
func WithProxy(urls ...string) Option {
	return func(c *config.Config) {
		c.Proxy.Enabled = true
		c.Proxy.URLs = urls
	}
}

// WithRobotsRespect enables/disables robots.txt compliance.
func WithRobotsRespect(respect bool) Option {
	return func(c *config.Config) { c.Engine.RespectRobotsTxt = respect }
}

// WithMaxRequests sets the global request limit.
func WithMaxRequests(n int) Option {
	return func(c *config.Config) { c.Engine.MaxRequests = n }
}

// WithVerbose enables debug-level logging.
func WithVerbose() Option {
	return func(c *config.Config) { c.Logging.Level = "debug" }
}

// NewCrawler creates a new Crawler with the given options.
func NewCrawler(opts ...Option) *Crawler {
	cfg := config.DefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	level := slog.LevelInfo
	if cfg.Logging.Level == "debug" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	return &Crawler{
		cfg:       cfg,
		logger:    logger,
		htmlRules: make(map[string]HTMLCallback),
	}
}

// OnHTML registers a callback for elements matching the CSS selector.
func (c *Crawler) OnHTML(selector string, cb HTMLCallback) {
	c.htmlRules[selector] = cb
}

// Start begins crawling from the given seed URLs.
func (c *Crawler) Start(urls ...string) error {
	// Build the engine
	eng := engine.New(c.cfg, c.logger)

	// Setup fetcher
	httpFetcher, err := fetcher.NewHTTPFetcher(c.cfg, c.logger)
	if err != nil {
		return fmt.Errorf("create fetcher: %w", err)
	}
	eng.SetFetcher("http", httpFetcher)

	// Setup parser (used for link discovery)
	compositeParser := parser.NewCompositeParser(c.logger)
	eng.SetParser(compositeParser)

	// Setup pipeline
	pipe := pipeline.New(c.logger)
	eng.SetPipeline(pipe)

	// Setup storage
	store, err := storage.NewFileStorage(c.cfg.Storage.Type, c.cfg.Storage.OutputPath, c.logger)
	if err != nil {
		return fmt.Errorf("create storage: %w", err)
	}
	eng.SetStorage(store)

	// Register HTML callbacks as engine response callbacks
	if len(c.htmlRules) > 0 {
		eng.OnResponse("html_callbacks", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
			doc, err := resp.Document()
			if err != nil {
				return nil, nil, err
			}

			var items []*types.Item
			var newReqs []*types.Request

			for selector, cb := range c.htmlRules {
				doc.Find(selector).Each(func(i int, sel *goquery.Selection) {
					// Each match gets its own item
					item := types.NewItem(resp.Request.URLString())
					elem := &Element{
						Selection: sel,
						Item:      item,
						Response:  resp,
					}
					cb(elem)
					newReqs = append(newReqs, elem.NewRequests...)

					// Only emit if the callback actually set data fields
					if len(item.Fields) > 0 {
						items = append(items, item)
					}
				})
			}

			return items, newReqs, nil
		})
	}

	// Add seed URLs — blocked/filtered seeds are warnings, not fatal errors
	var seedsAdded int
	for _, u := range urls {
		if err := eng.AddSeed(u); err != nil {
			// ErrBlocked, ErrDuplicate, ErrMaxDepth are non-fatal — just skip the seed
			c.logger.Warn("seed skipped", "url", u, "reason", err)
		} else {
			seedsAdded++
		}
	}
	if seedsAdded == 0 && len(urls) > 0 {
		return fmt.Errorf("all %d seed(s) were filtered or blocked", len(urls))
	}

	c.engine = eng
	return eng.Start()
}

// Wait blocks until the crawl is complete.
func (c *Crawler) Wait() {
	if c.engine != nil {
		c.engine.Wait()
	}
}

// Stop gracefully stops the crawler.
func (c *Crawler) Stop() {
	if c.engine != nil {
		c.engine.Stop()
	}
}

// Pause pauses the crawler.
func (c *Crawler) Pause() {
	if c.engine != nil {
		c.engine.Pause()
	}
}

// Resume resumes the crawler.
func (c *Crawler) Resume() {
	if c.engine != nil {
		c.engine.Resume()
	}
}

// Stats returns crawl statistics.
func (c *Crawler) Stats() map[string]any {
	if c.engine != nil {
		return c.engine.Stats().Snapshot()
	}
	return nil
}
