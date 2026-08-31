package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/spf13/cobra"

	"github.com/IshaanNene/ScrapeGoat/internal/apiserver"
	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/fetchlog"
	"github.com/IshaanNene/ScrapeGoat/internal/mcp"
	"github.com/IshaanNene/ScrapeGoat/internal/observability"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/provenance"
	"github.com/IshaanNene/ScrapeGoat/internal/storage"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
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
	resumeCrawl    bool
	recordDir      string
	corpusPath     string
	compliancePath string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "scrapegoat",
		Short: "ScrapeGoat — All-in-One Web Scraper/Crawler",
		Long: `ScrapeGoat is a web scraping and crawling toolkit for Go.

Crawl a site, extract structured data, or expose either to an AI agent over MCP.

  • Concurrent crawling with per-domain rate limiting and a circuit breaker
  • Structured extraction: CSS, XPath, regex, JSON-LD, and listing detection
  • SSRF-guarded fetching — see SECURITY.md for the trust boundary
  • Checkpoint pause/resume (--resume)
  • MCP server, REST API, Prometheus metrics

Tools outside the core — SEO audit, change detection, crawl graph, benchmark
harness — live in contrib/ and are built separately.`,
	}

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file path")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug logging")

	rootCmd.AddCommand(crawlCmd())
	rootCmd.AddCommand(newCmd())
	rootCmd.AddCommand(versionCmd())
	rootCmd.AddCommand(configCmd())
	rootCmd.AddCommand(extractCmd())
	rootCmd.AddCommand(mcpCmd())
	rootCmd.AddCommand(serveCmd())
	rootCmd.AddCommand(replayCmd())
	rootCmd.AddCommand(verifyCmd())

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
	cmd.Flags().BoolVar(&resumeCrawl, "resume", false, "resume from the last checkpoint instead of starting fresh")
	cmd.Flags().StringVar(&recordDir, "record", "", "record every fetch to this directory so the crawl can be replayed")
	cmd.Flags().StringVar(&corpusPath, "corpus", "", "write a provenance record per page here (.parquet or .jsonl, by extension)")
	cmd.Flags().StringVar(&compliancePath, "compliance-report", "", "write a machine-readable compliance report here (JSON; requires --corpus)")

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
	applyCLIOverrides(cmd, cfg)

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

	// With --record, the fetcher is wrapped so every response is written to a log
	// the crawl can later be replayed from. The engine cannot tell the difference,
	// which is the point: a recorded crawl is the same crawl.
	var recorder *fetchlog.Recorder
	if recordDir != "" {
		recorder, err = fetchlog.NewRecorder(httpFetcher, recordDir, nil)
		if err != nil {
			return fmt.Errorf("open fetch log: %w", err)
		}
		eng.SetFetcher("http", recorder)

		if err := writeRecordManifest(recordDir, cfg, args); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "  recording to %s\n", recordDir)
	} else {
		eng.SetFetcher("http", httpFetcher)
	}

	if err := wireCrawlPipeline(eng, cfg, logger); err != nil {
		return err
	}

	if compliancePath != "" && corpusPath == "" {
		return fmt.Errorf("--compliance-report needs --corpus: the report is derived from the corpus, " +
			"so that the two cannot disagree about the crawl they describe")
	}

	corpus, err := openCorpus(eng, recordDir)
	if err != nil {
		return err
	}
	if corpus != nil {
		defer corpus.Close()
	}

	// Setup metrics (if enabled). The recorder must be handed to the engine, not
	// merely served: without SetMetrics the endpoint comes up and reports zeroes
	// forever, which is worse than having no endpoint at all.
	if cfg.Metrics.Enabled {
		metrics := observability.NewMetrics(logger)
		eng.SetMetrics(metrics)
		if err := metrics.StartServer(cfg.Metrics.Port, cfg.Metrics.Path); err != nil {
			logger.Warn("failed to start metrics server", "error", err)
		}
	}

	// Restore prior state before seeding, so seeds already covered by the previous
	// run are filtered out by the restored dedup set rather than re-crawled.
	if resumeCrawl {
		if err := eng.ResumeFromCheckpoint(); err != nil {
			return fmt.Errorf("resume: %w", err)
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
	if seedsAdded == 0 && eng.Stats() != nil {
		// A resumed crawl legitimately adds no new seeds: the outstanding work is
		// already in the restored frontier, and the seeds are duplicates by design.
		if !resumeCrawl {
			return fmt.Errorf("all seeds were filtered or blocked — check URLs and robots.txt")
		}
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
	stats := eng.StatsSnapshot()

	// Seal the log before reporting, so the summary cannot claim a recording that
	// is still buffered.
	if recorder != nil {
		if err := finaliseRecording(recorder, recordDir); err != nil {
			logger.Error("the crawl finished but its log did not close cleanly", "error", err)
			return err
		}
	}

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

	reportCorpus(corpus)

	if stats["items_scraped"] == int64(0) {
		fmt.Println("\n  No items were scraped. The crawl command discovers and follows links by default.")
		fmt.Println("   For automatic content extraction, try:")
		fmt.Println("     scrapegoat search <url>      — extract title, headings, body text, meta")
		fmt.Println("     scrapegoat ai-crawl <url>    — AI-powered summarize, NER, sentiment")
		fmt.Println("     scrapegoat crawl <url> -c config.yaml  — use custom parse rules")
	}

	return nil
}

// openCorpus attaches a provenance corpus writer when --corpus was given.
//
// The crawl ID is the fetch log's directory when there is one, so a corpus record
// points at the log that can prove it. Without a log the records still stand on
// their own; they just cannot be re-derived.
func openCorpus(eng *engine.Engine, logDir string) (provenance.RecordWriter, error) {
	if corpusPath == "" {
		return nil, nil
	}

	// Format follows the extension. A .parquet full of JSON would be worse than
	// either, and the extension is what a reader looks at to decide how to open it.
	w, err := provenance.OpenCorpus(corpusPath)
	if err != nil {
		return nil, err
	}

	crawlID := logDir
	if crawlID == "" {
		crawlID = fmt.Sprintf("crawl-%d", time.Now().UnixNano())
	}
	eng.SetCorpusWriter(w, crawlID)

	fmt.Fprintf(os.Stderr, "  writing provenance records to %s\n", corpusPath)
	return w, nil
}

// reportCorpus prints what the corpus ended up holding.
func reportCorpus(w provenance.RecordWriter) {
	if w == nil {
		return
	}
	if err := w.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "  corpus did not close cleanly: %v\n", err)
		return
	}

	written, skipped := w.Stats()
	fmt.Printf("\n  Corpus:    %d records -> %s\n", written, w.Path())
	if skipped > 0 {
		fmt.Printf("             %d skipped for incomplete provenance\n", skipped)
	}

	records, err := provenance.ReadAnyCorpus(w.Path())
	if err != nil {
		return
	}
	s := provenance.Summarise(records)

	writeComplianceReport(records, w.Path())

	// Reported, not filtered. A crawl that had dropped these would print zero and
	// look clean; the number is the point.
	if s.Restrictive > 0 {
		fmt.Printf("             %d from sources that asked to be excluded from AI training\n", s.Restrictive)
	}
	if s.AISiteWide > 0 {
		fmt.Printf("             %d from sites whose robots.txt turns AI crawlers away\n", s.AISiteWide)
	}
	if s.Licensed > 0 {
		fmt.Printf("             %d carry an explicit licence\n", s.Licensed)
	}
	if s.RobotsDisallowed > 0 {
		fmt.Printf("             ! %d were fetched despite robots.txt — investigate\n", s.RobotsDisallowed)
	}
}

