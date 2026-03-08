package fetcher

import (
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

func testConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Engine.UserAgents = []string{"Bot/1.0", "Bot/2.0", "Bot/3.0"}
	cfg.Fetcher.MaxBodySize = 1024 * 1024
	return cfg
}

// --- User-Agent Rotation ---

func TestNextUserAgent(t *testing.T) {
	cfg := testConfig()
	logger := testLogger(t)
	f, err := NewHTTPFetcher(cfg, logger)
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer f.Close()

	seen := make(map[string]bool)
	for i := 0; i < 6; i++ {
		ua := f.nextUserAgent()
		seen[ua] = true
	}

	// All 3 user agents should have been rotated through
	if len(seen) != 3 {
		t.Errorf("expected 3 unique user agents, got %d: %v", len(seen), seen)
	}
}

func TestNextUserAgentEmpty(t *testing.T) {
	cfg := testConfig()
	cfg.Engine.UserAgents = nil
	logger := testLogger(t)
	f, err := NewHTTPFetcher(cfg, logger)
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer f.Close()

	ua := f.nextUserAgent()
	if !strings.HasPrefix(ua, "ScrapeGoat/") {
		t.Errorf("expected default user agent starting with 'ScrapeGoat/', got %q", ua)
	}
}

// --- Retry-After Parsing ---

func TestParseRetryAfterSeconds(t *testing.T) {
	d := parseRetryAfter("30")
	if d != 30*time.Second {
		t.Errorf("expected 30s, got %v", d)
	}
}

func TestParseRetryAfterCapped(t *testing.T) {
	d := parseRetryAfter("999")
	if d != 120*time.Second {
		t.Errorf("expected 120s (capped), got %v", d)
	}
}

func TestParseRetryAfterEmpty(t *testing.T) {
	d := parseRetryAfter("")
	if d != 5*time.Second {
		t.Errorf("expected 5s default, got %v", d)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	future := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	d := parseRetryAfter(future)
	// Should be roughly 10 seconds (allow 2s tolerance for test execution time)
	if d < 8*time.Second || d > 12*time.Second {
		t.Errorf("expected ~10s, got %v", d)
	}
}

// --- Retryable Error Detection ---

func TestIsRetryableErrorNil(t *testing.T) {
	if isRetryableError(nil) {
		t.Error("nil error should not be retryable")
	}
}

func TestIsRetryableErrorContextCanceled(t *testing.T) {
	if isRetryableError(context.Canceled) {
		t.Error("context.Canceled should not be retryable")
	}
}

func TestIsRetryableErrorUnexpectedEOF(t *testing.T) {
	if !isRetryableError(io.ErrUnexpectedEOF) {
		t.Error("unexpected EOF should be retryable")
	}
}

// --- RandomDelay ---

func TestRandomDelay(t *testing.T) {
	base := time.Second
	min := base - time.Duration(float64(base)*0.25)
	max := base + time.Duration(float64(base)*0.25)

	for i := 0; i < 100; i++ {
		d := RandomDelay(base)
		if d < min || d > max {
			t.Errorf("RandomDelay(%v) = %v, want [%v, %v]", base, d, min, max)
		}
	}
}

// --- Full Fetch Integration (httptest) ---

func TestFetchSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
		w.Write([]byte("<html><body>Hello</body></html>"))
	}))
	defer server.Close()

	cfg := testConfig()
	logger := testLogger(t)
	f, err := NewHTTPFetcher(cfg, logger)
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer f.Close()

	req, _ := types.NewRequest(server.URL)
	resp, err := f.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("fetch error: %v", err)
	}

	if resp.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	body := string(resp.Body)
	if !strings.Contains(body, "Hello") {
		t.Errorf("expected body to contain 'Hello', got %q", body)
	}
	if resp.FetchDuration <= 0 {
		t.Error("expected positive fetch duration")
	}
}

func TestFetch429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(429)
		w.Write([]byte("rate limited"))
	}))
	defer server.Close()

	cfg := testConfig()
	logger := testLogger(t)
	f, err := NewHTTPFetcher(cfg, logger)
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer f.Close()

	req, _ := types.NewRequest(server.URL)
	_, err = f.Fetch(context.Background(), req)
	if err == nil {
		t.Fatal("expected error on 429 response")
	}

	fetchErr, ok := err.(*types.FetchError)
	if !ok {
		t.Fatalf("expected *FetchError, got %T", err)
	}
	if !fetchErr.Retryable {
		t.Error("429 error should be retryable")
	}
	if fetchErr.RetryAfter != 5*time.Second {
		t.Errorf("expected RetryAfter 5s, got %v", fetchErr.RetryAfter)
	}
}

func TestFetch5xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte("service unavailable"))
	}))
	defer server.Close()

	cfg := testConfig()
	logger := testLogger(t)
	f, err := NewHTTPFetcher(cfg, logger)
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer f.Close()

	req, _ := types.NewRequest(server.URL)
	_, err = f.Fetch(context.Background(), req)
	if err == nil {
		t.Fatal("expected error on 503 response")
	}

	fetchErr, ok := err.(*types.FetchError)
	if !ok {
		t.Fatalf("expected *FetchError, got %T", err)
	}
	if !fetchErr.Retryable {
		t.Error("5xx error should be retryable")
	}
}

func TestFetchGzipDecompression(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/html")
		gz := gzip.NewWriter(w)
		gz.Write([]byte("<html><body>Compressed</body></html>"))
		gz.Close()
	}))
	defer server.Close()

	cfg := testConfig()
	logger := testLogger(t)
	f, err := NewHTTPFetcher(cfg, logger)
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer f.Close()

	req, _ := types.NewRequest(server.URL)
	resp, err := f.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("fetch error: %v", err)
	}

	body := string(resp.Body)
	if !strings.Contains(body, "Compressed") {
		t.Errorf("expected decompressed body containing 'Compressed', got %q", body)
	}
}

func TestFetchCustomHeaders(t *testing.T) {
	var receivedUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header.Get("User-Agent")
		w.WriteHeader(200)
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.Engine.UserAgents = []string{"CustomBot/1.0"}
	logger := testLogger(t)
	f, err := NewHTTPFetcher(cfg, logger)
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	defer f.Close()

	req, _ := types.NewRequest(server.URL)
	_, err = f.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("fetch error: %v", err)
	}

	if receivedUA != "CustomBot/1.0" {
		t.Errorf("expected User-Agent 'CustomBot/1.0', got %q", receivedUA)
	}
}

// --- Helpers ---

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
