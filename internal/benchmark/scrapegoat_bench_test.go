package benchmark

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

var benchLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

// ---------------------------------------------------------------------------
//  1. Requests/sec at concurrency 1, 10, 50, 100
// ---------------------------------------------------------------------------

func benchmarkRequestsAtConcurrency(b *testing.B, concurrency int) {
	b.Helper()

	// Spin up a trivial local HTTP server so we're measuring ScrapeGoat
	// overhead, not network latency.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h1>Bench</h1><a href="/page">link</a></body></html>`)
	}))
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = concurrency
	cfg.Engine.MaxDepth = 0 // seed-only
	cfg.Engine.MaxRequests = b.N
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 5 * time.Second

	var fetched atomic.Int64

	b.ResetTimer()
	b.ReportAllocs()

	eng := engine.New(cfg, benchLogger)
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, benchLogger)
	if err != nil {
		b.Fatalf("create fetcher: %v", err)
	}
	eng.SetFetcher("http", httpFetcher)

	eng.OnResponse("bench", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		fetched.Add(1)
		return nil, nil, nil
	})

	for i := 0; i < b.N; i++ {
		_ = eng.AddSeed(fmt.Sprintf("%s/page%d", ts.URL, i))
	}

	if err := eng.Start(); err != nil {
		b.Fatalf("start: %v", err)
	}
	eng.Wait()

	b.ReportMetric(float64(fetched.Load())/b.Elapsed().Seconds(), "req/s")
}

func BenchmarkRequests_Concurrency1(b *testing.B)   { benchmarkRequestsAtConcurrency(b, 1) }
func BenchmarkRequests_Concurrency10(b *testing.B)  { benchmarkRequestsAtConcurrency(b, 10) }
func BenchmarkRequests_Concurrency50(b *testing.B)  { benchmarkRequestsAtConcurrency(b, 50) }
func BenchmarkRequests_Concurrency100(b *testing.B) { benchmarkRequestsAtConcurrency(b, 100) }

// ---------------------------------------------------------------------------
//  2. Bloom filter lookup time at 1M and 10M URLs
// ---------------------------------------------------------------------------

func benchmarkBloomLookup(b *testing.B, preload int) {
	b.Helper()

	bf := engine.NewBloomFilter(preload, 0.01)
	for i := 0; i < preload; i++ {
		bf.Add(fmt.Sprintf("https://example.com/page/%d", i))
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		bf.Contains(fmt.Sprintf("https://example.com/page/%d", i%preload))
	}
}

func BenchmarkBloomLookup_1M(b *testing.B)  { benchmarkBloomLookup(b, 1_000_000) }
func BenchmarkBloomLookup_10M(b *testing.B) { benchmarkBloomLookup(b, 10_000_000) }

func benchmarkBloomAdd(b *testing.B, preload int) {
	b.Helper()

	bf := engine.NewBloomFilter(preload, 0.01)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		bf.Add(fmt.Sprintf("https://example.com/page/%d", i))
	}
	b.ReportMetric(float64(bf.MemoryUsageBytes())/1024/1024, "MB")
}

func BenchmarkBloomAdd_1M(b *testing.B)  { benchmarkBloomAdd(b, 1_000_000) }
func BenchmarkBloomAdd_10M(b *testing.B) { benchmarkBloomAdd(b, 10_000_000) }

// ---------------------------------------------------------------------------
//  3. Item pipeline throughput (items/sec)
// ---------------------------------------------------------------------------

func BenchmarkPipelineThroughput(b *testing.B) {
	pipe := pipeline.New(benchLogger)
	pipe.Use(&pipeline.TrimMiddleware{})
	pipe.Use(pipeline.NewHTMLSanitizeMiddleware())
	pipe.Use(pipeline.NewCurrencyNormalizeMiddleware([]string{"price"}))
	pipe.Use(pipeline.NewDateNormalizeMiddleware([]string{"date"}, "2006-01-02"))

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		item := types.NewItem("https://example.com")
		item.Set("title", "  <b>Product</b>  ")
		item.Set("price", "$1,234.56")
		item.Set("date", "January 15, 2024")
		item.Set("body", "<p>Description</p>")
		_, _ = pipe.Process(item)
	}
}

