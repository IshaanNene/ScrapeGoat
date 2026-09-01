// AI Web Crawler — crawls sites and uses LLM to summarize, extract entities, and analyze sentiment
// Requires Ollama running locally: ollama serve && ollama pull llama3.2
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/storage"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
	"github.com/PuerkitoBio/goquery"
)

func main() {
	fmt.Println("🤖 ScrapeGoat AI-Powered Web Crawler")
	fmt.Println("   Crawl → Extract → Summarize → NER → Sentiment (via LLM)")
	fmt.Println()

	if len(os.Args) < 2 {
		fmt.Println("Usage: go run ./examples/aicrawl/ <url> [url2] ...")
		fmt.Println()
		fmt.Println("Requires Ollama running locally:")
		fmt.Println("  ollama serve")
		fmt.Println("  ollama pull llama3.2")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  go run ./examples/aicrawl/ https://news.ycombinator.com")
		fmt.Println("  go run ./examples/aicrawl/ https://en.wikipedia.org/wiki/Artificial_intelligence")
		fmt.Println()
		fmt.Println("Environment variables:")
		fmt.Println("  SCRAPEGOAT_LLM_PROVIDER=ollama|openai|custom  (default: ollama)")
		fmt.Println("  SCRAPEGOAT_LLM_MODEL=llama3.2                 (default: llama3.2)")
		fmt.Println("  SCRAPEGOAT_LLM_ENDPOINT=http://localhost:11434 (default for ollama)")
		fmt.Println("  OPENAI_API_KEY=sk-...                        (for openai provider)")
		os.Exit(1)
	}

	seeds := os.Args[1:]
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// ── LLM Configuration ──────────────────────────────────────────
	provider := ProviderOllama
	model := "llama3.2"
	endpoint := "http://localhost:11434"

	if p := os.Getenv("SCRAPEGOAT_LLM_PROVIDER"); p != "" {
		provider = LLMProvider(p)
	}
	if m := os.Getenv("SCRAPEGOAT_LLM_MODEL"); m != "" {
		model = m
	}
	if e := os.Getenv("SCRAPEGOAT_LLM_ENDPOINT"); e != "" {
		endpoint = e
	}
	if provider == ProviderOpenAI && endpoint == "http://localhost:11434" {
		endpoint = "https://api.openai.com"
	}

	llmClient := NewLLMClient(LLMConfig{
		Provider:    provider,
		Endpoint:    endpoint,
		Model:       model,
		APIKey:      os.Getenv("OPENAI_API_KEY"),
		MaxTokens:   512,
		Temperature: 0.3,
	}, logger)

	// Quick LLM health check
	fmt.Printf("   LLM: %s/%s @ %s\n", provider, model, endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	resp, err := llmClient.Generate(ctx, "Reply with just 'ok'")
	cancel()
	if err != nil {
		fmt.Printf("   ⚠️  LLM not reachable: %v\n", err)
		fmt.Println("   Running without AI processing (just extraction)")
		fmt.Println()
		llmClient = nil
	} else {
		fmt.Printf("   ✅ LLM connected: %q\n\n", strings.TrimSpace(resp))
	}

	// ── Engine Setup ───────────────────────────────────────────────
	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = 5
	cfg.Engine.MaxDepth = 2
	cfg.Engine.PolitenessDelay = 500 * time.Millisecond
	cfg.Engine.MaxRequests = 100
	cfg.Storage.Type = "jsonl"
	cfg.Storage.OutputPath = "./output/ai_crawl"

	eng := engine.New(cfg, logger)

	// Fetcher
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, logger)
	if err != nil {
		fmt.Printf("Error creating fetcher: %v\n", err)
		os.Exit(1)
	}
	eng.SetFetcher("http", httpFetcher)

	// Parser (for link discovery)
	eng.SetParser(parser.NewCompositeParser(logger))

	// Pipeline with AI middlewares
	pipe := pipeline.New(logger)
	pipe.Use(&pipeline.TrimMiddleware{})

	if llmClient != nil {
		// AI Summarizer: summarizes body_text field
		pipe.Use(NewSummarizer(llmClient, []string{"body_text"}, 200, logger))

		// Named Entity Recognition: extracts entities from body_text
		pipe.Use(NewNERExtractor(llmClient, []string{"body_text"}, logger))

		// Sentiment Analysis: evaluates sentiment of body_text
		pipe.Use(NewSentimentAnalyzer(llmClient, []string{"body_text"}, logger))
	}

	eng.SetPipeline(pipe)

	// Storage
	store, err := storage.NewFileStorage("jsonl", "./output/ai_crawl", logger)
	if err != nil {
		fmt.Printf("Error creating storage: %v\n", err)
		os.Exit(1)
	}
	eng.SetStorage(store)

	// ── Callbacks: Extract page content ────────────────────────────
	eng.OnResponse("ai_extractor", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		doc, err := resp.Document()
		if err != nil {
			return nil, nil, err
		}

		url := resp.Request.URLString()
		item := types.NewItem(url)

		// Title
		title := strings.TrimSpace(doc.Find("title").First().Text())

		// Clean body text
		bodyClone := doc.Find("body").Clone()
		bodyClone.Find("script, style, nav, footer, header, aside").Remove()
		bodyText := strings.TrimSpace(bodyClone.Text())
		fields := strings.Fields(bodyText)
		bodyText = strings.Join(fields, " ")
		if len(bodyText) > 3000 {
			bodyText = bodyText[:3000]
		}

		// Headings
		var headings []string
		doc.Find("h1, h2, h3").Each(func(_ int, s *goquery.Selection) {
			if t := strings.TrimSpace(s.Text()); t != "" && len(t) < 200 {
				headings = append(headings, t)
			}
		})

		// Meta
		desc, _ := doc.Find("meta[name='description']").Attr("content")
		author, _ := doc.Find("meta[name='author']").Attr("content")

		if title != "" || len(bodyText) > 50 {
			item.Set("url", url)
			item.Set("title", title)
			item.Set("description", desc)
			item.Set("author", author)
			item.Set("headings", headings)
			item.Set("body_text", bodyText)
			item.Set("word_count", len(fields))
			item.Set("crawled_at", time.Now().Format(time.RFC3339))

			return []*types.Item{item}, nil, nil
		}

		return nil, nil, nil
	})

	// ── Add seeds and run ──────────────────────────────────────────
	for _, seed := range seeds {
		if err := eng.AddSeed(seed); err != nil {
			fmt.Printf("Error adding seed %q: %v\n", seed, err)
		}
	}

	fmt.Printf("   Seeds: %v\n", seeds)
	fmt.Printf("   Config: 5 workers, depth 2, max 100 pages\n")
	if llmClient != nil {
		fmt.Println("   AI Pipeline: Summarize → NER → Sentiment")
	} else {
		fmt.Println("   AI Pipeline: DISABLED (extraction only)")
	}
	fmt.Println()

	start := time.Now()
	if err := eng.Start(); err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	eng.Wait()
	elapsed := time.Since(start)

	stats := eng.Stats().Snapshot()
	fmt.Printf("\n✅ AI Crawl Complete in %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("   Pages crawled: %v\n", stats["requests_sent"])
	fmt.Printf("   Items processed: %v (through AI pipeline)\n", stats["items_scraped"])
	fmt.Printf("   Data: %v bytes\n", stats["bytes_downloaded"])
	fmt.Printf("   Output: ./output/ai_crawl/results.jsonl\n")
	if llmClient != nil {
		fmt.Println("   Fields: url, title, body_text, summary, entities, sentiment")
	}
}
