package engine

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/storage"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

var coverLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

// --- Dedup coverage ---
func TestDedupReset(t *testing.T) {
	d := NewDeduplicator(1000)
	d.MarkSeen("https://example.com/a")
	d.MarkSeen("https://example.com/b")
	if !d.IsSeen("https://example.com/a") {
		t.Fatal("should be seen")
	}
	d.Reset()
	if d.IsSeen("https://example.com/a") {
		t.Error("after Reset, URL should not be seen")
	}
}

func TestCanonicalizeURLCoverage(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://example.com/path?b=2&a=1#frag", "https://example.com/path?a=1&b=2"},
		{"https://example.com/path/", "https://example.com/path"},
		{"HTTPS://EXAMPLE.COM/Path", "https://example.com/Path"},
	}
	for _, tt := range tests {
		got := CanonicalizeURL(tt.in)
		if got != tt.want {
			t.Errorf("CanonicalizeURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// --- Frontier coverage: Pop (with context), IsEmpty, Drain, RestoreAll ---
func TestFrontierPopWithContext(t *testing.T) {
	f := NewFrontier()
	if !f.IsEmpty() {
		t.Error("new frontier should be empty")
	}
	req, _ := types.NewRequest("https://example.com")
	f.Push(req)
	if f.IsEmpty() {
		t.Error("should not be empty after push")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	popped := f.Pop(ctx)
	if popped == nil {
		t.Fatal("Pop returned nil")
	}
	if !f.IsEmpty() {
		t.Error("should be empty after popping only item")
	}
}

func TestFrontierDrainAndRestoreAll(t *testing.T) {
	f := NewFrontier()
	urls := []string{"https://example.com/1", "https://example.com/2", "https://example.com/3"}
	for _, u := range urls {
		req, _ := types.NewRequest(u)
		f.Push(req)
	}

	drained := f.Drain()
	if len(drained) != 3 {
		t.Errorf("Drain returned %d, want 3", len(drained))
	}
	if !f.IsEmpty() {
		t.Error("should be empty after drain")
	}

	f.RestoreAll(drained)
	if f.Len() != 3 {
		t.Errorf("after RestoreAll, Len=%d, want 3", f.Len())
	}
}

// --- Engine setters: SetPipeline, SetStorage, Stats, GetState, ResultsChan ---
func TestEngineSettersAndGetters(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Engine.RespectRobotsTxt = false
	eng := New(cfg, coverLogger)

	// SetPipeline
	pipe := pipeline.New(coverLogger)
	eng.SetPipeline(pipe)

	// SetStorage
	tmpDir := t.TempDir()
	store, _ := storage.NewFileStorage("jsonl", tmpDir, coverLogger)
	eng.SetStorage(store)

	// Stats
	stats := eng.Stats()
	if stats == nil {
		t.Error("Stats returned nil")
	}

	// GetState
	state := eng.GetState()
	t.Logf("state type: %T, value: %s", state, state)

	// ResultsChan
	ch := eng.ResultsChan()
	_ = ch // just verify it doesn't panic
}

// --- State.String() ---
func TestStateString(t *testing.T) {
	tests := []struct {
		state State
		want  string
	}{
		{StateIdle, "idle"},
		{StateRunning, "running"},
		{StatePaused, "paused"},
		{StateStopping, "stopping"},
		{StateStopped, "stopped"},
	}
	for _, tt := range tests {
		got := tt.state.String()
		if got != tt.want {
			t.Errorf("State(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

// --- Engine Pause/Resume (on idle engine) ---
func TestEnginePauseResumeBasic(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Engine.RespectRobotsTxt = false
	eng := New(cfg, coverLogger)
	eng.Pause()
	eng.Resume()
	t.Log("PASS: Pause+Resume on idle engine did not panic")
}

// --- Engine isDomainAllowed ---
func TestIsDomainAllowed(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Engine.AllowedDomains = []string{"example.com", "test.com"}
	cfg.Engine.RespectRobotsTxt = false
	eng := New(cfg, coverLogger)

	tests := []struct {
		domain string
		want   bool
	}{
		{"example.com", true},
		{"test.com", true},
		{"evil.com", false},
	}
	for _, tt := range tests {
		got := eng.isDomainAllowed(tt.domain)
		if got != tt.want {
			t.Errorf("isDomainAllowed(%q) = %v, want %v", tt.domain, got, tt.want)
		}
	}
}

// --- Full pipeline + storage exercising processItems + storeResults ---
func TestEngineProcessItemsAndStore(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h1>Test Page</h1></body></html>`)
	}))
	defer ts.Close()

	tmpDir := t.TempDir()

	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = 2
	cfg.Engine.MaxDepth = 0
	cfg.Engine.MaxRequests = 5
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 5 * time.Second
	cfg.Engine.RespectRobotsTxt = false

	eng := New(cfg, coverLogger)
	httpFetcher, _ := fetcher.NewHTTPFetcher(cfg, coverLogger)
	eng.SetFetcher("http", httpFetcher)

	pipe := pipeline.New(coverLogger)
	pipe.Use(&pipeline.TrimMiddleware{})
	eng.SetPipeline(pipe)

	store, _ := storage.NewFileStorage("jsonl", tmpDir, coverLogger)
	eng.SetStorage(store)

	var items atomic.Int64
	eng.OnResponse("test", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		item := types.NewItem(resp.Request.URLString())
		item.Set("title", "Test Page")
		items.Add(1)
		return []*types.Item{item}, nil, nil
	})

	for i := 0; i < 5; i++ {
		_ = eng.AddSeed(fmt.Sprintf("%s/page/%d", ts.URL, i))
	}

	_ = eng.Start()
	eng.Wait()

	if items.Load() == 0 {
		t.Error("expected items to be processed through pipeline and stored")
	}
	t.Logf("PASS: processed %d items through pipeline+storage", items.Load())
}
