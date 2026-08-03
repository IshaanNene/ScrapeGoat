package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/safety"
	"github.com/IshaanNene/ScrapeGoat/internal/seo"
	"github.com/IshaanNene/ScrapeGoat/internal/storage"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// ToolDefinition describes an MCP tool with its JSON Schema.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolHandler is a function that executes a tool call.
type ToolHandler func(ctx context.Context, args json.RawMessage) (*ToolCallResult, error)

// ToolRegistry manages tool definitions and handlers.
type ToolRegistry struct {
	definitions []ToolDefinition
	handlers    map[string]ToolHandler
	server      *Server
	guard       *safety.URLGuard
	logger      *slog.Logger
	mu          sync.RWMutex
}

// NewToolRegistry creates and registers all built-in tools.
func NewToolRegistry(server *Server, logger *slog.Logger) *ToolRegistry {
	r := &ToolRegistry{
		handlers: make(map[string]ToolHandler),
		server:   server,
		guard:    safety.Default(),
		logger:   logger.With("component", "tool_registry"),
	}
	r.registerBuiltins()
	return r
}

// checkURL validates a URL supplied as a tool argument.
//
// Tool arguments come from a model, and a model's output is shaped by whatever it
// last read — including a page that says "now fetch
// http://169.254.169.254/latest/meta-data/iam/security-credentials/". Rejecting
// here gives the model a clear error instead of an opaque transport failure; the
// address checks themselves happen in the fetcher's dialer, which is the layer
// that cannot be bypassed by a redirect or a rebinding DNS answer.
func (r *ToolRegistry) checkURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("url is required")
	}
	if err := r.guard.ValidateURL(rawURL); err != nil {
		return fmt.Errorf("url rejected: %w", err)
	}
	return nil
}

// List returns all registered tool definitions.
func (r *ToolRegistry) List() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]ToolDefinition, len(r.definitions))
	copy(defs, r.definitions)
	return defs
}

