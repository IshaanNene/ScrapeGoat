package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/observability"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/storage"
)

var (
	cfgFile        string
	verbose        bool
	outputPath     string
	outputType     string
	depth          int
	concurrent     int
	delay          string
	userAgent      string
	maxRequests    int
	maxRetries     int
	allowedDomains string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "scrapegoat",
		Short: "ScrapeGoat — All-in-One Web Scraper/Crawler",
		Long: `ScrapeGoat is a next-generation, enterprise-grade web scraping and crawling toolkit.

Features:
  • High-performance concurrent crawling with per-domain throttling
  • CSS selector and regex extraction
  • Search engine indexing mode (full-text, headings, meta, link graph)
  • AI-powered crawling (summarize, NER, sentiment via LLM)
  • JSON, JSONL, CSV export
  • Proxy rotation and User-Agent randomization
  • robots.txt compliance
  • Checkpoint-based pause/resume
  • Prometheus metrics endpoint`,
	}

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file path")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug logging")

	rootCmd.AddCommand(crawlCmd())
	rootCmd.AddCommand(searchCmd())
	rootCmd.AddCommand(aiCrawlCmd())
	rootCmd.AddCommand(newCmd())
	rootCmd.AddCommand(versionCmd())
	rootCmd.AddCommand(configCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// crawlCmd creates the "crawl" subcommand.
func crawlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crawl [url]",
		Short: "Start crawling a URL",
		Long:  "Start crawling from the given seed URL(s), following links and extracting data.",
		Args:  cobra.MinimumNArgs(1),
		RunE:  runCrawl,
	}

	cmd.Flags().StringVarP(&outputPath, "output", "o", "./output", "output directory or file path")
	cmd.Flags().StringVarP(&outputType, "format", "f", "json", "output format: json, jsonl, csv")
	cmd.Flags().IntVarP(&depth, "depth", "d", 3, "maximum crawl depth")
	cmd.Flags().IntVarP(&concurrent, "concurrency", "n", 10, "number of concurrent workers")
	cmd.Flags().StringVar(&delay, "delay", "1s", "politeness delay between requests per domain")
	cmd.Flags().StringVar(&userAgent, "user-agent", "", "custom User-Agent string")
	cmd.Flags().IntVarP(&maxRequests, "max-requests", "m", 0, "maximum total requests (0 = unlimited)")
	cmd.Flags().IntVar(&maxRetries, "max-retries", -1, "max retries per failed request (-1 = use config default of 3)")
	cmd.Flags().StringVar(&allowedDomains, "allowed-domains", "", "comma-separated domains to stay within (e.g. en.wikipedia.org)")

	return cmd
}