func BenchmarkPipelineThroughputParallel(b *testing.B) {
	pipe := pipeline.New(benchLogger)
	pipe.Use(&pipeline.TrimMiddleware{})
	pipe.Use(pipeline.NewHTMLSanitizeMiddleware())

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			item := types.NewItem("https://example.com")
			item.Set("title", " Hello <b>World</b> ")
			_, _ = pipe.Process(item)
		}
	})
}

// ---------------------------------------------------------------------------
//  4. Memory allocations per request (Frontier + Dedup)
// ---------------------------------------------------------------------------

func BenchmarkRequestAllocs(b *testing.B) {
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req, _ := types.NewRequest(fmt.Sprintf("https://example.com/page/%d", i))
		req.Priority = types.PriorityNormal
		req.Headers.Set("Accept", "text/html")
		_ = req.URLString()
		_ = req.Domain()
	}
}

func BenchmarkFrontierAllocsPushPop(b *testing.B) {
	f := engine.NewFrontier()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req, _ := types.NewRequest("https://example.com/page")
		req.Priority = i % 5
		f.Push(req)
	}
	for i := 0; i < b.N; i++ {
		f.TryPop()
	}
}

func BenchmarkDedupAllocsMarkAndCheck(b *testing.B) {
	d := engine.NewDeduplicator(b.N)
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		url := fmt.Sprintf("https://example.com/page/%d", i)
		d.MarkSeen(url)
		d.IsSeen(url)
	}
}

// ---------------------------------------------------------------------------
//  5. Full crawl simulation – concurrent engine with local HTTP server
// ---------------------------------------------------------------------------

func BenchmarkFullCrawlSimulation(b *testing.B) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body>
			<h1>Product</h1>
			<span class="price">$19.99</span>
			<a href="/page2">Next</a>
		</body></html>`)
	}))
	defer ts.Close()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		cfg := config.DefaultConfig()
		cfg.Engine.Concurrency = 5
		cfg.Engine.MaxDepth = 0
		cfg.Engine.MaxRequests = 50
		cfg.Engine.PolitenessDelay = 0
		cfg.Engine.RequestTimeout = 3 * time.Second

		eng := engine.New(cfg, benchLogger)
		httpFetcher, _ := fetcher.NewHTTPFetcher(cfg, benchLogger)
		eng.SetFetcher("http", httpFetcher)

		for j := 0; j < 50; j++ {
			_ = eng.AddSeed(fmt.Sprintf("%s/page%d", ts.URL, j))
		}
		_ = eng.Start()
		eng.Wait()
	}
}

// ---------------------------------------------------------------------------
//  6. Concurrent Bloom filter stress test
// ---------------------------------------------------------------------------

func BenchmarkBloomConcurrentWrite(b *testing.B) {
	bf := engine.NewBloomFilter(1_000_000, 0.01)
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			bf.Add(fmt.Sprintf("https://example.com/page/%d", i))
			i++
		}
	})
}

func BenchmarkBloomConcurrentReadWrite(b *testing.B) {
	bf := engine.NewBloomFilter(1_000_000, 0.01)
	// Pre-populate
	for i := 0; i < 100_000; i++ {
		bf.Add(fmt.Sprintf("https://example.com/page/%d", i))
	}

	b.ResetTimer()
	b.ReportAllocs()

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Writers
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 100_000; ; i++ {
			select {
			case <-ctx.Done():
				return
			default:
				bf.Add(fmt.Sprintf("https://example.com/page/%d", i))
			}
		}
	}()

	// Readers (benchmark loop)
	for i := 0; i < b.N; i++ {
		bf.Contains(fmt.Sprintf("https://example.com/page/%d", i%100_000))
	}
	cancel()
	wg.Wait()
}

// ---------------------------------------------------------------------------
//  7. Session pool throughput
// ---------------------------------------------------------------------------

func BenchmarkSessionPoolGet(b *testing.B) {
	pool := fetcher.NewSessionPool(fetcher.DefaultSessionPoolConfig(), benchLogger)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = pool.Get()
	}
}

func BenchmarkSessionPoolGetParallel(b *testing.B) {
	pool := fetcher.NewSessionPool(fetcher.DefaultSessionPoolConfig(), benchLogger)
	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = pool.Get()
		}
	})
}