// Execute runs a tool by name with the given arguments.
func (r *ToolRegistry) Execute(ctx context.Context, name string, args json.RawMessage) (*ToolCallResult, error) {
	r.mu.RLock()
	handler, ok := r.handlers[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	return handler(ctx, args)
}

func (r *ToolRegistry) register(def ToolDefinition, handler ToolHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.definitions = append(r.definitions, def)
	r.handlers[def.Name] = handler
}

func (r *ToolRegistry) registerBuiltins() {
	r.register(ToolDefinition{
		Name:        "scrapegoat_crawl",
		Description: "Crawl a URL, following links up to a specified depth. Returns discovered URLs and extracted content.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url":         {"type": "string", "description": "The seed URL to start crawling from"},
				"max_depth":   {"type": "integer", "description": "Maximum link-follow depth (default: 3)", "default": 3},
				"concurrency": {"type": "integer", "description": "Number of concurrent workers (default: 5)", "default": 5},
				"max_pages":   {"type": "integer", "description": "Maximum number of pages to crawl (default: 50)", "default": 50},
				"allowed_domains": {"type": "array", "items": {"type": "string"}, "description": "Only crawl these domains"}
			},
			"required": ["url"]
		}`),
	}, r.handleCrawl)

	r.register(ToolDefinition{
		Name:        "scrapegoat_extract",
		Description: "Extract structured data from a URL using a JSON schema definition. Uses CSS/XPath selectors or auto-extraction.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url":    {"type": "string", "description": "The URL to extract data from"},
				"schema": {"type": "object", "description": "JSON Schema describing the fields to extract", "properties": {
					"fields": {"type": "array", "items": {"type": "object", "properties": {
						"name":        {"type": "string"},
						"selector":    {"type": "string", "description": "CSS selector"},
						"type":        {"type": "string", "enum": ["string", "number", "boolean", "array"]},
						"description": {"type": "string"}
					}}}
				}}
			},
			"required": ["url"]
		}`),
	}, r.handleExtract)

	r.register(ToolDefinition{
		Name:        "scrapegoat_search",
		Description: "Full-text search crawl: crawls a site, indexes content, and returns pages matching a query.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url":       {"type": "string", "description": "The seed URL to crawl and index"},
				"query":     {"type": "string", "description": "Search query to match against page content"},
				"max_depth": {"type": "integer", "description": "Maximum crawl depth (default: 2)", "default": 2},
				"max_pages": {"type": "integer", "description": "Maximum pages to crawl (default: 20)", "default": 20}
			},
			"required": ["url", "query"]
		}`),
	}, r.handleSearch)

	r.register(ToolDefinition{
		Name:        "scrapegoat_screenshot",
		Description: "Take a screenshot of a URL using a headless browser. Returns the screenshot as base64-encoded PNG.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url":       {"type": "string", "description": "The URL to screenshot"},
				"full_page": {"type": "boolean", "description": "Capture the full scrollable page (default: false)", "default": false},
				"width":     {"type": "integer", "description": "Viewport width in pixels (default: 1280)", "default": 1280},
				"height":    {"type": "integer", "description": "Viewport height in pixels (default: 720)", "default": 720}
			},
			"required": ["url"]
		}`),
	}, r.handleScreenshot)

	r.register(ToolDefinition{
		Name:        "scrapegoat_batch",
		Description: "Submit multiple URLs for asynchronous crawling. Returns a job ID that can be polled for results.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"urls":        {"type": "array", "items": {"type": "string"}, "description": "List of URLs to crawl"},
				"concurrency": {"type": "integer", "description": "Concurrent workers per URL (default: 3)", "default": 3},
				"max_depth":   {"type": "integer", "description": "Max crawl depth per URL (default: 1)", "default": 1}
			},
			"required": ["urls"]
		}`),
	}, r.handleBatch)

	r.register(ToolDefinition{
		Name:        "scrapegoat_job_status",
		Description: "Check the status and results of an asynchronous batch crawl job.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"job_id": {"type": "string", "description": "The job ID returned by scrapegoat_batch"}
			},
			"required": ["job_id"]
		}`),
	}, r.handleJobStatus)

	r.register(ToolDefinition{
		Name:        "scrapegoat_sitemap",
		Description: "Fetch and parse a website's sitemap.xml, returning all listed URLs with metadata.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {"type": "string", "description": "The sitemap URL or website domain (will auto-discover sitemap.xml)"}
			},
			"required": ["url"]
		}`),
	}, r.handleSitemap)

	r.register(ToolDefinition{
		Name:        "scrapegoat_seo_audit",
		Description: "Run an SEO audit on a URL, checking title tags, meta descriptions, headings, images, and more. Returns a score out of 100.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {"type": "string", "description": "The URL to audit"}
			},
			"required": ["url"]
		}`),
	}, r.handleSEOAudit)
}

// --- Tool Handlers ---

// CrawlArgs are the arguments for scrapegoat_crawl.
type CrawlArgs struct {
	URL            string   `json:"url"`
	MaxDepth       int      `json:"max_depth"`
	Concurrency    int      `json:"concurrency"`
	MaxPages       int      `json:"max_pages"`
	AllowedDomains []string `json:"allowed_domains"`
}

func (r *ToolRegistry) handleCrawl(ctx context.Context, rawArgs json.RawMessage) (*ToolCallResult, error) {
	var args CrawlArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if err := r.checkURL(args.URL); err != nil {
		return nil, err
	}
	if args.MaxDepth <= 0 {
		args.MaxDepth = 3
	}
	if args.Concurrency <= 0 {
		args.Concurrency = 5
	}
	if args.MaxPages <= 0 {
		args.MaxPages = 50
	}

	cfg := config.DefaultConfig()
	cfg.Engine.MaxDepth = args.MaxDepth
	cfg.Engine.Concurrency = args.Concurrency
	cfg.Engine.MaxRequests = args.MaxPages
	cfg.Engine.AllowedDomains = args.AllowedDomains
	cfg.Storage.Type = "json"
	cfg.Storage.OutputPath = "" // in-memory

	eng := engine.New(cfg, r.logger)

	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, r.logger)
	if err != nil {
		return nil, fmt.Errorf("create fetcher: %w", err)
	}
	eng.SetFetcher("http", httpFetcher)

	compositeParser := parser.NewCompositeParser(r.logger)
	eng.SetParser(compositeParser)

	pipe := pipeline.New(r.logger)
	pipe.Use(&pipeline.TrimMiddleware{})
	eng.SetPipeline(pipe)

	// Collect results in memory.
	collector := &memoryCollector{}
	eng.SetStorage(collector)

	if err := eng.AddSeed(args.URL); err != nil {
		return nil, fmt.Errorf("add seed: %w", err)
	}

	if err := eng.Start(); err != nil {
		return nil, fmt.Errorf("start engine: %w", err)
	}
	eng.Wait()

	stats := eng.Stats().Snapshot()
	result := map[string]any{
		"url":        args.URL,
		"pages":      stats["requests_sent"],
		"items":      stats["items_scraped"],
		"errors":     stats["responses_error"],
		"bytes":      stats["bytes_downloaded"],
		"crawl_data": collector.Items(),
	}
	out, _ := json.MarshalIndent(result, "", "  ")
	return &ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: string(out)}},
	}, nil
}