// runCrawl executes the crawl command.
func runCrawl(cmd *cobra.Command, args []string) error {
	// Setup logger
	logger := setupLogger()

	// Load config
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Apply CLI overrides
	applyCLIOverrides(cfg)

	// Validate config
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	// Validate URLs
	for _, rawURL := range args {
		if err := config.ValidateURL(rawURL); err != nil {
			return fmt.Errorf("invalid URL %q: %w", rawURL, err)
		}
	}

	logger.Info("starting crawl",
		"seeds", args,
		"depth", cfg.Engine.MaxDepth,
		"concurrency", cfg.Engine.Concurrency,
		"output", cfg.Storage.OutputPath,
		"format", cfg.Storage.Type,
	)

	// Create engine
	eng := engine.New(cfg, logger)

	// Setup HTTP fetcher
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, logger)
	if err != nil {
		return fmt.Errorf("create fetcher: %w", err)
	}
	eng.SetFetcher("http", httpFetcher)

	// Setup parser
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

	// Setup metrics (if enabled)
	if cfg.Metrics.Enabled {
		metrics := observability.NewMetrics(logger)
		if err := metrics.StartServer(cfg.Metrics.Port, cfg.Metrics.Path); err != nil {
			logger.Warn("failed to start metrics server", "error", err)
		}
	}

	// Add seed URLs — robots-block on a seed is a warning, not fatal
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

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("received signal, shutting down...", "signal", sig)
		eng.Stop()
	}()

	// Start crawling
	start := time.Now()
	if err := eng.Start(); err != nil {
		return fmt.Errorf("start engine: %w", err)
	}

	// Wait for completion
	eng.Wait()

	elapsed := time.Since(start)
	stats := eng.Stats().Snapshot()

	logger.Info("crawl complete",
		"elapsed", elapsed,
		"requests", stats["requests_sent"],
		"items", stats["items_scraped"],
		"errors", stats["responses_error"],
		"bytes", stats["bytes_downloaded"],
	)

	fmt.Printf("\n  Crawl complete in %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("   Requests:  %v sent, %v failed\n", stats["requests_sent"], stats["requests_failed"])
	fmt.Printf("   Items:     %v scraped, %v dropped\n", stats["items_scraped"], stats["items_dropped"])
	fmt.Printf("   Data:      %v bytes downloaded\n", stats["bytes_downloaded"])
	fmt.Printf("   Output:    %s\n", cfg.Storage.OutputPath)

	if stats["items_scraped"] == int64(0) {
		fmt.Println("\n  No items were scraped. The crawl command discovers and follows links by default.")
		fmt.Println("   For automatic content extraction, try:")
		fmt.Println("     scrapegoat search <url>      — extract title, headings, body text, meta")
		fmt.Println("     scrapegoat ai-crawl <url>    — AI-powered summarize, NER, sentiment")
		fmt.Println("     scrapegoat crawl <url> -c config.yaml  — use custom parse rules")
	}

	return nil
}

// versionCmd creates the "version" subcommand.
func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("ScrapeGoat %s\n", config.Version)
		},
	}
}

// configCmd creates the "config" subcommand for inspecting configuration.
func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(cfgFile)
			if err != nil {
				return err
			}
			fmt.Printf("Engine:\n")
			fmt.Printf("  Concurrency:      %d\n", cfg.Engine.Concurrency)
			fmt.Printf("  Max Depth:         %d\n", cfg.Engine.MaxDepth)
			fmt.Printf("  Request Timeout:   %s\n", cfg.Engine.RequestTimeout)
			fmt.Printf("  Politeness Delay:  %s\n", cfg.Engine.PolitenessDelay)
			fmt.Printf("  Respect robots.txt: %v\n", cfg.Engine.RespectRobotsTxt)
			fmt.Printf("  Max Retries:       %d\n", cfg.Engine.MaxRetries)
			fmt.Printf("  User Agents:       %d configured\n", len(cfg.Engine.UserAgents))
			fmt.Printf("\nFetcher:\n")
			fmt.Printf("  Type:              %s\n", cfg.Fetcher.Type)
			fmt.Printf("  Follow Redirects:  %v\n", cfg.Fetcher.FollowRedirects)
			fmt.Printf("  Max Body Size:     %d bytes\n", cfg.Fetcher.MaxBodySize)
			fmt.Printf("\nProxy:\n")
			fmt.Printf("  Enabled:           %v\n", cfg.Proxy.Enabled)
			fmt.Printf("  Rotation:          %s\n", cfg.Proxy.Rotation)
			fmt.Printf("  Count:             %d\n", len(cfg.Proxy.URLs))
			fmt.Printf("\nStorage:\n")
			fmt.Printf("  Type:              %s\n", cfg.Storage.Type)
			fmt.Printf("  Output Path:       %s\n", cfg.Storage.OutputPath)
			fmt.Printf("\nMetrics:\n")
			fmt.Printf("  Enabled:           %v\n", cfg.Metrics.Enabled)
			fmt.Printf("  Port:              %d\n", cfg.Metrics.Port)
			return nil
		},
	}
	return cmd
}

// setupLogger creates a structured logger.
func setupLogger() *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	handler := slog.NewTextHandler(os.Stderr, opts)
	return slog.New(handler)
}

