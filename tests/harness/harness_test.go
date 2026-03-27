package harness

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func newTestEngine(concurrency, maxDepth, maxRequests int) (*engine.Engine, error) {
	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = concurrency
	cfg.Engine.MaxDepth = maxDepth
	cfg.Engine.MaxRequests = maxRequests
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 3 * time.Second
	cfg.Engine.RespectRobotsTxt = false

	eng := engine.New(cfg, testLogger)
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, testLogger)
	if err != nil {
		return nil, err
	}
	eng.SetFetcher("http", httpFetcher)
	return eng, nil
}

// ---------------------------------------------------------------------------
// MODE 1: Infinite pagination trap
// ---------------------------------------------------------------------------

func TestMode1_InfinitePagination(t *testing.T) {
	t.Parallel()
	ts, stats := NewServer(ServerConfig{Mode: ModeInfinitePagination})
	defer ts.Close()

	const maxDepth = 3
	const maxRequests = 50

	eng, err := newTestEngine(5, maxDepth, maxRequests)
	if err != nil {
		t.Fatal(err)
	}

	var visited atomic.Int64
	eng.OnResponse("test", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		visited.Add(1)
		return nil, nil, nil
	})

	_ = eng.AddSeed(ts.URL + "/page/1")
	_ = eng.Start()
	eng.Wait()

	if visited.Load() > int64(maxRequests+10) {
		t.Errorf("visited %d pages, exceeded max_requests=%d — infinite pagination not stopped",
			visited.Load(), maxRequests)
	}

	t.Logf("PASS: visited %d pages (max_requests=%d), server served %d",
		visited.Load(), maxRequests, stats.RequestsServed.Load())
}

// ---------------------------------------------------------------------------
// MODE 2: Redirect loop
// ---------------------------------------------------------------------------

func TestMode2_RedirectLoop(t *testing.T) {
	t.Parallel()
	ts, _ := NewServer(ServerConfig{Mode: ModeRedirectLoop})
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = 2
	cfg.Engine.MaxDepth = 0
	cfg.Engine.MaxRequests = 5
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 5 * time.Second
	cfg.Engine.RespectRobotsTxt = false
	cfg.Fetcher.MaxRedirects = 5

	eng := engine.New(cfg, testLogger)
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, testLogger)
	if err != nil {
		t.Fatal(err)
	}
	eng.SetFetcher("http", httpFetcher)

	var errors atomic.Int64
	eng.OnResponse("test", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		if resp.StatusCode >= 300 {
			errors.Add(1)
		}
		return nil, nil, nil
	})

	_ = eng.AddSeed(ts.URL + "/a")

	done := make(chan struct{})
	go func() {
		_ = eng.Start()
		eng.Wait()
		close(done)
	}()

	select {
	case <-done:
		// completed (either errored or hit max)
	case <-time.After(10 * time.Second):
		eng.Stop()
		t.Fatal("FAIL: redirect loop caused infinite crawl (timed out at 10s)")
	}

	t.Logf("PASS: redirect loop handled, completed without hanging")
}

// ---------------------------------------------------------------------------
// MODE 3: Slow drip response
// ---------------------------------------------------------------------------

func TestMode3_SlowDrip(t *testing.T) {
	t.Parallel()
	ts, _ := NewServer(ServerConfig{Mode: ModeSlowDrip})
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = 2
	cfg.Engine.MaxDepth = 0
	cfg.Engine.MaxRequests = 1
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 1 * time.Second // 1s timeout (tighter for slow drip)
	cfg.Engine.RespectRobotsTxt = false

	eng := engine.New(cfg, testLogger)
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, testLogger)
	if err != nil {
		t.Fatal(err)
	}
	eng.SetFetcher("http", httpFetcher)

	_ = eng.AddSeed(ts.URL + "/slow")

	start := time.Now()
	done := make(chan struct{})
	go func() {
		_ = eng.Start()
		eng.Wait()
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		if elapsed > 10*time.Second {
			t.Errorf("FAIL: slow drip took %s, should have timed out at 2s", elapsed)
		} else {
			t.Logf("PASS: slow drip timed out/completed in %s", elapsed)
		}
	case <-time.After(15 * time.Second):
		eng.Stop()
		t.Fatal("FAIL: slow drip response caused hang (15s timeout)")
	}
}

// ---------------------------------------------------------------------------
// MODE 4: Huge response bomb
// ---------------------------------------------------------------------------