// writeComplianceReport emits the auditable account of what the crawl respected.
//
// Derived from the corpus rather than tallied alongside it. A report that counted
// independently could disagree with the data it describes, and then neither would
// be worth anything to the person holding both.
func writeComplianceReport(records []provenance.Record, corpus string) {
	if compliancePath == "" {
		return
	}

	// 10,000 keeps the report a document rather than a second copy of the corpus.
	// When the cap bites the report says so, because a silently truncated audit is
	// worse than a long one.
	const maxListed = 10_000

	report := provenance.BuildComplianceReport(records, recordDir, corpus, maxListed)
	if err := provenance.WriteComplianceReport(compliancePath, report); err != nil {
		fmt.Fprintf(os.Stderr, "  could not write the compliance report: %v\n", err)
		return
	}

	fmt.Printf("\n  Compliance report -> %s\n", compliancePath)
	for _, w := range report.Warnings {
		fmt.Printf("             ! %s\n", w)
	}
}

// wireCrawlPipeline attaches the parser, pipeline, and storage to an engine.
//
// Shared by crawl and replay deliberately. A replay that built its own pipeline
// would be reproducing the fetches but not the processing, and the first time the
// two drifted the replay would produce different output from the same responses —
// which is precisely the failure the fetch log exists to rule out.
func wireCrawlPipeline(eng *engine.Engine, cfg *config.Config, logger *slog.Logger) error {
	eng.SetParser(parser.NewCompositeParser(logger))

	pipe := pipeline.New(logger)
	pipe.Use(&pipeline.TrimMiddleware{})
	eng.SetPipeline(pipe)

	store, err := storage.NewFileStorageOrdered(
		cfg.Storage.Type, cfg.Storage.OutputPath, logger, cfg.Storage.DeterministicOrder)
	if err != nil {
		return fmt.Errorf("create storage: %w", err)
	}
	eng.SetStorage(store)

	return nil
}