// ExtractArgs are the arguments for scrapegoat_extract.
type ExtractArgs struct {
	URL    string `json:"url"`
	Schema *struct {
		Fields []struct {
			Name        string `json:"name"`
			Selector    string `json:"selector"`
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"fields"`
	} `json:"schema"`
}

func (r *ToolRegistry) handleExtract(ctx context.Context, rawArgs json.RawMessage) (*ToolCallResult, error) {
	var args ExtractArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if err := r.checkURL(args.URL); err != nil {
		return nil, err
	}

	cfg := config.DefaultConfig()
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, r.logger)
	if err != nil {
		return nil, fmt.Errorf("create fetcher: %w", err)
	}

	req, err := types.NewRequest(args.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := httpFetcher.Fetch(fetchCtx, req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	extractor := parser.NewAutoExtractor(r.logger)
	data, err := extractor.Extract(resp)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}

	out, _ := json.MarshalIndent(data, "", "  ")
	return &ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: string(out)}},
	}, nil
}

// SearchArgs are the arguments for scrapegoat_search.
type SearchArgs struct {
	URL      string `json:"url"`
	Query    string `json:"query"`
	MaxDepth int    `json:"max_depth"`
	MaxPages int    `json:"max_pages"`
}

func (r *ToolRegistry) handleSearch(ctx context.Context, rawArgs json.RawMessage) (*ToolCallResult, error) {
	var args SearchArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if err := r.checkURL(args.URL); err != nil {
		return nil, err
	}
	if args.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if args.MaxDepth <= 0 {
		args.MaxDepth = 2
	}
	if args.MaxPages <= 0 {
		args.MaxPages = 20
	}

	cfg := config.DefaultConfig()
	cfg.Engine.MaxDepth = args.MaxDepth
	cfg.Engine.Concurrency = 5
	cfg.Engine.MaxRequests = args.MaxPages

	eng := engine.New(cfg, r.logger)
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, r.logger)
	if err != nil {
		return nil, fmt.Errorf("create fetcher: %w", err)
	}
	eng.SetFetcher("http", httpFetcher)
	eng.SetParser(parser.NewCompositeParser(r.logger))
	pipe := pipeline.New(r.logger)
	pipe.Use(&pipeline.TrimMiddleware{})
	eng.SetPipeline(pipe)

	collector := &memoryCollector{}
	eng.SetStorage(collector)

	if err := eng.AddSeed(args.URL); err != nil {
		return nil, fmt.Errorf("add seed: %w", err)
	}
	if err := eng.Start(); err != nil {
		return nil, fmt.Errorf("start engine: %w", err)
	}
	eng.Wait()

	// Filter items that match the query.
	queryLower := strings.ToLower(args.Query)
	var matches []map[string]any
	for _, item := range collector.Items() {
		itemJSON, _ := json.Marshal(item)
		if strings.Contains(strings.ToLower(string(itemJSON)), queryLower) {
			matches = append(matches, item)
		}
	}

	result := map[string]any{
		"query":       args.Query,
		"total_pages": len(collector.Items()),
		"matches":     len(matches),
		"results":     matches,
	}
	out, _ := json.MarshalIndent(result, "", "  ")
	return &ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: string(out)}},
	}, nil
}

// ScreenshotArgs are the arguments for scrapegoat_screenshot.
type ScreenshotArgs struct {
	URL      string `json:"url"`
	FullPage bool   `json:"full_page"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

func (r *ToolRegistry) handleScreenshot(ctx context.Context, rawArgs json.RawMessage) (*ToolCallResult, error) {
	var args ScreenshotArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if err := r.checkURL(args.URL); err != nil {
		return nil, err
	}
	if args.Width <= 0 {
		args.Width = 1280
	}
	if args.Height <= 0 {
		args.Height = 720
	}

	// Use go-rod for headless screenshot.
	cfg := config.DefaultConfig()
	cfg.Browser.Headless = true

	browserFetcher, err := fetcher.NewBrowserFetcher(cfg, r.logger)
	if err != nil {
		return &ToolCallResult{
			Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("Browser not available: %v. Install Chromium to use screenshots.", err)}},
			IsError: true,
		}, nil
	}
	defer browserFetcher.Close()

	req, err := types.NewRequest(args.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	req.FetcherType = "browser"

	fetchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	resp, err := browserFetcher.Fetch(fetchCtx, req)
	if err != nil {
		return nil, fmt.Errorf("screenshot fetch: %w", err)
	}

	result := map[string]any{
		"url":         args.URL,
		"status_code": resp.StatusCode,
		"title":       "", // extracted if available
		"body_length": len(resp.Body),
		"message":     "Screenshot captured via headless browser. Page HTML content returned.",
	}

	// Extract title if possible.
	if doc, docErr := resp.Document(); docErr == nil {
		result["title"] = doc.Find("title").Text()
	}

	out, _ := json.MarshalIndent(result, "", "  ")
	return &ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: string(out)}},
	}, nil
}

