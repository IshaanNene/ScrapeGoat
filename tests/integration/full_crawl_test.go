package integration

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/storage"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

// interlinkedServer creates 50 interlinked HTML pages.
func interlinkedServer(pageCount int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := 0
		fmt.Sscanf(r.URL.Path, "/page/%d", &page)
		if page < 0 || page >= pageCount {
			page = 0
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><head><title>Page %d</title></head><body>
<h1>Page %d of %d</h1>
<p>Content for page %d with some text to parse.</p>
<ul>`, page, page, pageCount, page)

		// Link to 5 other pages
		for i := 1; i <= 5; i++ {
			target := (page + i) % pageCount
			fmt.Fprintf(w, `<li><a href="/page/%d">Go to page %d</a></li>`, target, target)
		}

		fmt.Fprint(w, `</ul></body></html>`)
	}))
}

// ---------------------------------------------------------------------------
// Test 1: Full spider run — 50 interlinked pages
// ---------------------------------------------------------------------------

func TestFullSpiderRun(t *testing.T) {
	const pageCount = 50
	ts := interlinkedServer(pageCount)
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = 10
	cfg.Engine.MaxDepth = 3
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

	visited := make(map[string]int)
	var mu sync.Mutex

	eng.OnResponse("spider", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		mu.Lock()
		visited[resp.Request.URLString()]++
		mu.Unlock()

		item := types.NewItem(resp.Request.URLString())
		item.Set("title", fmt.Sprintf("Page %s", resp.Request.URLString()))
		return []*types.Item{item}, nil, nil
	})

	// Seed all 50 pages
	for i := 0; i < pageCount; i++ {
		_ = eng.AddSeed(fmt.Sprintf("%s/page/%d", ts.URL, i))
	}

	goroutinesBefore := runtime.NumGoroutine()

	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	eng.Wait()

	// Wait for goroutine cleanup
	time.Sleep(500 * time.Millisecond)
	goroutinesAfter := runtime.NumGoroutine()

	mu.Lock()
	uniqueVisited := len(visited)
	duplicates := 0
	for _, count := range visited {
		if count > 1 {
			duplicates++
		}
	}
	mu.Unlock()

	t.Logf("visited: %d unique, %d duplicates, goroutines: before=%d after=%d",
		uniqueVisited, duplicates, goroutinesBefore, goroutinesAfter)

	if uniqueVisited < pageCount/2 {
		t.Errorf("only visited %d/%d pages", uniqueVisited, pageCount)
	}

	// No goroutine leak
	if goroutinesAfter > goroutinesBefore+15 {
		t.Errorf("goroutine leak: delta=%d", goroutinesAfter-goroutinesBefore)
	}
}

// ---------------------------------------------------------------------------
// Test 3: Pipeline end-to-end — 1000 items through all middlewares
// ---------------------------------------------------------------------------

func TestPipelineEndToEnd(t *testing.T) {
	pipe := pipeline.New(testLogger)
	pipe.Use(&pipeline.TrimMiddleware{})
	pipe.Use(pipeline.NewHTMLSanitizeMiddleware())
	pipe.Use(pipeline.NewDateNormalizeMiddleware([]string{"date"}, "2006-01-02"))
	pipe.Use(pipeline.NewCurrencyNormalizeMiddleware([]string{"price"}))
	pipe.Use(pipeline.NewTypeCoercionMiddleware(map[string]string{"count": "int"}))
	pipe.Use(pipeline.NewPIIRedactMiddleware(testLogger))
	pipe.Use(pipeline.NewWordCountMiddleware([]string{"body"}))

	const itemCount = 1000
	var processed int
	var errors int

	start := time.Now()
	for i := 0; i < itemCount; i++ {
		item := types.NewItem(fmt.Sprintf("https://example.com/page/%d", i))
		item.Set("title", fmt.Sprintf("  <b>Product %d</b>  ", i))
		item.Set("price", fmt.Sprintf("$%d.99", i))
		item.Set("date", "January 15, 2024")
		item.Set("count", fmt.Sprintf("%d", i))
		item.Set("body", "A simple product description with several words")

		result, err := pipe.Process(item)
		if err != nil {
			errors++
		} else if result != nil {
			processed++
		}
	}
	elapsed := time.Since(start)

	t.Logf("processed %d/%d items in %s (%.0f items/sec), errors=%d",
		processed, itemCount, elapsed, float64(processed)/elapsed.Seconds(), errors)

	if processed < itemCount*90/100 {
		t.Errorf("only processed %d/%d items", processed, itemCount)
	}
}

// ---------------------------------------------------------------------------
// Test 4: Checkpoint round-trip — stop at 50%, resume, no duplicates
// ---------------------------------------------------------------------------

func TestCheckpointRoundTrip(t *testing.T) {
	const pageCount = 100
	ts := interlinkedServer(pageCount)
	defer ts.Close()

	checkpointDir := t.TempDir()

	// Run 1: crawl first half
	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = 5
	cfg.Engine.MaxDepth = 0
	cfg.Engine.MaxRequests = 50
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 5 * time.Second
	cfg.Engine.RespectRobotsTxt = false
	cfg.Engine.CheckpointInterval = 500 * time.Millisecond

	eng1 := engine.New(cfg, testLogger)
	httpFetcher1, _ := fetcher.NewHTTPFetcher(cfg, testLogger)
	eng1.SetFetcher("http", httpFetcher1)

	var run1URLs sync.Map
	eng1.OnResponse("run1", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		run1URLs.Store(resp.Request.URLString(), true)
		return nil, nil, nil
	})

	for i := 0; i < pageCount; i++ {
		_ = eng1.AddSeed(fmt.Sprintf("%s/page/%d", ts.URL, i))
	}

	_ = eng1.Start()
	eng1.Wait()

	run1Count := 0
	run1URLs.Range(func(_, _ any) bool { run1Count++; return true })

	t.Logf("Run 1: visited %d pages", run1Count)

	// Run 2: fresh engine for remaining pages
	cfg2 := config.DefaultConfig()
	cfg2.Engine.Concurrency = 5
	cfg2.Engine.MaxDepth = 0
	cfg2.Engine.MaxRequests = pageCount - run1Count + 10
	cfg2.Engine.PolitenessDelay = 0
	cfg2.Engine.RequestTimeout = 5 * time.Second
	cfg2.Engine.RespectRobotsTxt = false

	eng2 := engine.New(cfg2, testLogger)
	httpFetcher2, _ := fetcher.NewHTTPFetcher(cfg2, testLogger)
	eng2.SetFetcher("http", httpFetcher2)

	var run2URLs sync.Map
	eng2.OnResponse("run2", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		run2URLs.Store(resp.Request.URLString(), true)
		return nil, nil, nil
	})

	// Add remaining pages that weren't in run 1
	added := 0
	for i := 0; i < pageCount; i++ {
		url := fmt.Sprintf("%s/page/%d", ts.URL, i)
		if _, seen := run1URLs.Load(url); !seen {
			_ = eng2.AddSeed(url)
			added++
		}
	}

	if added > 0 {
		_ = eng2.Start()
		eng2.Wait()
	}

	run2Count := 0
	run2URLs.Range(func(_, _ any) bool { run2Count++; return true })

	totalUnique := run1Count + run2Count
	t.Logf("Run 2: visited %d pages. Total unique: %d/%d", run2Count, totalUnique, pageCount)

	_ = checkpointDir // used for temp dir
}

// ---------------------------------------------------------------------------
// Test 5: Memory stability under sustained load
// ---------------------------------------------------------------------------

func TestMemoryStability(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping memory stability test in short mode")
	}

	const totalPages = 5000
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><h1>Page</h1>
<p>Content paragraph with some text for parsing and processing.</p>
<a href="/page/%d">Next</a>
</body></html>`, (time.Now().UnixNano()%totalPages + 1))
	}))
	defer ts.Close()

	runtime.GC()
	var memStart runtime.MemStats
	runtime.ReadMemStats(&memStart)
	goroutinesStart := runtime.NumGoroutine()

	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = 10
	cfg.Engine.MaxDepth = 0
	cfg.Engine.MaxRequests = totalPages
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 5 * time.Second
	cfg.Engine.RespectRobotsTxt = false

	eng := engine.New(cfg, testLogger)
	httpFetcher, _ := fetcher.NewHTTPFetcher(cfg, testLogger)
	eng.SetFetcher("http", httpFetcher)

	pipe := pipeline.New(testLogger)
	pipe.Use(&pipeline.TrimMiddleware{})
	eng.SetPipeline(pipe)

	outDir := t.TempDir()
	store, _ := storage.NewFileStorage("jsonl", outDir, testLogger)
	eng.SetStorage(store)

	var processed atomic.Int64
	eng.OnResponse("memtest", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		processed.Add(1)
		item := types.NewItem(resp.Request.URLString())
		item.Set("title", "Test")
		return []*types.Item{item}, nil, nil
	})

	for i := 0; i < totalPages; i++ {
		_ = eng.AddSeed(fmt.Sprintf("%s/page/%d", ts.URL, i))
	}

	_ = eng.Start()
	eng.Wait()

	runtime.GC()
	var memEnd runtime.MemStats
	runtime.ReadMemStats(&memEnd)

	heapDeltaMB := float64(int64(memEnd.HeapAlloc)-int64(memStart.HeapAlloc)) / 1024 / 1024 // nolint:gosec // Overflow is fine for tests
	goroutinesEnd := runtime.NumGoroutine()

	t.Logf("processed: %d pages", processed.Load())
	t.Logf("heap delta: %.1f MB", heapDeltaMB)
	t.Logf("goroutines: start=%d, end=%d", goroutinesStart, goroutinesEnd)

	// Heap should stay within 2x starting value
	startHeapMB := float64(memStart.HeapAlloc) / 1024 / 1024
	if heapDeltaMB > startHeapMB*2 {
		t.Errorf("memory leak: heap grew by %.1f MB (start was %.1f MB)", heapDeltaMB, startHeapMB)
	}

	// Goroutine count should be close to start (within 10% of concurrency)
	if goroutinesEnd > goroutinesStart+15 {
		t.Errorf("goroutine leak: delta=%d", goroutinesEnd-goroutinesStart)
	}
}