// applyCLIOverrides applies command-line flag values to the config.
func applyCLIOverrides(cfg *config.Config) {
	// Always apply depth and concurrency since they have sensible defaults
	cfg.Engine.MaxDepth = depth
	if concurrent > 0 {
		cfg.Engine.Concurrency = concurrent
	}
	if delay != "" {
		d, err := time.ParseDuration(delay)
		if err == nil {
			cfg.Engine.PolitenessDelay = d
		}
	}
	if userAgent != "" {
		cfg.Engine.UserAgents = []string{userAgent}
	}
	if outputPath != "" {
		cfg.Storage.OutputPath = outputPath
	}
	if outputType != "" {
		cfg.Storage.Type = strings.ToLower(outputType)
	}
	if maxRequests > 0 {
		cfg.Engine.MaxRequests = maxRequests
	}
	if maxRetries >= 0 {
		cfg.Engine.MaxRetries = maxRetries
	}
	if allowedDomains != "" {
		var domains []string
		for _, d := range strings.Split(allowedDomains, ",") {
			if d = strings.TrimSpace(d); d != "" {
				domains = append(domains, d)
			}
		}
		cfg.Engine.AllowedDomains = domains
	}
}

// newCmd creates the "new" subcommand for scaffolding spiders.
func newCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new [type] [name]",
		Short: "Generate a new spider or project scaffold",
		Long:  "Generate boilerplate code for a new spider using the Spider interface.",
		Args:  cobra.ExactArgs(2),
		RunE:  runNew,
	}
	return cmd
}

// runNew generates scaffold files.
func runNew(cmd *cobra.Command, args []string) error {
	scaffoldType := args[0]
	name := args[1]

	switch scaffoldType {
	case "spider":
		return generateSpider(name)
	default:
		return fmt.Errorf("unknown scaffold type %q (available: spider)", scaffoldType)
	}
}

// generateSpider creates a new spider directory with boilerplate main.go.
func generateSpider(name string) error {
	dir := name
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	mainFile := filepath.Join(dir, "main.go")
	if _, err := os.Stat(mainFile); err == nil {
		return fmt.Errorf("%s already exists", mainFile)
	}

	template := fmt.Sprintf(`package main

import (
	"fmt"
	"log"

	"github.com/PuerkitoBio/goquery"
	scrapegoat "github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat"
)

// %sSpider scrapes data from a target website.
type %sSpider struct{}

func (s *%sSpider) Name() string { return %q }

func (s *%sSpider) StartURLs() []string {
	return []string{
		"https://example.com", // 👈 Replace with your target URL
	}
}

func (s *%sSpider) Parse(resp *scrapegoat.Response) (*scrapegoat.SpiderResult, error) {
	result := &scrapegoat.SpiderResult{}

	// Extract data using CSS selectors
	resp.Doc.Find("h1").Each(func(i int, sel *goquery.Selection) {
		item := scrapegoat.NewItem(resp.URL)
		item.Set("title", sel.Text())
		result.Items = append(result.Items, item)
	})

	// Follow links (optional)
	resp.Doc.Find("a[href]").Each(func(i int, sel *goquery.Selection) {
		if href, ok := sel.Attr("href"); ok {
			result.Follow = append(result.Follow, href)
		}
	})

	return result, nil
}

func main() {
	fmt.Println("Starting %s spider...")

	err := scrapegoat.RunSpider(&%sSpider{},
		scrapegoat.WithConcurrency(5),
		scrapegoat.WithMaxDepth(2),
		scrapegoat.WithOutput("json", "./output/%s"),
	)
	if err != nil {
		log.Fatal(err)
	}
}
`, strings.Title(name), strings.Title(name), strings.Title(name), name,
		strings.Title(name), strings.Title(name), name, strings.Title(name), name)

	if err := os.WriteFile(mainFile, []byte(template), 0o644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	fmt.Printf("✅ Created spider scaffold:\n")
	fmt.Printf("   %s/main.go\n\n", dir)
	fmt.Printf("Next steps:\n")
	fmt.Printf("  1. Edit %s — update StartURLs() and Parse()\n", mainFile)
	fmt.Printf("  2. Run:  go run ./%s/\n", dir)

	return nil
}