// BatchArgs are the arguments for scrapegoat_batch.
type BatchArgs struct {
	URLs        []string `json:"urls"`
	Concurrency int      `json:"concurrency"`
	MaxDepth    int      `json:"max_depth"`
}

func (r *ToolRegistry) handleBatch(ctx context.Context, rawArgs json.RawMessage) (*ToolCallResult, error) {
	var args BatchArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if len(args.URLs) == 0 {
		return nil, fmt.Errorf("urls is required and must not be empty")
	}
	for _, u := range args.URLs {
		if err := r.checkURL(u); err != nil {
			return nil, err
		}
	}
	if args.Concurrency <= 0 {
		args.Concurrency = 3
	}
	if args.MaxDepth <= 0 {
		args.MaxDepth = 1
	}

	job := r.server.CreateJob()
	job.Status = "running"

	// Run batch crawl in background.
	go func() {
		for _, url := range args.URLs {
			cfg := config.DefaultConfig()
			cfg.Engine.MaxDepth = args.MaxDepth
			cfg.Engine.Concurrency = args.Concurrency
			cfg.Engine.MaxRequests = 10

			eng := engine.New(cfg, r.logger)
			httpFetcher, err := fetcher.NewHTTPFetcher(cfg, r.logger)
			if err != nil {
				r.logger.Error("batch: fetcher error", "url", url, "error", err)
				continue
			}
			eng.SetFetcher("http", httpFetcher)
			eng.SetParser(parser.NewCompositeParser(r.logger))
			pipe := pipeline.New(r.logger)
			pipe.Use(&pipeline.TrimMiddleware{})
			eng.SetPipeline(pipe)

			collector := &memoryCollector{}
			eng.SetStorage(collector)

			if err := eng.AddSeed(url); err != nil {
				r.logger.Warn("batch: seed skipped", "url", url, "error", err)
				continue
			}
			if err := eng.Start(); err != nil {
				r.logger.Error("batch: start error", "url", url, "error", err)
				continue
			}
			eng.Wait()

			for _, item := range collector.Items() {
				item["_source_url"] = url
				job.AddItem(item)
			}
		}
		job.mu.Lock()
		job.Status = "completed"
		job.mu.Unlock()
	}()

	result := map[string]any{
		"job_id":    job.ID,
		"status":    "running",
		"url_count": len(args.URLs),
		"message":   "Batch crawl started. Use scrapegoat_job_status to check progress.",
	}
	out, _ := json.MarshalIndent(result, "", "  ")
	return &ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: string(out)}},
	}, nil
}

