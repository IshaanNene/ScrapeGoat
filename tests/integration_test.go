package integration

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/seo"
	"github.com/IshaanNene/ScrapeGoat/internal/storage"
	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

// TestLiveFetch tests fetching a real URL.
func TestLiveFetch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live test")
	}

	cfg := testutil.LoopbackConfig()
	f, err := fetcher.NewHTTPFetcher(cfg, testLogger)
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer f.Close()

	req, _ := types.NewRequest("https://quotes.toscrape.com")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	resp, err := f.Fetch(ctx, req)
	if err != nil {
		t.Fatalf("fetch error: %v", err)
	}

	t.Logf("Status: %d", resp.StatusCode)
	t.Logf("Content-Type: %s", resp.ContentType)
	t.Logf("Body size: %d bytes", len(resp.Body))
	t.Logf("Duration: %s", resp.FetchDuration)

	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if len(resp.Body) < 100 {
		t.Error("body too short")
	}
}

// TestLiveParse tests parsing a real page.
func TestLiveParse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live test")
	}

	cfg := testutil.LoopbackConfig()
	f, _ := fetcher.NewHTTPFetcher(cfg, testLogger)
	defer f.Close()

	req, _ := types.NewRequest("https://quotes.toscrape.com")
	resp, err := f.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	// CSS parsing
	p := parser.NewCSSParser(testLogger)
	rules := []config.ParseRule{
		{Name: "quotes", Type: "css", Selector: ".quote .text"},
		{Name: "authors", Type: "css", Selector: ".quote .author"},
	}
	items, links, err := p.Parse(resp, rules)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	t.Logf("CSS: %d items, %d links", len(items), len(links))
	if len(items) > 0 {
		for _, item := range items {
			for k, v := range item.Fields {
				t.Logf("  %s = %v", k, v)
			}
		}
	}
	if len(links) < 5 {
		t.Errorf("expected at least 5 links, got %d", len(links))
	}

	// Structured data
	sde := parser.NewStructuredDataExtractor(testLogger)
	sdResults, _ := sde.Extract(resp)
	t.Logf("Structured data: %d results", len(sdResults))
	for _, sd := range sdResults {
		t.Logf("  Type: %s, Fields: %d", sd.Type, len(sd.Data))
	}
}

// TestLiveSEOAudit tests the SEO auditor against a real page.
func TestLiveSEOAudit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live test")
	}

	cfg := testutil.LoopbackConfig()
	f, _ := fetcher.NewHTTPFetcher(cfg, testLogger)
	defer f.Close()

	req, _ := types.NewRequest("https://quotes.toscrape.com")
	resp, _ := f.Fetch(context.Background(), req)

	auditor := seo.NewMetaAuditor(testLogger)
	result, err := auditor.Audit(resp)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	t.Logf("SEO Score: %d/100", result.Score)
	for _, issue := range result.Issues {
		t.Logf("  [%s] %s: %s", issue.Severity, issue.Category, issue.Message)
	}
	t.Logf("Tags: %v", result.Tags)
}

// TestLiveSitemap tests sitemap crawling.
func TestLiveSitemap(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live test")
	}

	sc := seo.NewSitemapCrawler(testLogger)
	discovered := sc.DiscoverSitemap("quotes.toscrape.com")
	t.Logf("Discovered sitemap: %s", discovered)

	if discovered != "" {
		urls, err := sc.Crawl(discovered)
		if err != nil {
			t.Logf("Sitemap crawl error (expected for this site): %v", err)
		}
		t.Logf("Sitemap URLs: %d", len(urls))
	}
}

// TestLiveCrawl tests a full crawl cycle.
func TestLiveCrawl(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live test")
	}

	cfg := testutil.LoopbackConfig()
	cfg.Engine.MaxDepth = 1
	cfg.Engine.Concurrency = 2
	cfg.Engine.PolitenessDelay = 500 * time.Millisecond
	cfg.Storage.Type = "jsonl"
	cfg.Storage.OutputPath = t.TempDir()

	eng := engine.New(cfg, testLogger)

	httpFetcher, _ := fetcher.NewHTTPFetcher(cfg, testLogger)
	eng.SetFetcher("http", httpFetcher)
	eng.SetParser(parser.NewCompositeParser(testLogger))

	pipe := pipeline.New(testLogger)
	pipe.Use(&pipeline.TrimMiddleware{})
	eng.SetPipeline(pipe)

	store, _ := storage.NewFileStorage("jsonl", cfg.Storage.OutputPath, testLogger)
	eng.SetStorage(store)

	if err := eng.AddSeed("https://quotes.toscrape.com"); err != nil {
		t.Fatal(err)
	}

	// Start with timeout
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		eng.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Log("Crawl completed naturally")
	case <-time.After(60 * time.Second):
		eng.Stop()
		<-done
		t.Log("Crawl timed out, stopped gracefully")
	}

	snap := eng.Stats().Snapshot()
	t.Logf("Results:")
	t.Logf("  Requests:  %v sent, %v failed", snap["requests_sent"], snap["requests_failed"])
	t.Logf("  Items:     %v scraped, %v dropped", snap["items_scraped"], snap["items_dropped"])
	t.Logf("  URLs:      %v enqueued, %v filtered", snap["urls_enqueued"], snap["urls_filtered"])
	t.Logf("  Data:      %v bytes", snap["bytes_downloaded"])
	t.Logf("  Elapsed:   %v", snap["elapsed"])

	sent := snap["requests_sent"].(int64)
	if sent < 1 {
		t.Error("expected at least 1 request sent")
	}
}

// TestBacklinkExtraction tests backlink extraction against a live page.
func TestBacklinkExtraction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live test")
	}

	cfg := testutil.LoopbackConfig()
	f, _ := fetcher.NewHTTPFetcher(cfg, testLogger)
	defer f.Close()

	req, _ := types.NewRequest("https://quotes.toscrape.com")
	resp, _ := f.Fetch(context.Background(), req)

	backlinks, err := seo.ExtractBacklinks(resp)
	if err != nil {
		t.Fatalf("backlink extraction: %v", err)
	}

	t.Logf("Found %d backlinks", len(backlinks))

	internal := 0
	external := 0
	nofollow := 0
	for _, bl := range backlinks {
		if bl.External {
			external++
		} else {
			internal++
		}
		if bl.NoFollow {
			nofollow++
		}
	}
	t.Logf("  Internal: %d, External: %d, NoFollow: %d", internal, external, nofollow)
}
