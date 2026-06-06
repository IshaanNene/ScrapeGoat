package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.APIServer = config.APIServerConfig{
		Enabled: true,
		Port:    0,
		APIKey:  "test-key-123",
		DBPath:  filepath.Join(dir, "test.db"),
		CORS:    true,
		RateRPS: 100,
	}

	srv, err := NewServer(cfg, testLogger())
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	return srv
}

// --- Job Manager Tests ---

func TestJobManager_CreateAndGet(t *testing.T) {
	dir := t.TempDir()
	jm, err := NewJobManager(filepath.Join(dir, "test.db"), testLogger())
	if err != nil {
		t.Fatalf("create job manager: %v", err)
	}
	defer jm.Close()

	job, err := jm.CreateJob(JobConfig{
		Type:     "crawl",
		URL:      "https://example.com",
		Priority: PriorityHigh,
		Config:   map[string]any{"max_depth": 3},
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	if job.ID == "" {
		t.Error("job ID is empty")
	}
	if job.Status != StatusPending {
		t.Errorf("status = %q, want pending", job.Status)
	}

	// Get.
	retrieved, err := jm.GetJob(job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if retrieved.URL != "https://example.com" {
		t.Errorf("url = %q, want https://example.com", retrieved.URL)
	}
	if retrieved.Priority != PriorityHigh {
		t.Errorf("priority = %d, want %d", retrieved.Priority, PriorityHigh)
	}
}

func TestJobManager_StatusTransitions(t *testing.T) {
	dir := t.TempDir()
	jm, err := NewJobManager(filepath.Join(dir, "test.db"), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer jm.Close()

	job, _ := jm.CreateJob(JobConfig{Type: "crawl", URL: "https://example.com"})

	// Pending -> Running.
	if err := jm.UpdateStatus(job.ID, StatusRunning); err != nil {
		t.Fatalf("update to running: %v", err)
	}
	j, _ := jm.GetJob(job.ID)
	if j.Status != StatusRunning {
		t.Errorf("status = %q, want running", j.Status)
	}
	if j.StartedAt == nil {
		t.Error("started_at should be set")
	}

	// Running -> Completed.
	jm.CompleteJob(job.ID, 42, map[string]any{"pages": 10})
	j, _ = jm.GetJob(job.ID)
	if j.Status != StatusCompleted {
		t.Errorf("status = %q, want completed", j.Status)
	}
	if j.ItemCount != 42 {
		t.Errorf("item_count = %d, want 42", j.ItemCount)
	}
	if j.EndedAt == nil {
		t.Error("ended_at should be set")
	}
}

func TestJobManager_FailJob(t *testing.T) {
	dir := t.TempDir()
	jm, err := NewJobManager(filepath.Join(dir, "test.db"), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer jm.Close()

	job, _ := jm.CreateJob(JobConfig{Type: "crawl", URL: "https://example.com"})
	jm.FailJob(job.ID, "connection refused")

	j, _ := jm.GetJob(job.ID)
	if j.Status != StatusFailed {
		t.Errorf("status = %q, want failed", j.Status)
	}
	if j.Error != "connection refused" {
		t.Errorf("error = %q, want 'connection refused'", j.Error)
	}
}

func TestJobManager_Items(t *testing.T) {
	dir := t.TempDir()
	jm, err := NewJobManager(filepath.Join(dir, "test.db"), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer jm.Close()

	job, _ := jm.CreateJob(JobConfig{Type: "crawl", URL: "https://example.com"})

	// Add items.
	for i := 0; i < 5; i++ {
		err := jm.AddJobItem(job.ID, map[string]any{
			"title": fmt.Sprintf("Item %d", i),
			"url":   fmt.Sprintf("https://example.com/%d", i),
		})
		if err != nil {
			t.Fatalf("add item %d: %v", i, err)
		}
	}

	// Get all items.
	items, err := jm.GetJobItems(job.ID, 0, 0)
	if err != nil {
		t.Fatalf("get items: %v", err)
	}
	if len(items) != 5 {
		t.Errorf("item count = %d, want 5", len(items))
	}

	// Pagination.
	page1, _ := jm.GetJobItems(job.ID, 0, 2)
	if len(page1) != 2 {
		t.Errorf("page 1 count = %d, want 2", len(page1))
	}
	page2, _ := jm.GetJobItems(job.ID, 2, 2)
	if len(page2) != 2 {
		t.Errorf("page 2 count = %d, want 2", len(page2))
	}
}

func TestJobManager_ListJobs(t *testing.T) {
	dir := t.TempDir()
	jm, err := NewJobManager(filepath.Join(dir, "test.db"), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer jm.Close()

	// Create 3 jobs with different statuses.
	j1, _ := jm.CreateJob(JobConfig{Type: "crawl", URL: "https://a.com"})
	j2, _ := jm.CreateJob(JobConfig{Type: "crawl", URL: "https://b.com"})
	_, _ = jm.CreateJob(JobConfig{Type: "batch", URL: "https://c.com"})

	jm.UpdateStatus(j1.ID, StatusRunning)
	jm.CompleteJob(j2.ID, 10, nil)

	// List all.
	all, _ := jm.ListJobs("", 50)
	if len(all) != 3 {
		t.Errorf("all jobs = %d, want 3", len(all))
	}

	// Filter by status.
	pending, _ := jm.ListJobs(StatusPending, 50)
	if len(pending) != 1 {
		t.Errorf("pending jobs = %d, want 1", len(pending))
	}
}

// --- HTTP Handler Tests ---

func TestHealth(t *testing.T) {
	srv := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	srv.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var body map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
}

func TestAuth_MissingKey(t *testing.T) {
	srv := testServer(t)

	handler := srv.withAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAuth_ValidKey(t *testing.T) {
	srv := testServer(t)

	handler := srv.withAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-API-Key", "test-key-123")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestAuth_BearerToken(t *testing.T) {
	srv := testServer(t)

	handler := srv.withAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	w := httptest.NewRecorder()
	handler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestExtract_MissingURL(t *testing.T) {
	srv := testServer(t)

	body := `{}`
	req := httptest.NewRequest(http.MethodPost, "/v1/extract", strings.NewReader(body))
	req.Header.Set("X-API-Key", "test-key-123")
	w := httptest.NewRecorder()

	srv.handleExtract(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// --- Integration Test with httptest Server ---

func TestIntegration_JobLifecycle(t *testing.T) {
	dir := t.TempDir()
	jm, err := NewJobManager(filepath.Join(dir, "test.db"), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer jm.Close()

	// Create.
	job, err := jm.CreateJob(JobConfig{
		Type:     "crawl",
		URL:      "https://httpbin.org/get",
		Priority: PriorityNormal,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Transition through lifecycle.
	jm.UpdateStatus(job.ID, StatusRunning)

	// Add items.
	for i := 0; i < 3; i++ {
		jm.AddJobItem(job.ID, map[string]any{"i": i, "url": fmt.Sprintf("https://httpbin.org/%d", i)})
	}

	// Complete.
	jm.CompleteJob(job.ID, 3, map[string]any{"pages": 3, "bytes": 15000})

	// Verify final state.
	final, _ := jm.GetJob(job.ID)
	if final.Status != StatusCompleted {
		t.Errorf("final status = %q, want completed", final.Status)
	}
	if final.ItemCount != 3 {
		t.Errorf("item count = %d, want 3", final.ItemCount)
	}

	items, _ := jm.GetJobItems(job.ID, 0, 100)
	if len(items) != 3 {
		t.Errorf("items = %d, want 3", len(items))
	}
}

func TestIntegration_APIServerWithTestHTTPServer(t *testing.T) {
	// Create a test HTTP server that serves crawlable content.
	testHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
  <h1>Hello ScrapeGoat</h1>
  <p>This is a test page for integration testing.</p>
  <a href="/page2">Page 2</a>
</body>
</html>`)
	}))
	defer testHTTP.Close()

	// Create API server.
	srv := testServer(t)

	// Test the extract handler against our test server.
	body := fmt.Sprintf(`{"url": "%s"}`, testHTTP.URL)
	req := httptest.NewRequest(http.MethodPost, "/v1/extract", strings.NewReader(body))
	req.Header.Set("X-API-Key", "test-key-123")
	w := httptest.NewRecorder()

	srv.handleExtract(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("extract status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["url"] != testHTTP.URL {
		t.Errorf("url = %v, want %s", resp["url"], testHTTP.URL)
	}

	// Verify extraction returned data.
	if resp["data"] == nil {
		t.Error("expected non-nil extraction data")
	}
}

func TestIntegration_SEOAudit(t *testing.T) {
	// Test page with SEO issues.
	testHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!DOCTYPE html><html><head></head><body><p>No title, no meta, no h1</p></body></html>`)
	}))
	defer testHTTP.Close()

	srv := testServer(t)
	body := fmt.Sprintf(`{"url": "%s"}`, testHTTP.URL)
	req := httptest.NewRequest(http.MethodPost, "/v1/seo-audit", strings.NewReader(body))
	req.Header.Set("X-API-Key", "test-key-123")
	w := httptest.NewRecorder()

	srv.handleSEOAudit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var result map[string]any
	json.Unmarshal(w.Body.Bytes(), &result)

	// Should have a low score due to missing tags.
	score, ok := result["score"].(float64)
	if !ok {
		t.Fatal("expected score field")
	}
	if score > 70 {
		t.Errorf("score = %f, expected <70 for page with missing title/h1/meta", score)
	}
}

// Ensure unused imports.
var _ = context.Background
var _ = time.Now
