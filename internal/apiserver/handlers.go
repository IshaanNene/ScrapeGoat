package apiserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/sitemap"
	"github.com/IshaanNene/ScrapeGoat/internal/storage"
	"github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat/types"
)

// --- Request Types ---

// CrawlRequest is the payload for POST /v1/crawl.
type CrawlRequest struct {
	URL            string   `json:"url"`
	MaxDepth       int      `json:"max_depth"`
	Concurrency    int      `json:"concurrency"`
	MaxPages       int      `json:"max_pages"`
	AllowedDomains []string `json:"allowed_domains"`
	Priority       string   `json:"priority"` // low, normal, high
	Async          bool     `json:"async"`
}

// ExtractRequest is the payload for POST /v1/extract.
type ExtractRequest struct {
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

// BatchRequest is the payload for POST /v1/batch.
type BatchRequest struct {
	URLs        []string `json:"urls"`
	Concurrency int      `json:"concurrency"`
	MaxDepth    int      `json:"max_depth"`
}

// ScreenshotRequest is the payload for POST /v1/screenshot.
type ScreenshotRequest struct {
	URL      string `json:"url"`
	FullPage bool   `json:"full_page"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// --- Handlers ---

func (s *Server) handleCrawl(w http.ResponseWriter, r *http.Request) {
	var req CrawlRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.URL == "" {
		s.jsonError(w, http.StatusBadRequest, "url is required")
		return
	}
	if req.MaxDepth <= 0 {
		req.MaxDepth = 3
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 5
	}
	if req.MaxPages <= 0 {
		req.MaxPages = 50
	}

	priority := PriorityNormal
	switch req.Priority {
	case "high":
		priority = PriorityHigh
	case "low":
		priority = PriorityLow
	}

	job, err := s.jobManager.CreateJob(JobConfig{
		Type:     "crawl",
		URL:      req.URL,
		Priority: priority,
		Config: map[string]any{
			"max_depth":       req.MaxDepth,
			"concurrency":     req.Concurrency,
			"max_pages":       req.MaxPages,
			"allowed_domains": req.AllowedDomains,
		},
	})
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to create job")
		return
	}

	// Run the crawl.
	go s.executeCrawl(job, req)

	if req.Async {
		s.jsonResponse(w, http.StatusAccepted, map[string]any{
			"job_id":  job.ID,
			"status":  job.Status,
			"message": "Crawl job started. Poll GET /v1/jobs/" + job.ID + " for status.",
		})
		return
	}

	// Synchronous: wait for completion (with timeout).
	deadline := time.After(5 * time.Minute)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			s.jsonResponse(w, http.StatusAccepted, map[string]any{
				"job_id":  job.ID,
				"status":  "running",
				"message": "Crawl is still running. Poll GET /v1/jobs/" + job.ID,
			})
			return
		case <-ticker.C:
			current, _ := s.jobManager.GetJob(job.ID)
			if current != nil && (current.Status == StatusCompleted || current.Status == StatusFailed) {
				items, _ := s.jobManager.GetJobItems(job.ID, 0, 0)
				s.jsonResponse(w, http.StatusOK, map[string]any{
					"job_id":     job.ID,
					"status":     current.Status,
					"item_count": current.ItemCount,
					"items":      items,
					"stats":      current.Stats,
				})
				return
			}
		}
	}
}

func (s *Server) executeCrawl(job *Job, req CrawlRequest) {
	if err := s.jobManager.UpdateStatus(job.ID, StatusRunning); err != nil {
		s.logger.Warn("could not mark job running", "job", job.ID, "error", err)
	}
	s.BroadcastJobEvent(job.ID, map[string]any{"event": "started", "job_id": job.ID})

	cfg := s.requestConfig()
	cfg.Engine.MaxDepth = req.MaxDepth
	cfg.Engine.Concurrency = req.Concurrency
	cfg.Engine.MaxRequests = req.MaxPages
	cfg.Engine.AllowedDomains = req.AllowedDomains

	eng := engine.New(cfg, s.logger)
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, s.logger)
	if err != nil {
		s.jobManager.FailJob(job.ID, fmt.Sprintf("fetcher init: %v", err))
		return
	}
	eng.SetFetcher("http", httpFetcher)
	eng.SetParser(parser.NewCompositeParser(s.logger))

	pipe := pipeline.New(s.logger)
	pipe.Use(&pipeline.TrimMiddleware{})
	eng.SetPipeline(pipe)

	collector := newStreamingCollector(s, job.ID)
	eng.SetStorage(collector)

	if err := eng.AddSeed(req.URL); err != nil {
		s.jobManager.FailJob(job.ID, fmt.Sprintf("seed: %v", err))
		return
	}

	if err := eng.Start(); err != nil {
		s.jobManager.FailJob(job.ID, fmt.Sprintf("start: %v", err))
		return
	}
	eng.Wait()

	stats := eng.StatsSnapshot()
	s.jobManager.CompleteJob(job.ID, collector.count, stats)
	s.BroadcastJobEvent(job.ID, map[string]any{
		"event":      "completed",
		"job_id":     job.ID,
		"item_count": collector.count,
	})
}

func (s *Server) handleExtract(w http.ResponseWriter, r *http.Request) {
	var req ExtractRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.URL == "" {
		s.jsonError(w, http.StatusBadRequest, "url is required")
		return
	}

	cfg := s.requestConfig()
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, s.logger)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, "fetcher init failed")
		return
	}

	fetchReq, err := types.NewRequest(req.URL)
	if err != nil {
		s.jsonError(w, http.StatusBadRequest, "invalid URL")
		return
	}

	resp, err := httpFetcher.Fetch(r.Context(), fetchReq)
	if err != nil {
		s.jsonError(w, http.StatusBadGateway, fmt.Sprintf("fetch error: %v", err))
		return
	}

	extractor := parser.NewAutoExtractor(s.logger)
	data, err := extractor.Extract(resp)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, fmt.Sprintf("extraction error: %v", err))
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"url":  req.URL,
		"data": data,
	})
}

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	var req BatchRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.URLs) == 0 {
		s.jsonError(w, http.StatusBadRequest, "urls is required")
		return
	}
	if req.Concurrency <= 0 {
		req.Concurrency = 3
	}
	if req.MaxDepth <= 0 {
		req.MaxDepth = 1
	}

	job, err := s.jobManager.CreateJob(JobConfig{
		Type:     "batch",
		URL:      req.URLs[0],
		Priority: PriorityNormal,
		Config: map[string]any{
			"urls":        req.URLs,
			"concurrency": req.Concurrency,
			"max_depth":   req.MaxDepth,
		},
	})
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to create batch job")
		return
	}

	go s.executeBatch(job, req)

	s.jsonResponse(w, http.StatusAccepted, map[string]any{
		"job_id":    job.ID,
		"status":    "pending",
		"url_count": len(req.URLs),
	})
}

func (s *Server) executeBatch(job *Job, req BatchRequest) {
	if err := s.jobManager.UpdateStatus(job.ID, StatusRunning); err != nil {
		s.logger.Warn("could not mark job running", "job", job.ID, "error", err)
	}

	totalItems := 0
	for _, url := range req.URLs {
		cfg := s.requestConfig()
		cfg.Engine.MaxDepth = req.MaxDepth
		cfg.Engine.Concurrency = req.Concurrency
		cfg.Engine.MaxRequests = 10

		eng := engine.New(cfg, s.logger)
		httpFetcher, err := fetcher.NewHTTPFetcher(cfg, s.logger)
		if err != nil {
			s.logger.Error("batch: fetcher error", "url", url, "error", err)
			continue
		}
		eng.SetFetcher("http", httpFetcher)
		eng.SetParser(parser.NewCompositeParser(s.logger))

		pipe := pipeline.New(s.logger)
		pipe.Use(&pipeline.TrimMiddleware{})
		eng.SetPipeline(pipe)

		collector := newStreamingCollector(s, job.ID)
		eng.SetStorage(collector)

		if err := eng.AddSeed(url); err != nil {
			continue
		}
		if err := eng.Start(); err != nil {
			continue
		}
		eng.Wait()
		totalItems += collector.count
	}

	s.jobManager.CompleteJob(job.ID, totalItems, nil)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	job, err := s.jobManager.GetJob(jobID)
	if err != nil {
		s.jsonError(w, http.StatusNotFound, "job not found")
		return
	}
	s.jsonResponse(w, http.StatusOK, job)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if err := s.jobManager.UpdateStatus(jobID, StatusCancelled); err != nil {
		s.jsonError(w, http.StatusNotFound, "job not found")
		return
	}
	s.jsonResponse(w, http.StatusOK, map[string]any{"job_id": jobID, "status": "cancelled"})
}

func (s *Server) handleGetJobItems(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 100
	}

	items, err := s.jobManager.GetJobItems(jobID, offset, limit)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to fetch items")
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"job_id": jobID,
		"offset": offset,
		"limit":  limit,
		"count":  len(items),
		"items":  items,
	})
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}

	jobs, err := s.jobManager.ListJobs(status, limit)
	if err != nil {
		s.jsonError(w, http.StatusInternalServerError, "failed to list jobs")
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"count": len(jobs),
		"jobs":  jobs,
	})
}

func (s *Server) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	var req ScreenshotRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.URL == "" {
		s.jsonError(w, http.StatusBadRequest, "url is required")
		return
	}
	// This endpoint takes a URL from an API client and renders it in a browser, so
	// it is an SSRF primitive unless the target is checked. The scheme/shape check
	// happens here; the address checks happen on the dial, inside the guarded
	// egress proxy the browser fetcher runs.
	if err := s.guard.ValidateURL(req.URL); err != nil {
		s.jsonError(w, http.StatusBadRequest, fmt.Sprintf("url rejected: %v", err))
		return
	}

	cfg := s.requestConfig()
	cfg.Browser.Headless = true

	browserFetcher, err := fetcher.NewBrowserFetcher(cfg, s.guard, s.logger)
	if err != nil {
		s.jsonError(w, http.StatusServiceUnavailable, "browser not available")
		return
	}
	defer browserFetcher.Close()

	fetchReq, _ := types.NewRequest(req.URL)
	fetchReq.FetcherType = "browser"

	resp, err := browserFetcher.Fetch(r.Context(), fetchReq)
	if err != nil {
		s.jsonError(w, http.StatusBadGateway, fmt.Sprintf("screenshot failed: %v", err))
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"url":         req.URL,
		"status_code": resp.StatusCode,
		"body_length": len(resp.Body),
	})
}

func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := s.decodeJSON(r, &req); err != nil {
		s.jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.URL == "" {
		s.jsonError(w, http.StatusBadRequest, "url is required")
		return
	}

	crawler := sitemap.New(s.logger)
	urls, err := crawler.Crawl(req.URL)
	if err != nil {
		s.jsonError(w, http.StatusBadGateway, fmt.Sprintf("sitemap crawl failed: %v", err))
		return
	}

	s.jsonResponse(w, http.StatusOK, map[string]any{
		"url":       req.URL,
		"url_count": len(urls),
		"urls":      urls,
	})
}

// --- Helpers ---

func (s *Server) decodeJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	defer r.Body.Close()
	return json.Unmarshal(body, v)
}

// --- Streaming Collector ---

type streamingCollector struct {
	server *Server
	jobID  string
	count  int
}

func newStreamingCollector(s *Server, jobID string) *streamingCollector {
	return &streamingCollector{server: s, jobID: jobID}
}

func (c *streamingCollector) Store(items []*types.Item) error {
	for _, item := range items {
		m := make(map[string]any, len(item.Fields)+2)
		for k, v := range item.Fields {
			m[k] = v
		}
		m["_url"] = item.URL
		m["_timestamp"] = item.Timestamp.Format(time.RFC3339)

		// Persist to job store.
		_ = c.server.jobManager.AddJobItem(c.jobID, m)

		// Broadcast via WebSocket.
		c.server.BroadcastJobEvent(c.jobID, map[string]any{
			"event": "item",
			"data":  m,
		})
		c.count++
	}
	return nil
}

func (c *streamingCollector) Close() error { return nil }
func (c *streamingCollector) Name() string { return "api_streaming" }

var _ storage.Storage = (*streamingCollector)(nil)
