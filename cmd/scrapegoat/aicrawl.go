package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/spf13/cobra"

	"github.com/IshaanNene/ScrapeGoat/internal/ai"
	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/storage"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

var (
	llmProvider  string
	llmModel     string
	llmEndpoint  string
	aiMaxPages   int
	aiDepth      int
	aiConcurrent int
	aiOutput     string
	aiDelay      string
)

// aiCrawlCmd creates the "ai-crawl" subcommand.
func aiCrawlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai-crawl [url]",
		Short: "AI-powered crawler — crawl, summarize, extract entities, analyze sentiment",
		Long: `Crawl a website and process each page through an AI pipeline.

Pipeline stages (powered by LLM):
  1. Extract clean body text, headings, and metadata
  2. Summarize content (200 words)
  3. Named Entity Recognition (people, orgs, locations)
  4. Sentiment analysis (positive/negative/neutral)

Supports Ollama (local), OpenAI, or any compatible API.

Requires:
  ollama serve && ollama pull llama3.2`,
		Args: cobra.MinimumNArgs(1),
		RunE: runAICrawl,
	}

	cmd.Flags().StringVar(&llmProvider, "llm", "ollama", "LLM provider: ollama, openai, custom")
	cmd.Flags().StringVar(&llmModel, "model", "llama3.2:3b", "LLM model name (run 'ollama list' to see installed models)")
	cmd.Flags().StringVar(&llmEndpoint, "llm-endpoint", "", "LLM endpoint URL (default: auto)")
	cmd.Flags().IntVar(&aiMaxPages, "max-pages", 50, "maximum pages to crawl")
	cmd.Flags().IntVarP(&aiDepth, "depth", "d", 2, "maximum crawl depth")
	cmd.Flags().IntVarP(&aiConcurrent, "concurrency", "n", 5, "number of concurrent workers")
	cmd.Flags().StringVarP(&aiOutput, "output", "o", "./output/ai_crawl", "output directory")
	cmd.Flags().StringVar(&aiDelay, "delay", "500ms", "delay between requests")

	return cmd
}