// writeRecordManifest stamps the log with what produced it.
//
// Written before the crawl rather than after, so that a run killed halfway still
// leaves a log that says what it was. FinishedAt is filled in at the end; its
// absence is how a reader tells a truncated crawl from a complete one.
func writeRecordManifest(dir string, cfg *config.Config, seeds []string) error {
	hash, err := fetchlog.HashConfig(cfg)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config for manifest: %w", err)
	}

	return fetchlog.WriteManifest(dir, fetchlog.Manifest{
		Version:    config.Version,
		Seeds:      seeds,
		ConfigHash: hash,
		Config:     raw,
		StartedAt:  time.Now(),
	})
}

// finaliseRecording closes the log and completes its manifest.
func finaliseRecording(rec *fetchlog.Recorder, dir string) error {
	stats, err := rec.Store().Stat()
	if err != nil {
		return fmt.Errorf("stat fetch log: %w", err)
	}
	if err := rec.Close(); err != nil {
		return fmt.Errorf("close fetch log: %w", err)
	}

	entries, err := fetchlog.ReadLog(dir)
	if err != nil {
		return fmt.Errorf("read back fetch log: %w", err)
	}

	m, err := fetchlog.ReadManifest(dir)
	if err != nil {
		return err
	}
	m.FinishedAt = time.Now()
	m.Entries = int64(len(entries))
	m.Objects = stats.Objects
	m.Bytes = stats.Bytes

	if err := fetchlog.WriteManifest(dir, m); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "\n  Recorded %d fetches (%d objects, %d bytes) to %s\n",
		len(entries), stats.Objects, stats.Bytes, dir)
	fmt.Fprintf(os.Stderr, "   Replay:  scrapegoat replay %s\n", dir)
	fmt.Fprintf(os.Stderr, "   Verify:  scrapegoat verify %s\n", dir)

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
func applyCLIOverrides(cmd *cobra.Command, cfg *config.Config) {
	// Only when the flag was actually typed. Assigning unconditionally meant the
	// flag's own default (3) overwrote whatever the config file said, so
	// `max_depth: 10` in a config was silently ignored unless -d was also passed —
	// the config value was unreachable through the CLI.
	if flagChanged(cmd, "depth") {
		cfg.Engine.MaxDepth = depth
	}
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
	// Same trap as depth: these flags have non-empty defaults, so an unconditional
	// assignment meant `-o`'s default of ./output silently overwrote whatever
	// output_path a config file set. A crawl configured to write to one place
	// wrote to another and said so only in a line nobody reads.
	if flagChanged(cmd, "output") {
		cfg.Storage.OutputPath = outputPath
	}
	if flagChanged(cmd, "format") {
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

// flagChanged reports whether the user actually set a flag, as opposed to cobra
// filling in its default. A nil command means no flags were parsed at all, which
// is the case in tests that call a RunE directly.
func flagChanged(cmd *cobra.Command, name string) bool {
	if cmd == nil {
		return false
	}
	return cmd.Flags().Changed(name)
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
`, cases.Title(language.Und).String(name), cases.Title(language.Und).String(name), cases.Title(language.Und).String(name), name,
		cases.Title(language.Und).String(name), cases.Title(language.Und).String(name), name, cases.Title(language.Und).String(name), name)

	if err := os.WriteFile(mainFile, []byte(template), 0o600); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	fmt.Printf("✅ Created spider scaffold:\n")
	fmt.Printf("   %s/main.go\n\n", dir)
	fmt.Printf("Next steps:\n")
	fmt.Printf("  1. Edit %s — update StartURLs() and Parse()\n", mainFile)
	fmt.Printf("  2. Run:  go run ./%s/\n", dir)

	return nil
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

			// Progress and summary go to stderr so that stdout is nothing but the
			// JSON document. The first thing anyone does with a JSON-emitting CLI
			// is pipe it to jq, and mixing decoration into stdout breaks that.
			fmt.Fprintf(os.Stderr, "🔍 Extracting structured data from %s...\n", args[0])

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
				fmt.Fprintf(os.Stderr, "📁 Saved to %s\n", extractOutput)
			}

			fmt.Fprintf(os.Stderr, "📊 Extracted: %d data items, %d links, %d images, %d tables\n",
				len(data.Data), len(data.Links), len(data.Images), len(data.Tables))
			fmt.Fprintf(os.Stderr, "📄 Page type: %s\n", data.Type)

			return nil
		},
	}

	cmd.Flags().StringVarP(&extractOutput, "output", "o", "", "save output to file")
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

metrics:
  enabled: true
  port: 9090
  path: /metrics

logging:
  level: info
  format: text
`, name, name)

	if err := os.WriteFile(filepath.Join(dir, "scrapegoat.yaml"), []byte(configContent), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Create spider scaffold
	if err := generateSpiderInDir(filepath.Join(dir, "spiders"), name); err != nil {
		return err
	}

	// Create go.mod
	// Only the module line is written. Versions are left to `go mod tidy`, which
	// runs below: pinning them here means the scaffold ships a version that is
	// stale the day after the next release, and a go.sum that does not exist yet.
	goMod := fmt.Sprintf(`module %s

go 1.25
`, name)

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		return fmt.Errorf("write go.mod: %w", err)
	}

	// Resolve dependencies now, so the project builds the moment it is created.
	// Without this the next step in the printed instructions — `go run ./spiders/`
	// — fails on missing go.sum entries, which is a poor first five minutes.
	tidied := true
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	tidy.Stderr = os.Stderr
	if err := tidy.Run(); err != nil {
		tidied = false
		fmt.Fprintf(os.Stderr, "\n⚠️  could not run `go mod tidy` in %s: %v\n", dir, err)
	}

	fmt.Printf("✅ Created ScrapeGoat project: %s/\n\n", dir)
	fmt.Printf("   %s/\n", dir)
	fmt.Printf("   ├── scrapegoat.yaml     # Project configuration\n")
	fmt.Printf("   ├── go.mod              # Go module\n")
	fmt.Printf("   └── spiders/\n")
	fmt.Printf("       └── main.go         # Your spider\n")
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  1. cd %s\n", dir)
	if !tidied {
		fmt.Printf("  2. go mod tidy\n")
		fmt.Printf("  3. Edit spiders/main.go with your target URLs and selectors\n")
		fmt.Printf("  4. go run ./spiders/\n")
	} else {
		fmt.Printf("  2. go run ./spiders/          # works as-is against example.com\n")
		fmt.Printf("  3. Edit spiders/main.go with your own URLs and selectors\n")
	}

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
`, cases.Title(language.Und).String(name), cases.Title(language.Und).String(name), name, cases.Title(language.Und).String(name), cases.Title(language.Und).String(name), name, cases.Title(language.Und).String(name))

	return os.WriteFile(filepath.Join(dir, "main.go"), []byte(template), 0o644)
}

// mcpCmd creates the "mcp" subcommand to start the MCP server.
func mcpCmd() *cobra.Command {
	var (
		mcpTransport string
		mcpPort      int
		mcpAPIKey    string
	)

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Start the MCP (Model Context Protocol) server",
		Long: `Start an MCP server so AI agents (Claude, GPT-4, Cursor, Cline) can use
ScrapeGoat as a native tool. Supports stdio and HTTP transports.

Stdio transport (default):
  Reads JSON-RPC 2.0 from stdin, writes responses to stdout.
  Use this for Claude Desktop and Cursor integration.

HTTP transport:
  Starts an HTTP server with JSON-RPC endpoint and SSE streaming.
  Requires an API key for authentication (--api-key or SCRAPEGOAT_API_KEY).

Examples:
  scrapegoat mcp                                  # stdio mode (for Claude Desktop)
  scrapegoat mcp --transport=http --port=8090      # HTTP mode
  scrapegoat mcp --transport=http --api-key=sk-... # HTTP with auth`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()

			// For HTTP transport, require an API key.
			if mcpTransport == "http" && mcpAPIKey == "" {
				mcpAPIKey = os.Getenv("SCRAPEGOAT_API_KEY")
				if mcpAPIKey == "" {
					return fmt.Errorf("HTTP transport requires an API key: use --api-key or set SCRAPEGOAT_API_KEY")
				}
			}

			server := mcp.NewServer(logger, mcpAPIKey)

			switch mcpTransport {
			case "stdio":
				server.SetTransport(mcp.NewStdioTransport(logger))
			case "http":
				server.SetTransport(mcp.NewHTTPTransport(mcpPort, mcpAPIKey, logger))
				fmt.Printf("🐐 ScrapeGoat MCP Server (HTTP) at http://localhost:%d/mcp\n", mcpPort)
				fmt.Printf("   SSE endpoint: http://localhost:%d/mcp/sse\n", mcpPort)
				fmt.Printf("   API Key: %s...%s\n", mcpAPIKey[:4], mcpAPIKey[len(mcpAPIKey)-4:])
				fmt.Println("\nPress Ctrl+C to stop.")
			default:
				return fmt.Errorf("unknown transport %q (available: stdio, http)", mcpTransport)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				cancel()
			}()

			return server.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&mcpTransport, "transport", "stdio", "transport type: stdio or http")
	cmd.Flags().IntVar(&mcpPort, "port", 8090, "HTTP server port (only for http transport)")
	cmd.Flags().StringVar(&mcpAPIKey, "api-key", "", "API key for HTTP transport authentication (or set SCRAPEGOAT_API_KEY)")

	return cmd
}

