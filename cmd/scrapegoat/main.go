package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/IshaanNene/ScrapeGoat/internal/benchmark"
	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/dashboard"
	"github.com/IshaanNene/ScrapeGoat/internal/distributed"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/observability"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/storage"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
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
	rootCmd.AddCommand(masterCmd())
	rootCmd.AddCommand(workerCmd())
	rootCmd.AddCommand(extractCmd())
	rootCmd.AddCommand(dashboardCmd())
	rootCmd.AddCommand(benchmarkCmd())
	rootCmd.AddCommand(scaleCmd())

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
	case "project":
		return generateProject(name)
	default:
		return fmt.Errorf("unknown scaffold type %q (available: spider, project)", scaffoldType)
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

// masterCmd creates the "master" subcommand for starting the distributed coordinator.
func masterCmd() *cobra.Command {
	var (
		addr string
	)

	cmd := &cobra.Command{
		Use:   "master",
		Short: "Start the distributed crawl coordinator",
		Long:  "Start the master node that coordinates distributed workers, assigns tasks, and monitors cluster health.",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()

			master := distributed.NewMaster(logger)
			api := distributed.NewMasterAPI(master, logger)

			if err := api.Start(addr); err != nil {
				return fmt.Errorf("start master API: %w", err)
			}

			fmt.Printf("🐐 ScrapeGoat Master running at %s\n", addr)
			fmt.Printf("\nEndpoints:\n")
			fmt.Printf("  POST   %s/api/register    — Register worker\n", addr)
			fmt.Printf("  POST   %s/api/submit      — Submit crawl task\n", addr)
			fmt.Printf("  GET    %s/api/status       — Cluster status\n", addr)
			fmt.Printf("  GET    %s/api/tasks/:id    — Get tasks for worker\n", addr)
			fmt.Printf("\nPress Ctrl+C to stop.\n")

			// Wait for shutdown signal
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			<-sigCh

			fmt.Println("\nMaster shutting down...")
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", ":8081", "master API listen address")
	return cmd
}

// workerCmd creates the "worker" subcommand for starting a distributed worker.
func workerCmd() *cobra.Command {
	var (
		masterAddr string
		capacity   int
		workerID   string
	)

	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Start a distributed crawl worker",
		Long:  "Start a worker node that connects to a master, pulls crawl tasks, and executes them.",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()

			wcfg := &distributed.WorkerConfig{
				ID:         workerID,
				MasterAddr: masterAddr,
				Capacity:   capacity,
			}

			worker := distributed.NewWorker(wcfg, logger)
			worker.SetCrawlFunc(func(task *distributed.Task) error {
				logger.Info("executing crawl task",
					"task_id", task.ID,
					"urls", len(task.URLs),
				)
				return nil
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if err := worker.Start(ctx); err != nil {
				return fmt.Errorf("start worker: %w", err)
			}

			fmt.Printf("🐐 ScrapeGoat Worker %s connected to %s\n", workerID, masterAddr)
			fmt.Printf("   Capacity: %d concurrent tasks\n", capacity)
			fmt.Printf("\nPress Ctrl+C to stop.\n")

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			<-sigCh

			worker.Stop()
			fmt.Println("\nWorker stopped.")
			return nil
		},
	}

	cmd.Flags().StringVar(&masterAddr, "master", "http://localhost:8081", "master node address")
	cmd.Flags().IntVar(&capacity, "capacity", 10, "max concurrent tasks")
	cmd.Flags().StringVar(&workerID, "id", fmt.Sprintf("worker-%d", time.Now().UnixMilli()), "worker ID")
	return cmd
}

// extractCmd creates the "extract" subcommand for auto-extracting data.
func extractCmd() *cobra.Command {
	var (
		extractOutput string
	)

	cmd := &cobra.Command{
		Use:   "extract [url]",
		Short: "Auto-extract structured data from any URL",
		Long: `Automatically extract structured data from a webpage without writing selectors.
Uses JSON-LD, OpenGraph, meta tags, and heuristic patterns to detect and extract
products, articles, tables, and other structured content.

Examples:
  scrapegoat extract https://books.toscrape.com
  scrapegoat extract https://news.site.com/article -o results.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()
			cfg := config.DefaultConfig()

			// Fetch the URL
			httpFetcher, err := fetcher.NewHTTPFetcher(cfg, logger)
			if err != nil {
				return fmt.Errorf("create fetcher: %w", err)
			}

			req, err := types.NewRequest(args[0])
			if err != nil {
				return fmt.Errorf("invalid URL: %w", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			fmt.Printf("🔍 Extracting structured data from %s...\n\n", args[0])

			resp, err := httpFetcher.Fetch(ctx, req)
			if err != nil {
				return fmt.Errorf("fetch URL: %w", err)
			}

			extractor := parser.NewAutoExtractor(logger)
			data, err := extractor.Extract(resp)
			if err != nil {
				return fmt.Errorf("extract data: %w", err)
			}

			// Output results
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(data); err != nil {
				return err
			}

			// Save to file if specified
			if extractOutput != "" {
				if err := extractor.ExtractToFile(resp, extractOutput); err != nil {
					return fmt.Errorf("write output: %w", err)
				}
				fmt.Printf("\n📁 Saved to %s\n", extractOutput)
			}

			fmt.Printf("\n📊 Extracted: %d data items, %d links, %d images, %d tables\n",
				len(data.Data), len(data.Links), len(data.Images), len(data.Tables))
			fmt.Printf("📄 Page type: %s\n", data.Type)

			return nil
		},
	}

	cmd.Flags().StringVarP(&extractOutput, "output", "o", "", "save output to file")
	return cmd
}

// dashboardCmd creates the "dashboard" subcommand.
func dashboardCmd() *cobra.Command {
	var (
		dashAddr string
	)

	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Launch the web dashboard",
		Long:  "Start the ScrapeGoat web dashboard for monitoring crawl jobs, viewing metrics, and managing workers.",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()

			// Parse port from address
			dashPort := 8080
			if len(dashAddr) > 1 && dashAddr[0] == ':' {
				fmt.Sscanf(dashAddr[1:], "%d", &dashPort)
			}

			// Create a stats provider that serves demo data
			provider := &dashboardStatsProvider{}
			dash := dashboard.NewDashboard(dashPort, provider, logger)

			if err := dash.Start(); err != nil {
				return fmt.Errorf("start dashboard: %w", err)
			}

			fmt.Printf("🐐 ScrapeGoat Dashboard running at http://localhost%s\n", dashAddr)
			fmt.Println("\nPress Ctrl+C to stop.")

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			<-sigCh

			fmt.Println("\nDashboard stopped.")
			return nil
		},
	}

	cmd.Flags().StringVar(&dashAddr, "addr", ":8080", "dashboard listen address")
	return cmd
}

// dashboardStatsProvider implements dashboard.StatsProvider for standalone mode.
type dashboardStatsProvider struct{}

func (p *dashboardStatsProvider) GetStats() map[string]any {
	return map[string]any{
		"requests_sent":    int64(0),
		"items_scraped":    int64(0),
		"bytes_downloaded": int64(0),
		"workers_active":   int64(0),
		"queue_size":       int64(0),
	}
}

func (p *dashboardStatsProvider) GetState() string {
	return "idle"
}

// benchmarkCmd creates the "benchmark" subcommand.
func benchmarkCmd() *cobra.Command {
	var (
		bConcurrency int
		bDuration    string
		bRequests    int
	)

	cmd := &cobra.Command{
		Use:   "benchmark [url]",
		Short: "Run performance benchmarks",
		Long:  "Benchmark ScrapeGoat's fetching performance against a target URL. Measures requests/sec, latency percentiles, and throughput.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()

			duration, err := time.ParseDuration(bDuration)
			if err != nil {
				return fmt.Errorf("invalid duration: %w", err)
			}

			cfg := &benchmark.BenchmarkConfig{
				URL:         args[0],
				Concurrency: bConcurrency,
				Duration:    duration,
				Requests:    bRequests,
				Headers: map[string]string{
					"User-Agent": "ScrapeGoat-Benchmark/1.0",
				},
			}

			fmt.Println("🐐 ScrapeGoat Benchmark")
			fmt.Printf("   Target:      %s\n", cfg.URL)
			fmt.Printf("   Concurrency: %d\n", cfg.Concurrency)
			fmt.Printf("   Duration:    %s\n", cfg.Duration)
			fmt.Println()

			b := benchmark.NewBenchmark(logger)
			result := b.Run(cfg)
			benchmark.PrintResult(result)

			return nil
		},
	}

	cmd.Flags().IntVar(&bConcurrency, "concurrency", 10, "number of concurrent workers")
	cmd.Flags().StringVar(&bDuration, "duration", "10s", "benchmark duration")
	cmd.Flags().IntVar(&bRequests, "requests", 0, "max requests (0 = unlimited)")
	return cmd
}

// scaleCmd creates the "scale" subcommand.
func scaleCmd() *cobra.Command {
	var masterAddr string

	cmd := &cobra.Command{
		Use:   "scale [workers]",
		Short: "Scale worker count up or down",
		Long:  "Adjust the number of distributed crawl workers in the cluster.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("📊 Requesting scale to %s workers at %s...\n", args[0], masterAddr)

			resp, err := http.Get(masterAddr + "/api/scale")
			if err != nil {
				return fmt.Errorf("contact master: %w", err)
			}
			defer resp.Body.Close()

			var status map[string]any
			json.NewDecoder(resp.Body).Decode(&status)

			fmt.Printf("✅ Scale request sent. Current workers: %v\n", status["current_workers"])
			return nil
		},
	}

	cmd.Flags().StringVar(&masterAddr, "master", "http://localhost:8081", "master node address")
	return cmd
}

// generateProject creates a new project directory with config and spider scaffold.
func generateProject(name string) error {
	dir := name
	if err := os.MkdirAll(filepath.Join(dir, "spiders"), 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	// Create scrapegoat.yaml config
	configContent := fmt.Sprintf(`# ScrapeGoat Project Configuration
name: %s
version: "1.0.0"

engine:
  concurrency: 10
  max_depth: 5
  politeness_delay: 1s
  request_timeout: 30s
  respect_robots_txt: true
  max_retries: 3

fetcher:
  type: http
  follow_redirects: true
  max_body_size: 10485760

browser:
  render: false
  browser_type: chromium
  headless: true
  wait_time: 3s

proxy:
  enabled: false
  rotation: round_robin
  urls: []

middleware:
  request:
    - name: header_rotation
      enabled: true
    - name: request_fingerprint
      enabled: true
    - name: captcha_detection
      enabled: true
    - name: cloudflare_detection
      enabled: true

pipeline:
  middlewares:
    - name: trim
    - name: required_fields
      options:
        fields:
          - title

storage:
  type: json
  output_path: ./output/%s
  batch_size: 100

distributed:
  enabled: false
  master_addr: ":8081"
  redis_addr: "localhost:6379"

metrics:
  enabled: true
  port: 9090
  path: /metrics

logging:
  level: info
  format: text
`, name, name)

	if err := os.WriteFile(filepath.Join(dir, "scrapegoat.yaml"), []byte(configContent), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Create spider scaffold
	if err := generateSpiderInDir(filepath.Join(dir, "spiders"), name); err != nil {
		return err
	}

	// Create go.mod
	goMod := fmt.Sprintf(`module %s

go 1.22

require (
	github.com/IshaanNene/ScrapeGoat v0.1.0
	github.com/PuerkitoBio/goquery v1.8.1
)
`, name)

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		return fmt.Errorf("write go.mod: %w", err)
	}

	fmt.Printf("✅ Created ScrapeGoat project: %s/\n\n", dir)
	fmt.Printf("   %s/\n", dir)
	fmt.Printf("   ├── scrapegoat.yaml     # Project configuration\n")
	fmt.Printf("   ├── go.mod              # Go module\n")
	fmt.Printf("   └── spiders/\n")
	fmt.Printf("       └── main.go         # Your spider\n")
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  1. cd %s\n", dir)
	fmt.Printf("  2. Edit spiders/main.go with your target URLs and selectors\n")
	fmt.Printf("  3. Run: go run ./spiders/\n")

	return nil
}

// generateSpiderInDir creates a spider file in the given directory.
func generateSpiderInDir(dir, name string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	template := fmt.Sprintf(`package main

import (
	"fmt"
	"log"

	"github.com/PuerkitoBio/goquery"
	scrapegoat "github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat"
)

type %sSpider struct{}

func (s *%sSpider) Name() string { return %q }

func (s *%sSpider) StartURLs() []string {
	return []string{
		"https://example.com",
	}
}

func (s *%sSpider) Parse(resp *scrapegoat.Response) (*scrapegoat.SpiderResult, error) {
	result := &scrapegoat.SpiderResult{}

	resp.Doc.Find("h1").Each(func(i int, sel *goquery.Selection) {
		item := scrapegoat.NewItem(resp.URL)
		item.Set("title", sel.Text())
		result.Items = append(result.Items, item)
	})

	return result, nil
}

func main() {
	fmt.Println("Starting %s spider...")
	err := scrapegoat.RunSpider(&%sSpider{},
		scrapegoat.WithConcurrency(5),
		scrapegoat.WithMaxDepth(2),
		scrapegoat.WithOutput("json", "./output"),
	)
	if err != nil {
		log.Fatal(err)
	}
}
`, strings.Title(name), strings.Title(name), name, strings.Title(name), strings.Title(name), name, strings.Title(name))

	return os.WriteFile(filepath.Join(dir, "main.go"), []byte(template), 0o644)
}
