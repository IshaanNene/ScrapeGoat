package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/spf13/cobra"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/storage"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

var (
	searchMaxPages       int
	searchDepth          int
	searchConcurrent     int
	searchOutput         string
	searchDelay          string
	searchAllowedDomains string
)

// searchCmd creates the "search" subcommand.
func searchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search [url]",
		Short: "Search engine crawler — index pages with full text, headings, meta, and link graph",
		Long: `Crawl a website and build a search-engine-style index.

Extracts for each page: title, meta description, keywords, canonical URL,
h1/h2/h3 headings, cleaned body text, word count, outbound link count,
image count, and content hash. Output is JSONL (one document per line).`,
		Args: cobra.MinimumNArgs(1),
		RunE: runSearch,
	}

	cmd.Flags().IntVar(&searchMaxPages, "max-pages", 500, "maximum pages to crawl")
	cmd.Flags().IntVarP(&searchDepth, "depth", "d", 3, "maximum crawl depth")
	cmd.Flags().IntVarP(&searchConcurrent, "concurrency", "n", 10, "number of concurrent workers")
	cmd.Flags().StringVarP(&searchOutput, "output", "o", "./output/search_index", "output directory")
	cmd.Flags().StringVar(&searchDelay, "delay", "200ms", "delay between requests")
	cmd.Flags().StringVar(&searchAllowedDomains, "allowed-domains", "", "comma-separated domains to stay within")

	return cmd
}

func runSearch(cmd *cobra.Command, args []string) error {
	logger := setupLogger()
	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = searchConcurrent
	cfg.Engine.MaxDepth = searchDepth
	cfg.Engine.MaxRequests = searchMaxPages
	cfg.Engine.RespectRobotsTxt = true
	cfg.Storage.Type = "jsonl"
	cfg.Storage.OutputPath = searchOutput

	if d, err := time.ParseDuration(searchDelay); err == nil {
		cfg.Engine.PolitenessDelay = d
	}
	if searchAllowedDomains != "" {
		var domains []string
		for _, d := range strings.Split(searchAllowedDomains, ",") {
			if d = strings.TrimSpace(d); d != "" {
				domains = append(domains, d)
			}
		}
		cfg.Engine.AllowedDomains = domains
	}

	eng := engine.New(cfg, logger)

	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, logger)
	if err != nil {
		return fmt.Errorf("create fetcher: %w", err)
	}
	eng.SetFetcher("http", httpFetcher)
	eng.SetParser(parser.NewCompositeParser(logger))

	pipe := pipeline.New(logger)
	pipe.Use(&pipeline.TrimMiddleware{})
	eng.SetPipeline(pipe)

	store, err := storage.NewFileStorage("jsonl", searchOutput, logger)
	if err != nil {
		return fmt.Errorf("create storage: %w", err)
	}
	eng.SetStorage(store)

	// Register the search engine indexer callback
	eng.OnResponse("search_indexer", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		doc, err := resp.Document()
		if err != nil {
			return nil, nil, err
		}

		url := resp.Request.URLString()
		item := types.NewItem(url)

		title := strings.TrimSpace(doc.Find("title").First().Text())
		desc, _ := doc.Find("meta[name='description']").Attr("content")
		keywords, _ := doc.Find("meta[name='keywords']").Attr("content")
		canonical, _ := doc.Find("link[rel='canonical']").Attr("href")
		lang, _ := doc.Find("html").Attr("lang")

		var h1s, h2s, h3s []string
		doc.Find("h1").Each(func(_ int, s *goquery.Selection) {
			if t := strings.TrimSpace(s.Text()); t != "" {
				h1s = append(h1s, t)
			}
		})
		doc.Find("h2").Each(func(_ int, s *goquery.Selection) {
			if t := strings.TrimSpace(s.Text()); t != "" {
				h2s = append(h2s, t)
			}
		})
		doc.Find("h3").Each(func(_ int, s *goquery.Selection) {
			if t := strings.TrimSpace(s.Text()); t != "" {
				h3s = append(h3s, t)
			}
		})

		bodyClone := doc.Find("body").Clone()
		bodyClone.Find("script, style, nav, footer, header, aside, .sidebar, .menu, .nav, .cookie").Remove()
		bodyText := strings.TrimSpace(bodyClone.Text())
		words := strings.Fields(bodyText)
		bodyText = strings.Join(words, " ")
		if len(bodyText) > 5000 {
			bodyText = bodyText[:5000] + "..."
		}

		linkCount := doc.Find("a[href]").Length()
		imgCount := doc.Find("img").Length()
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(bodyText)))[:16]

		if title != "" || len(words) > 10 {
			item.Set("url", url)
			item.Set("title", title)
			item.Set("description", desc)
			item.Set("keywords", keywords)
			item.Set("canonical", canonical)
			item.Set("language", lang)
			item.Set("h1", h1s)
			item.Set("h2", h2s)
			item.Set("h3", h3s)
			item.Set("body_text", bodyText)
			item.Set("word_count", len(words))
			item.Set("outbound_links", linkCount)
			item.Set("images", imgCount)
			item.Set("content_hash", hash)
			item.Set("indexed_at", time.Now().Format(time.RFC3339))
			return []*types.Item{item}, nil, nil
		}
		return nil, nil, nil
	})

	// Add seeds — robots-block is a warning, not fatal
	var seedsAdded int
	for _, rawURL := range args {
		if err := eng.AddSeed(rawURL); err != nil {
			logger.Warn("seed skipped", "url", rawURL, "reason", err)
		} else {
			seedsAdded++
		}
	}
	if seedsAdded == 0 {
		return fmt.Errorf("all seeds were filtered or blocked — check URLs and robots.txt")
	}

	fmt.Printf("  Search Engine Crawler\n")
	fmt.Printf("   Seeds: %v\n", args)
	fmt.Printf("   Config: %d workers, depth %d, max %d pages\n\n", searchConcurrent, searchDepth, searchMaxPages)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("shutting down...", "signal", sig)
		eng.Stop()
	}()

	start := time.Now()
	if err := eng.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	eng.Wait()

	stats := eng.Stats().Snapshot()
	fmt.Printf("\n  Search index built in %s\n", time.Since(start).Round(time.Millisecond))
	fmt.Printf("   Pages indexed: %v\n", stats["items_scraped"])
	fmt.Printf("   Pages crawled: %v\n", stats["requests_sent"])
	fmt.Printf("   Data: %v bytes\n", stats["bytes_downloaded"])
	fmt.Printf("   Output: %s/results.jsonl\n", searchOutput)
	return nil
}
