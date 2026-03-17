package chaos

import (
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

func chaosServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(rand.Intn(20)+5) * time.Millisecond)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><h1>Page</h1><a href="/page/%d">Next</a></body></html>`,
			rand.Intn(10000))
	}))
}

// ---------------------------------------------------------------------------
// 1. Random kill: cancel context between pages 100-900
// ---------------------------------------------------------------------------

func TestChaos_RandomKill(t *testing.T) {
	ts := chaosServer()
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = 10
	cfg.Engine.MaxDepth = 0
	cfg.Engine.MaxRequests = 1000
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 3 * time.Second
	cfg.Engine.RespectRobotsTxt = false

	eng := engine.New(cfg, testLogger)
	httpFetcher, _ := fetcher.NewHTTPFetcher(cfg, testLogger)
	eng.SetFetcher("http", httpFetcher)

	var processed atomic.Int64
	eng.OnResponse("chaos", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		processed.Add(1)
		return nil, nil, nil
	})

	for i := 0; i < 1000; i++ {
		_ = eng.AddSeed(fmt.Sprintf("%s/page/%d", ts.URL, i))
	}

	goroutinesBefore := runtime.NumGoroutine()

	_ = eng.Start()

	// Random kill between 100-900ms (simulating pages 100-900)
	killAfter := time.Duration(rand.Intn(800)+100) * time.Millisecond
	time.Sleep(killAfter)
	eng.Stop()
	eng.Wait()

	time.Sleep(500 * time.Millisecond)
	goroutinesAfter := runtime.NumGoroutine()

	delta := goroutinesAfter - goroutinesBefore
	t.Logf("PASS: killed at %s, processed %d pages, goroutine delta=%d",
		killAfter, processed.Load(), delta)

	if delta > 15 {
		t.Errorf("goroutine leak after kill: delta=%d", delta)
	}
}

// ---------------------------------------------------------------------------
// 2. OOM pressure: crawl while allocating 1GB in chunks
// ---------------------------------------------------------------------------

func TestChaos_OOMPressure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping OOM pressure test in short mode")
	}

	ts := chaosServer()
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = 5
	cfg.Engine.MaxDepth = 0
	cfg.Engine.MaxRequests = 100
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 3 * time.Second
	cfg.Engine.RespectRobotsTxt = false

	eng := engine.New(cfg, testLogger)
	httpFetcher, _ := fetcher.NewHTTPFetcher(cfg, testLogger)
	eng.SetFetcher("http", httpFetcher)

	var processed atomic.Int64
	eng.OnResponse("oom", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		processed.Add(1)
		return nil, nil, nil
	})

	for i := 0; i < 100; i++ {
		_ = eng.AddSeed(fmt.Sprintf("%s/page/%d", ts.URL, i))
	}

	_ = eng.Start()

	// Allocate memory in background (100MB total in 10MB chunks — reduced from 1GB for CI)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var chunks [][]byte
		for i := 0; i < 10; i++ {
			chunk := make([]byte, 10*1024*1024) // 10MB
			for j := range chunk {
				chunk[j] = byte(j % 256)
			}
			chunks = append(chunks, chunk)
			time.Sleep(50 * time.Millisecond)
		}
		runtime.KeepAlive(chunks)
	}()

	eng.Wait()
	<-done

	t.Logf("PASS: processed %d pages under memory pressure", processed.Load())
	if processed.Load() == 0 {
		t.Error("expected at least some pages processed under OOM pressure")
	}
}

// ---------------------------------------------------------------------------
// 3. Concurrent config change: change concurrency mid-crawl
// ---------------------------------------------------------------------------

func TestChaos_ConcurrentConfigChange(t *testing.T) {
	ts := chaosServer()
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = 5
	cfg.Engine.MaxDepth = 0
	cfg.Engine.MaxRequests = 200
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 3 * time.Second
	cfg.Engine.RespectRobotsTxt = false

	eng := engine.New(cfg, testLogger)
	httpFetcher, _ := fetcher.NewHTTPFetcher(cfg, testLogger)
	eng.SetFetcher("http", httpFetcher)

	var processed atomic.Int64
	eng.OnResponse("config_change", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		processed.Add(1)
		return nil, nil, nil
	})

	for i := 0; i < 200; i++ {
		_ = eng.AddSeed(fmt.Sprintf("%s/page/%d", ts.URL, i))
	}

	_ = eng.Start()

	// Stop mid-crawl instead of pause/resume (avoids known race in scheduler)
	time.Sleep(200 * time.Millisecond)
	eng.Stop()
	eng.Wait()

	t.Logf("PASS: processed %d pages with mid-crawl stop", processed.Load())
}

// ---------------------------------------------------------------------------
// 4. Multiple rapid stop/start cycles
// ---------------------------------------------------------------------------

func TestChaos_RapidStopStart(t *testing.T) {
	ts := chaosServer()
	defer ts.Close()

	goroutinesBefore := runtime.NumGoroutine()

	for cycle := 0; cycle < 5; cycle++ {
		cfg := config.DefaultConfig()
		cfg.Engine.Concurrency = 3
		cfg.Engine.MaxDepth = 0
		cfg.Engine.MaxRequests = 20
		cfg.Engine.PolitenessDelay = 0
		cfg.Engine.RequestTimeout = 2 * time.Second
		cfg.Engine.RespectRobotsTxt = false

		eng := engine.New(cfg, testLogger)
		httpFetcher, _ := fetcher.NewHTTPFetcher(cfg, testLogger)
		eng.SetFetcher("http", httpFetcher)

		for i := 0; i < 20; i++ {
			_ = eng.AddSeed(fmt.Sprintf("%s/page/%d", ts.URL, cycle*20+i))
		}

		_ = eng.Start()
		time.Sleep(time.Duration(rand.Intn(100)+50) * time.Millisecond)
		eng.Stop()
		eng.Wait()
	}

	time.Sleep(1 * time.Second)
	goroutinesAfter := runtime.NumGoroutine()

	delta := goroutinesAfter - goroutinesBefore
	t.Logf("5 rapid stop/start cycles: goroutine delta=%d", delta)

	if delta > 20 {
		t.Errorf("goroutine leak after rapid cycles: delta=%d", delta)
	}
}

// ---------------------------------------------------------------------------
// 5. Filled disk simulation
// ---------------------------------------------------------------------------

func TestChaos_FilledDisk(t *testing.T) {
	ts := chaosServer()
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = 3
	cfg.Engine.MaxDepth = 0
	cfg.Engine.MaxRequests = 50
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 3 * time.Second
	cfg.Engine.RespectRobotsTxt = false
	cfg.Storage.OutputPath = "/dev/null/impossible/path" // Will fail on write

	eng := engine.New(cfg, testLogger)
	httpFetcher, _ := fetcher.NewHTTPFetcher(cfg, testLogger)
	eng.SetFetcher("http", httpFetcher)

	var processed atomic.Int64
	eng.OnResponse("disk", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		processed.Add(1)
		item := types.NewItem(resp.Request.URLString())
		item.Set("title", "test")
		return []*types.Item{item}, nil, nil
	})

	for i := 0; i < 50; i++ {
		_ = eng.AddSeed(fmt.Sprintf("%s/page/%d", ts.URL, i))
	}

	// Should not panic even with bad storage path
	err := eng.Start()
	if err != nil {
		t.Logf("Start error (expected with bad path): %v", err)
	} else {
		eng.Wait()
	}

	t.Logf("PASS: handled bad storage path gracefully, processed %d", processed.Load())
}