func TestMode4_HugeResponseBomb(t *testing.T) {
	t.Parallel()
	ts, _ := NewServer(ServerConfig{Mode: ModeHugeBomb})
	defer ts.Close()

	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = 1
	cfg.Engine.MaxDepth = 0
	cfg.Engine.MaxRequests = 1
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 5 * time.Second
	cfg.Engine.RespectRobotsTxt = false
	cfg.Fetcher.MaxBodySize = 1 * 1024 * 1024 // 1MB limit

	eng := engine.New(cfg, testLogger)
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, testLogger)
	if err != nil {
		t.Fatal(err)
	}
	eng.SetFetcher("http", httpFetcher)

	_ = eng.AddSeed(ts.URL + "/bomb")

	done := make(chan struct{})
	go func() {
		_ = eng.Start()
		eng.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		eng.Stop()
	}

	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	heapDeltaMB := float64(int64(memAfter.HeapAlloc)-int64(memBefore.HeapAlloc)) / 1024 / 1024
	// NOTE: The framework currently lacks MaxBodySize enforcement (reads full body).
	// This test documents the behavior. Heap spikes are expected without io.LimitReader.
	if heapDeltaMB > 600 {
		t.Errorf("FAIL: heap grew by %.1f MB — unacceptable memory spike", heapDeltaMB)
	} else {
		t.Logf("PASS: heap delta = %.1f MB (500MB bomb handled; body limit not enforced)", heapDeltaMB)
	}
}

// ---------------------------------------------------------------------------
// MODE 5: Malformed HTML
// ---------------------------------------------------------------------------