func runAICrawl(cmd *cobra.Command, args []string) error {
	logger := setupLogger()

	// LLM setup
	provider := ai.LLMProvider(llmProvider)
	endpoint := llmEndpoint
	if endpoint == "" {
		switch provider {
		case ai.ProviderOllama:
			endpoint = "http://localhost:11434"
		case ai.ProviderOpenAI:
			endpoint = "https://api.openai.com"
		}
	}

	llmClient := ai.NewLLMClient(ai.LLMConfig{
		Provider:    provider,
		Endpoint:    endpoint,
		Model:       llmModel,
		APIKey:      os.Getenv("OPENAI_API_KEY"),
		MaxTokens:   512,
		Temperature: 0.3,
	}, logger)

	fmt.Printf("🤖 AI-Powered Web Crawler\n")
	fmt.Printf("   LLM: %s/%s @ %s\n", provider, llmModel, endpoint)

	// Health check — 30s timeout to handle Ollama model cold-start (~8s on first load)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	resp, err := llmClient.Generate(ctx, "Reply with just 'ok'")
	cancel()

	llmAvailable := err == nil && strings.TrimSpace(resp) != ""
	if llmAvailable {
		fmt.Printf("     LLM connected: %q\n", strings.TrimSpace(resp))
	} else {
		if err != nil {
			fmt.Printf("     LLM not reachable: %v\n", err)
		} else {
			fmt.Printf("     LLM returned empty response — model '%s' may not be installed.\n", llmModel)
			fmt.Printf("   Run: ollama list   to see installed models\n")
		}
		fmt.Println("   Running without AI processing (extraction only)")
	}

	// Engine config
	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = aiConcurrent
	cfg.Engine.MaxDepth = aiDepth
	cfg.Engine.MaxRequests = aiMaxPages
	cfg.Storage.Type = "jsonl"
	cfg.Storage.OutputPath = aiOutput

	if d, err := time.ParseDuration(aiDelay); err == nil {
		cfg.Engine.PolitenessDelay = d
	}

	eng := engine.New(cfg, logger)

	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, logger)
	if err != nil {
		return fmt.Errorf("create fetcher: %w", err)
	}
	eng.SetFetcher("http", httpFetcher)
	eng.SetParser(parser.NewCompositeParser(logger))

	// Pipeline: trim + AI stages
	pipe := pipeline.New(logger)
	pipe.Use(&pipeline.TrimMiddleware{})

	if llmAvailable {
		pipe.Use(ai.NewSummarizer(llmClient, []string{"body_text"}, 200, logger))
		pipe.Use(ai.NewNERExtractor(llmClient, []string{"body_text"}, logger))
		pipe.Use(ai.NewSentimentAnalyzer(llmClient, []string{"body_text"}, logger))
	}

	eng.SetPipeline(pipe)

	store, err := storage.NewFileStorage("jsonl", aiOutput, logger)
	if err != nil {
		return fmt.Errorf("create storage: %w", err)
	}
	eng.SetStorage(store)

	// Content extractor callback
	eng.OnResponse("ai_extractor", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		doc, err := resp.Document()
		if err != nil {
			return nil, nil, err
		}

		url := resp.Request.URLString()
		item := types.NewItem(url)

		title := strings.TrimSpace(doc.Find("title").First().Text())
		desc, _ := doc.Find("meta[name='description']").Attr("content")
		author, _ := doc.Find("meta[name='author']").Attr("content")

		var headings []string
		doc.Find("h1, h2, h3").Each(func(_ int, s *goquery.Selection) {
			if t := strings.TrimSpace(s.Text()); t != "" && len(t) < 200 {
				headings = append(headings, t)
			}
		})

		bodyClone := doc.Find("body").Clone()
		bodyClone.Find("script, style, nav, footer, header, aside").Remove()
		bodyText := strings.TrimSpace(bodyClone.Text())
		words := strings.Fields(bodyText)
		bodyText = strings.Join(words, " ")
		if len(bodyText) > 3000 {
			bodyText = bodyText[:3000]
		}

		// Skip thin pages — utility/nav pages like Special:Search, upload wizards etc.
		if title != "" || len(words) > 200 {
			item.Set("url", url)
			item.Set("title", title)
			item.Set("description", desc)
			item.Set("author", author)
			item.Set("headings", headings)
			item.Set("body_text", bodyText)
			item.Set("word_count", len(words))
			item.Set("crawled_at", time.Now().Format(time.RFC3339))
			return []*types.Item{item}, nil, nil
		}
		return nil, nil, nil
	})

	var seedsAdded int
	for _, rawURL := range args {
		if err := eng.AddSeed(rawURL); err != nil {
			logger.Warn("seed skipped", "url", rawURL, "reason", err)
		} else {
			seedsAdded++
		}
	}
	if seedsAdded == 0 {
		return fmt.Errorf("all seeds were filtered or blocked")
	}

	fmt.Printf("   Seeds: %v\n", args)
	fmt.Printf("   Config: %d workers, depth %d, max %d pages\n", aiConcurrent, aiDepth, aiMaxPages)
	if llmAvailable {
		fmt.Println("   Pipeline: Extract → Summarize → NER → Sentiment")
	} else {
		fmt.Println("   Pipeline: Extract only (no LLM)")
	}
	fmt.Println()

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
	fmt.Printf("\n✅ AI Crawl Complete in %s\n", time.Since(start).Round(time.Millisecond))
	fmt.Printf("   Pages crawled: %v\n", stats["requests_sent"])
	fmt.Printf("   Items processed: %v (through AI pipeline)\n", stats["items_scraped"])
	fmt.Printf("   Data: %v bytes\n", stats["bytes_downloaded"])
	fmt.Printf("   Output: %s/results.jsonl\n", aiOutput)
	if llmAvailable {
		fmt.Println("   Fields: url, title, body_text, body_text_summary, body_text_entities, body_text_sentiment")
	}
	return nil
}