// serveCmd creates the "serve" subcommand to start the API server.
func serveCmd() *cobra.Command {
	var (
		servePort    int
		serveAPIKey  string
		serveDB      string
		serveNoAuth  bool
		serveOrigins []string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the REST/WebSocket API server",
		Long: `Start a headless API server for programmatic access to ScrapeGoat.

Provides REST endpoints for crawling, extraction, and job management,
plus WebSocket streaming for real-time item delivery.

Examples:
  scrapegoat serve --port=8080 --api-key=sk-...
  scrapegoat serve --db=./jobs.db
  SCRAPEGOAT_API_KEY=sk-... scrapegoat serve`,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := setupLogger()
			cfg := config.DefaultConfig()

			if servePort > 0 {
				cfg.APIServer.Port = servePort
			}
			if cfg.APIServer.Port == 0 {
				cfg.APIServer.Port = 8080
			}

			if serveAPIKey != "" {
				cfg.APIServer.APIKey = serveAPIKey
			}
			if cfg.APIServer.APIKey == "" {
				cfg.APIServer.APIKey = os.Getenv("SCRAPEGOAT_API_KEY")
			}

			if serveDB != "" {
				cfg.APIServer.DBPath = serveDB
			}
			if cfg.APIServer.DBPath == "" {
				cfg.APIServer.DBPath = "./scrapegoat_jobs.db"
			}

			cfg.APIServer.CORS = true
			cfg.APIServer.AllowNoAuth = serveNoAuth
			cfg.APIServer.AllowedOrigins = serveOrigins

			srv, err := apiserver.NewServer(cfg, logger)
			if err != nil {
				return fmt.Errorf("create server: %w", err)
			}

			fmt.Printf("🐐 ScrapeGoat API Server at http://localhost:%d\n", cfg.APIServer.Port)
			fmt.Printf("   Health: http://localhost:%d/health\n", cfg.APIServer.Port)
			if cfg.APIServer.APIKey != "" {
				fmt.Printf("   API Key: %s...%s\n", cfg.APIServer.APIKey[:4], cfg.APIServer.APIKey[len(cfg.APIServer.APIKey)-4:])
			} else {
				fmt.Println("   ⚠️  Authentication DISABLED (--insecure-no-auth)")
			}
			fmt.Println("\nPress Ctrl+C to stop.")

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				cancel()
			}()

			return srv.Start(ctx)
		},
	}

	cmd.Flags().IntVar(&servePort, "port", 8080, "API server port")
	cmd.Flags().StringVar(&serveAPIKey, "api-key", "", "API key for authentication (or set SCRAPEGOAT_API_KEY)")
	cmd.Flags().StringVar(&serveDB, "db", "", "SQLite database path for job persistence")
	cmd.Flags().BoolVar(&serveNoAuth, "insecure-no-auth", false,
		"start without an API key — every endpoint is open to anything that can reach the port")
	cmd.Flags().StringSliceVar(&serveOrigins, "allowed-origin", nil,
		"origin permitted by CORS and WebSocket upgrades; repeatable. Empty means same-origin only")

	return cmd
}