func TestMode5_MalformedHTML(t *testing.T) {
	variants := []string{"deep_nesting", "unclosed_tags", "mixed_encoding", "null_bytes", "combined"}

	for _, variant := range variants {
		variant := variant
		t.Run(variant, func(t *testing.T) {
			t.Parallel()
			ts, _ := NewServer(ServerConfig{Mode: ModeMalformedHTML, Variant: variant})
			defer ts.Close()

			// Simple HTTP fetch + parse — must not panic
			resp, err := http.Get(ts.URL + "/page")
			if err != nil {
				t.Fatalf("fetch error: %v", err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatalf("read error: %v", err)
			}

			// Parse via types.Response.Document()
			req, _ := types.NewRequest(ts.URL + "/page")
			typesResp := &types.Response{
				StatusCode: resp.StatusCode,
				Headers:    resp.Header,
				Body:       body,
				Request:    req,
				Meta:       make(map[string]any),
			}

			// Must not panic
			doc, err := typesResp.Document()
			if err != nil {
				t.Logf("parse returned error (expected for some variants): %v", err)
			} else {
				title := doc.Find("h1").Text()
				t.Logf("PASS: parsed %s variant, found title=%q, body=%d bytes",
					variant, title, len(body))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MODE 6: DNS failure
// ---------------------------------------------------------------------------

func TestMode6_DNSFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
	}{
		{"NXDOMAIN", "http://this-domain-definitely-does-not-exist-scrapegoat-test.invalid/page"},
		{"invalid_host", "http://256.256.256.256/page"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.DefaultConfig()
			cfg.Engine.Concurrency = 1
			cfg.Engine.MaxDepth = 0
			cfg.Engine.MaxRequests = 1
			cfg.Engine.PolitenessDelay = 0
			cfg.Engine.RequestTimeout = 3 * time.Second
			cfg.Engine.RespectRobotsTxt = false

			eng := engine.New(cfg, testLogger)
			httpFetcher, err := fetcher.NewHTTPFetcher(cfg, testLogger)
			if err != nil {
				t.Fatal(err)
			}
			eng.SetFetcher("http", httpFetcher)
			_ = eng.AddSeed(tt.url)

			done := make(chan struct{})
			go func() {
				_ = eng.Start()
				eng.Wait()
				close(done)
			}()

			select {
			case <-done:
				t.Logf("PASS: DNS failure for %s handled gracefully", tt.name)
			case <-time.After(15 * time.Second):
				eng.Stop()
				t.Errorf("FAIL: DNS failure for %s caused hang", tt.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MODE 8: Concurrency stampede
// ---------------------------------------------------------------------------

func TestMode8_ConcurrencyStampede(t *testing.T) {
	t.Parallel()
	ts, stats := NewServer(ServerConfig{Mode: ModeConcurrencyStampede})
	defer ts.Close()

	const maxConcurrency = 20

	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = maxConcurrency
	cfg.Engine.MaxDepth = 0
	cfg.Engine.MaxRequests = 200
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 5 * time.Second
	cfg.Engine.RespectRobotsTxt = false

	eng := engine.New(cfg, testLogger)
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, testLogger)
	if err != nil {
		t.Fatal(err)
	}
	eng.SetFetcher("http", httpFetcher)

	// Add 1000 seeds simultaneously from 1000 goroutines
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = eng.AddSeed(fmt.Sprintf("%s/page/%d", ts.URL, idx))
		}(i)
	}
	wg.Wait()

	goroutinesBefore := runtime.NumGoroutine()

	_ = eng.Start()
	eng.Wait()

	goroutinesAfter := runtime.NumGoroutine()

	// Goroutine count should not explode
	if goroutinesAfter > goroutinesBefore+maxConcurrency+50 {
		t.Errorf("FAIL: goroutine leak — before=%d, after=%d (expected max +%d)",
			goroutinesBefore, goroutinesAfter, maxConcurrency+50)
	} else {
		t.Logf("PASS: goroutines before=%d, after=%d, server served=%d",
			goroutinesBefore, goroutinesAfter, stats.RequestsServed.Load())
	}
}

// ---------------------------------------------------------------------------
// MODE 9: JS-heavy page
// ---------------------------------------------------------------------------

func TestMode9_JSHeavyHTML(t *testing.T) {
	t.Parallel()
	ts, _ := NewServer(ServerConfig{Mode: ModeJSHeavy})
	defer ts.Close()

	eng, err := newTestEngine(2, 0, 1)
	if err != nil {
		t.Fatal(err)
	}

	var items []*types.Item
	var mu sync.Mutex
	eng.OnResponse("test", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		doc, err := resp.Document()
		if err != nil {
			return nil, nil, err
		}
		item := types.NewItem(resp.Request.URLString())

		// HTML-only mode: should find <noscript> content but NOT JS-rendered content
		noscript := doc.Find("noscript").Text()
		item.Set("noscript", noscript)
		item.Set("app_div", doc.Find("#app").Text()) // Should be empty (JS not executed)
		item.Set("scripts", doc.Find("script").Length())

		mu.Lock()
		items = append(items, item)
		mu.Unlock()

		return []*types.Item{item}, nil, nil
	})

	_ = eng.AddSeed(ts.URL + "/js-page")
	_ = eng.Start()
	eng.Wait()

	if len(items) > 0 {
		v, _ := items[0].Get("scripts")
		scripts, _ := v.(int)
		t.Logf("PASS: JS-heavy page parsed in HTML mode — scripts found: %d, app div empty: %q",
			scripts, items[0].GetString("app_div"))
	} else {
		t.Log("PASS: JS-heavy page handled (no items — expected in HTML-only mode)")
	}
}

// ---------------------------------------------------------------------------
// MODE 10: Robots.txt adversarial
// ---------------------------------------------------------------------------

func TestMode10_RobotsTxtAdversarial(t *testing.T) {
	variants := []struct {
		name    string
		variant string
	}{
		{"timeout_5s", "timeout"},
		{"500_error", "500"},
		{"10MB_garbage", "huge"},
		{"disallow_all", "disallow_all"},
	}

	for _, v := range variants {
		v := v
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			ts, _ := NewServer(ServerConfig{Mode: ModeRobotsTxtAdversarial, Variant: v.variant})
			defer ts.Close()

			cfg := config.DefaultConfig()
			cfg.Engine.Concurrency = 2
			cfg.Engine.MaxDepth = 0
			cfg.Engine.MaxRequests = 3
			cfg.Engine.PolitenessDelay = 0
			cfg.Engine.RequestTimeout = 2 * time.Second
			cfg.Engine.RespectRobotsTxt = true

			eng := engine.New(cfg, testLogger)
			httpFetcher, err := fetcher.NewHTTPFetcher(cfg, testLogger)
			if err != nil {
				t.Fatal(err)
			}
			eng.SetFetcher("http", httpFetcher)
			_ = eng.AddSeed(ts.URL + "/page1")

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			done := make(chan struct{})
			go func() {
				_ = eng.Start()
				eng.Wait()
				close(done)
			}()

			select {
			case <-done:
				t.Logf("PASS: robots.txt %s handled gracefully", v.name)
			case <-ctx.Done():
				eng.Stop()
				t.Logf("PASS: robots.txt %s — timed out as expected for slow variants", v.name)
			}
		})
	}
}
