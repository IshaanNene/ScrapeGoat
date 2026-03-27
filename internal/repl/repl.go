package repl

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
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

// REPL provides an interactive command-line interface for ScrapeGoat.
type REPL struct {
	cfg    *config.Config
	engine *engine.Engine
	logger *slog.Logger
	reader *bufio.Reader
}

// New creates a new REPL instance.
func New(cfg *config.Config, logger *slog.Logger) *REPL {
	return &REPL{
		cfg:    cfg,
		logger: logger,
		reader: bufio.NewReader(os.Stdin),
	}
}

// Start begins the interactive REPL loop.
func (r *REPL) Start() {
	fmt.Println("🕸️  ScrapeGoat Interactive Shell")
	fmt.Println("   Type 'help' for available commands, 'exit' to quit.")
	fmt.Println()

	for {
		fmt.Print("scrapegoat> ")
		line, err := r.reader.ReadString('\n')
		if err != nil {
			break
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		cmd := parts[0]
		args := parts[1:]

		switch cmd {
		case "help", "?":
			r.printHelp()
		case "exit", "quit", "q":
			fmt.Println("Goodbye! 👋")
			return
		case "fetch":
			r.cmdFetch(args)
		case "crawl":
			r.cmdCrawl(args)
		case "parse":
			r.cmdParse(args)
		case "status":
			r.cmdStatus()
		case "stop":
			r.cmdStop()
		case "pause":
			r.cmdPause()
		case "resume":
			r.cmdResume()
		case "config":
			r.cmdConfig()
		case "set":
			r.cmdSet(args)
		case "stats":
			r.cmdStats()
		case "selectors":
			r.cmdSelectors(args)
		case "links":
			r.cmdLinks(args)
		case "clear":
			fmt.Print("\033[H\033[2J")
		default:
			fmt.Printf("Unknown command: %s. Type 'help' for available commands.\n", cmd)
		}
	}
}

func (r *REPL) printHelp() {
	fmt.Println(`
Available Commands:
  fetch <url>           Fetch a single URL and show response info
  crawl <url> [depth]   Start crawling from a URL
  parse <url> <sel>     Fetch URL and extract text using CSS selector
  selectors <url>       Auto-generate selectors for a URL
  links <url>           Extract all links from a URL

  status                Show engine status
  stats                 Show crawl statistics
  stop                  Stop crawling
  pause                 Pause crawling
  resume                Resume crawling

  config                Show current configuration
  set <key> <value>     Update a config value (e.g. set depth 3)

  clear                 Clear the screen
  help                  Show this help
  exit                  Exit the REPL`)
}

func (r *REPL) cmdFetch(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: fetch <url>")
		return
	}

	url := args[0]
	fmt.Printf("Fetching %s...\n", url)

	httpFetcher, err := fetcher.NewHTTPFetcher(r.cfg, r.logger)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer httpFetcher.Close()

	req, err := types.NewRequest(url)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := httpFetcher.Fetch(ctx, req)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("\n  Status:       %d\n", resp.StatusCode)
	fmt.Printf("  Content-Type: %s\n", resp.ContentType)
	fmt.Printf("  Size:         %d bytes\n", resp.ContentLength)
	fmt.Printf("  Final URL:    %s\n", resp.FinalURL)
	fmt.Printf("  Duration:     %s\n", resp.FetchDuration)

	if len(resp.Body) > 500 {
		fmt.Printf("  Body:         %s...\n", string(resp.Body[:500]))
	} else {
		fmt.Printf("  Body:         %s\n", string(resp.Body))
	}
}

func (r *REPL) cmdCrawl(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: crawl <url> [depth]")
		return
	}

	url := args[0]
	depth := r.cfg.Engine.MaxDepth
	if len(args) > 1 {
		_, _ = fmt.Sscanf(args[1], "%d", &depth)
	}

	fmt.Printf("Starting crawl: %s (depth: %d)\n", url, depth)

	eng := engine.New(r.cfg, r.logger)

	httpFetcher, _ := fetcher.NewHTTPFetcher(r.cfg, r.logger)
	eng.SetFetcher("http", httpFetcher)
	eng.SetParser(parser.NewCompositeParser(r.logger))

	pipe := pipeline.New(r.logger)
	eng.SetPipeline(pipe)

	store, _ := storage.NewFileStorage(r.cfg.Storage.Type, r.cfg.Storage.OutputPath, r.logger)
	eng.SetStorage(store)

	_ = eng.AddSeed(url)
	r.engine = eng

	go func() {
		_ = eng.Start()
		eng.Wait()
		fmt.Println("\n✅ Crawl complete!")
		fmt.Print("scrapegoat> ")
	}()

	fmt.Println("Crawl running in background. Use 'stats' to check progress, 'stop' to halt.")
}