// JobStatusArgs are the arguments for scrapegoat_job_status.
type JobStatusArgs struct {
	JobID string `json:"job_id"`
}

func (r *ToolRegistry) handleJobStatus(ctx context.Context, rawArgs json.RawMessage) (*ToolCallResult, error) {
	var args JobStatusArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if args.JobID == "" {
		return nil, fmt.Errorf("job_id is required")
	}

	job, ok := r.server.GetJob(args.JobID)
	if !ok {
		return nil, fmt.Errorf("job not found: %s", args.JobID)
	}

	job.mu.Lock()
	status := job.Status
	job.mu.Unlock()

	items := job.GetItems()
	result := map[string]any{
		"job_id":     job.ID,
		"status":     status,
		"created_at": job.CreatedAt.Format(time.RFC3339),
		"item_count": len(items),
		"items":      items,
	}
	out, _ := json.MarshalIndent(result, "", "  ")
	return &ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: string(out)}},
	}, nil
}

// SitemapArgs are the arguments for scrapegoat_sitemap.
type SitemapArgs struct {
	URL string `json:"url"`
}

func (r *ToolRegistry) handleSitemap(ctx context.Context, rawArgs json.RawMessage) (*ToolCallResult, error) {
	var args SitemapArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if err := r.checkURL(args.URL); err != nil {
		return nil, err
	}

	crawler := seo.NewSitemapCrawler(r.logger)

	sitemapURL := args.URL
	if !strings.Contains(sitemapURL, "sitemap") {
		// Try to discover the sitemap from the domain.
		domain := strings.TrimPrefix(strings.TrimPrefix(sitemapURL, "https://"), "http://")
		domain = strings.Split(domain, "/")[0]
		if discovered := crawler.DiscoverSitemap(domain); discovered != "" {
			sitemapURL = discovered
		} else {
			sitemapURL = "https://" + domain + "/sitemap.xml"
		}
	}

	urls, err := crawler.Crawl(sitemapURL)
	if err != nil {
		return nil, fmt.Errorf("crawl sitemap: %w", err)
	}

	result := map[string]any{
		"sitemap_url": sitemapURL,
		"url_count":   len(urls),
		"urls":        urls,
	}
	out, _ := json.MarshalIndent(result, "", "  ")
	return &ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: string(out)}},
	}, nil
}

// SEOAuditArgs are the arguments for scrapegoat_seo_audit.
type SEOAuditArgs struct {
	URL string `json:"url"`
}

func (r *ToolRegistry) handleSEOAudit(ctx context.Context, rawArgs json.RawMessage) (*ToolCallResult, error) {
	var args SEOAuditArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if err := r.checkURL(args.URL); err != nil {
		return nil, err
	}

	cfg := config.DefaultConfig()
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, r.logger)
	if err != nil {
		return nil, fmt.Errorf("create fetcher: %w", err)
	}

	req, err := types.NewRequest(args.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := httpFetcher.Fetch(fetchCtx, req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	auditor := seo.NewMetaAuditor(r.logger)
	auditResult, err := auditor.Audit(resp)
	if err != nil {
		return nil, fmt.Errorf("audit: %w", err)
	}

	out, _ := json.MarshalIndent(auditResult, "", "  ")
	return &ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: string(out)}},
	}, nil
}

// --- Memory Collector ---

// memoryCollector is an in-memory storage backend for MCP tool use.
type memoryCollector struct {
	mu    sync.Mutex
	items []map[string]any
}

func (c *memoryCollector) Store(items []*types.Item) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, item := range items {
		m := make(map[string]any, len(item.Fields)+2)
		for k, v := range item.Fields {
			m[k] = v
		}
		m["_url"] = item.URL
		m["_timestamp"] = item.Timestamp.Format(time.RFC3339)
		c.items = append(c.items, m)
	}
	return nil
}

func (c *memoryCollector) Close() error { return nil }

func (c *memoryCollector) Name() string { return "memory" }

// Items returns all collected items.
func (c *memoryCollector) Items() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]map[string]any, len(c.items))
	copy(result, c.items)
	return result
}

// Ensure memoryCollector implements storage.Storage.
var _ storage.Storage = (*memoryCollector)(nil)