func (r *REPL) cmdParse(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: parse <url> <css-selector>")
		return
	}

	url := args[0]
	selector := strings.Join(args[1:], " ")
	fmt.Printf("Fetching %s and extracting '%s'...\n", url, selector)

	httpFetcher, _ := fetcher.NewHTTPFetcher(r.cfg, r.logger)
	defer httpFetcher.Close()

	req, _ := types.NewRequest(url)
	ctx := context.Background()
	resp, err := httpFetcher.Fetch(ctx, req)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	doc, err := resp.Document()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	doc.Find(selector).Each(func(i int, sel *goquery.Selection) {
		fmt.Printf("  [%d] %s\n", i+1, strings.TrimSpace(sel.Text()))
	})
}

func (r *REPL) cmdSelectors(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: selectors <url>")
		return
	}
	fmt.Printf("Auto-generating selectors for %s...\n", args[0])
	fmt.Println("  (Use 'parse <url> <selector>' to test a selector)")
}

func (r *REPL) cmdLinks(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: links <url>")
		return
	}

	httpFetcher, _ := fetcher.NewHTTPFetcher(r.cfg, r.logger)
	defer httpFetcher.Close()

	req, _ := types.NewRequest(args[0])
	resp, err := httpFetcher.Fetch(context.Background(), req)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	cssParser := parser.NewCSSParser(r.logger)
	_, links, _ := cssParser.Parse(resp, nil)

	fmt.Printf("\nFound %d links:\n", len(links))
	for i, link := range links {
		if i >= 20 {
			fmt.Printf("  ... and %d more\n", len(links)-20)
			break
		}
		fmt.Printf("  %s\n", link)
	}
}

func (r *REPL) cmdStatus() {
	if r.engine == nil {
		fmt.Println("No crawl running.")
		return
	}
	fmt.Printf("Engine State: %s\n", r.engine.GetState())
}

func (r *REPL) cmdStop() {
	if r.engine != nil {
		r.engine.Stop()
		fmt.Println("Stopping...")
	} else {
		fmt.Println("No crawl running.")
	}
}

func (r *REPL) cmdPause() {
	if r.engine != nil {
		r.engine.Pause()
		fmt.Println("Paused.")
	}
}

func (r *REPL) cmdResume() {
	if r.engine != nil {
		r.engine.Resume()
		fmt.Println("Resumed.")
	}
}

func (r *REPL) cmdConfig() {
	fmt.Printf("  Concurrency:  %d\n", r.cfg.Engine.Concurrency)
	fmt.Printf("  Max Depth:    %d\n", r.cfg.Engine.MaxDepth)
	fmt.Printf("  Delay:        %s\n", r.cfg.Engine.PolitenessDelay)
	fmt.Printf("  Output:       %s (%s)\n", r.cfg.Storage.OutputPath, r.cfg.Storage.Type)
	fmt.Printf("  Robots:       %v\n", r.cfg.Engine.RespectRobotsTxt)
}

func (r *REPL) cmdSet(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: set <key> <value>")
		fmt.Println("  Keys: depth, concurrency, delay, output, format")
		return
	}

	key := args[0]
	val := args[1]

	switch key {
	case "depth":
		_, _ = fmt.Sscanf(val, "%d", &r.cfg.Engine.MaxDepth)
		fmt.Printf("  Max depth set to %d\n", r.cfg.Engine.MaxDepth)
	case "concurrency":
		_, _ = fmt.Sscanf(val, "%d", &r.cfg.Engine.Concurrency)
		fmt.Printf("  Concurrency set to %d\n", r.cfg.Engine.Concurrency)
	case "delay":
		d, err := time.ParseDuration(val)
		if err == nil {
			r.cfg.Engine.PolitenessDelay = d
			fmt.Printf("  Delay set to %s\n", d)
		}
	case "output":
		r.cfg.Storage.OutputPath = val
		fmt.Printf("  Output path set to %s\n", val)
	case "format":
		r.cfg.Storage.Type = val
		fmt.Printf("  Output format set to %s\n", val)
	default:
		fmt.Printf("  Unknown key: %s\n", key)
	}
}

func (r *REPL) cmdStats() {
	if r.engine == nil {
		fmt.Println("No crawl running.")
		return
	}
	stats := r.engine.Stats().Snapshot()
	for k, v := range stats {
		fmt.Printf("  %-20s %v\n", k, v)
	}
}
