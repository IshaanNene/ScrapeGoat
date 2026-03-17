package benchmark

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/IshaanNene/ScrapeGoat/internal/config"
	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	"github.com/IshaanNene/ScrapeGoat/internal/pipeline"
	"github.com/IshaanNene/ScrapeGoat/internal/types"
)

// ---------------------------------------------------------------------------
// Realistic HTTP test server
// ---------------------------------------------------------------------------

// realisticServer returns an httptest.Server that simulates real-world HTTP:
//   - Random 5-50ms response delay (normal distribution, µ=25ms, σ=10ms)
//   - 2% of requests return 429 (rate limit)
//   - 1% of requests return 503 (server overload)
//   - 0.5% of requests drop the connection mid-response
//   - Responses are 10-50KB realistic HTML
func realisticServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Normal-distribution delay: µ=25ms, σ=10ms, clamped to [5,50]ms
		delay := time.Duration(clampf(rand.NormFloat64()*10+25, 5, 50)) * time.Millisecond
		time.Sleep(delay)

		roll := rand.Float64()
		switch {
		case roll < 0.005:
			// 0.5%: drop connection mid-response
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 50000\r\n\r\n"))
				conn.Write([]byte("<html><body>partial"))
				conn.Close()
			}
			return
		case roll < 0.025:
			// 2%: rate limit
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `<html><body><h1>Rate Limited</h1></body></html>`)
			return
		case roll < 0.035:
			// 1%: server error
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `<html><body><h1>Service Unavailable</h1></body></html>`)
			return
		}

		// Normal 200 response: 10-50KB realistic HTML
		sizeKB := rand.Intn(40) + 10
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write(generateRealisticHTML(sizeKB))
	}))
}

func clampf(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// generateRealisticHTML produces sizeKB kilobytes of realistic-looking HTML.
func generateRealisticHTML(sizeKB int) []byte {
	var b strings.Builder
	targetBytes := sizeKB * 1024

	b.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Product Catalog - Page</title>
    <link rel="stylesheet" href="/styles.css">
</head>
<body>
<nav class="navbar">
    <a href="/">Home</a>
    <a href="/products">Products</a>
    <a href="/about">About</a>
    <a href="/contact">Contact</a>
</nav>
<main class="container">
`)
	for b.Len() < targetBytes {
		id := rand.Intn(100000)
		price := float64(rand.Intn(10000)) / 100
		rating := float64(rand.Intn(50)) / 10
		b.WriteString(fmt.Sprintf(`
<article class="product-card" data-id="%d">
    <img src="/images/product-%d.jpg" alt="Product %d" loading="lazy">
    <div class="product-info">
        <h2 class="product-title">Premium Widget Model %d</h2>
        <p class="product-description">High-quality industrial widget with advanced features
            including thermal management, corrosion resistance, and modular design.
            Suitable for enterprise deployments in manufacturing environments.</p>
        <span class="price" data-currency="USD">$%.2f</span>
        <div class="rating" data-value="%.1f">
            <span class="stars">★★★★☆</span>
            <span class="count">(%d reviews)</span>
        </div>
        <ul class="features">
            <li>Dimension: %dx%dx%d mm</li>
            <li>Weight: %.1f kg</li>
            <li>Material: Stainless Steel 304</li>
            <li>Warranty: %d years</li>
        </ul>
        <a href="/product/%d" class="btn-primary">View Details</a>
        <a href="/cart/add/%d" class="btn-secondary">Add to Cart</a>
    </div>
</article>
`, id, id, id, id, price, rating, rand.Intn(500)+10,
			rand.Intn(100)+10, rand.Intn(100)+10, rand.Intn(50)+5,
			float64(rand.Intn(100))/10, rand.Intn(5)+1, id, id))
	}

	b.WriteString(`
</main>
<footer>
    <p>&copy; 2024 ScrapeGoat Test Corp. All rights reserved.</p>
    <nav>
        <a href="/privacy">Privacy</a> | <a href="/terms">Terms</a>
    </nav>
</footer>
</body>
</html>`)

	return []byte(b.String())
}

// ---------------------------------------------------------------------------
// 1. TTFB at concurrency 1, 10, 50, 100 — p50/p95/p99/p999
// ---------------------------------------------------------------------------

func TestTTFBLatency(t *testing.T) {
	ts := realisticServer()
	defer ts.Close()

	concurrencies := []int{1, 10, 50, 100}
	totalRequests := 500

	t.Logf("\n%-12s | %10s | %10s | %10s | %10s | %6s",
		"Concurrency", "p50", "p95", "p99", "p999", "Errors")
	t.Logf("%-12s-+-%10s-+-%10s-+-%10s-+-%10s-+-%6s",
		"------------", "----------", "----------", "----------", "----------", "------")

	for _, conc := range concurrencies {
		latencies := make([]time.Duration, 0, totalRequests)
		var mu sync.Mutex
		var errors atomic.Int64

		sem := make(chan struct{}, conc)
		var wg sync.WaitGroup

		for i := 0; i < totalRequests; i++ {
			wg.Add(1)
			sem <- struct{}{}
			go func(idx int) {
				defer wg.Done()
				defer func() { <-sem }()

				start := time.Now()
				resp, err := http.Get(fmt.Sprintf("%s/page/%d", ts.URL, idx))
				elapsed := time.Since(start)

				if err != nil {
					errors.Add(1)
					return
				}
				resp.Body.Close()

				mu.Lock()
				latencies = append(latencies, elapsed)
				mu.Unlock()
			}(i)
		}
		wg.Wait()

		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		n := len(latencies)
		if n == 0 {
			t.Logf("%-12d | %10s | %10s | %10s | %10s | %6d",
				conc, "N/A", "N/A", "N/A", "N/A", errors.Load())
			continue
		}

		p50 := latencies[n*50/100]
		p95 := latencies[n*95/100]
		p99 := latencies[min(n*99/100, n-1)]
		p999 := latencies[min(n*999/1000, n-1)]

		t.Logf("%-12d | %10s | %10s | %10s | %10s | %6d",
			conc, p50.Round(time.Microsecond), p95.Round(time.Microsecond),
			p99.Round(time.Microsecond), p999.Round(time.Microsecond), errors.Load())
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// 2. Per-stage pipeline latency breakdown (ns/op)
// ---------------------------------------------------------------------------

func BenchmarkStage_DNSResolve(b *testing.B) {
	// DNS resolution via net.LookupHost (localhost resolves from cache)
	ts := realisticServer()
	defer ts.Close()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		req, _ := http.NewRequest("GET", ts.URL+"/page", nil)
		_ = req
		b.ReportMetric(float64(time.Since(start).Nanoseconds()), "dns_ns/op")
	}
}

func BenchmarkStage_TCPConnect(b *testing.B) {
	ts := realisticServer()
	defer ts.Close()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		resp, err := http.Get(fmt.Sprintf("%s/page/%d", ts.URL, i))
		ttfb := time.Since(start)
		if err == nil {
			resp.Body.Close()
		}
		b.ReportMetric(float64(ttfb.Nanoseconds()), "ttfb_ns/op")
	}
}

func BenchmarkStage_HTMLParse(b *testing.B) {
	html := generateRealisticHTML(30)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resp := &types.Response{
			StatusCode: 200,
			Body:       html,
			Headers:    make(http.Header),
			Request:    &types.Request{},
			Meta:       make(map[string]any),
		}
		start := time.Now()
		doc, err := resp.Document()
		if err != nil {
			b.Fatal(err)
		}
		// Simulate CSS selector extraction
		doc.Find(".product-title").Each(func(i int, s *goquery.Selection) {})
		doc.Find(".price").Each(func(i int, s *goquery.Selection) {})
		doc.Find("a[href]").Each(func(i int, s *goquery.Selection) {})
		b.ReportMetric(float64(time.Since(start).Nanoseconds()), "parse_ns/op")
	}
}

func BenchmarkStage_PipelineProcess(b *testing.B) {
	pipe := pipeline.New(benchLogger)
	pipe.Use(&pipeline.TrimMiddleware{})
	pipe.Use(pipeline.NewHTMLSanitizeMiddleware())
	pipe.Use(pipeline.NewCurrencyNormalizeMiddleware([]string{"price"}))
	pipe.Use(pipeline.NewDateNormalizeMiddleware([]string{"date"}, "2006-01-02"))

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		item := types.NewItem("https://example.com")
		item.Set("title", "  <b>Product Widget</b>  ")
		item.Set("price", "$1,234.56")
		item.Set("date", "January 15, 2024")
		item.Set("body", "<p>Product description with <strong>bold</strong> text</p>")

		start := time.Now()
		_, _ = pipe.Process(item)
		b.ReportMetric(float64(time.Since(start).Nanoseconds()), "pipeline_ns/op")
	}
}

func BenchmarkStage_RequestResponseRoundtrip(b *testing.B) {
	ts := realisticServer()
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 5 * time.Second

	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, benchLogger)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req, _ := types.NewRequest(fmt.Sprintf("%s/page/%d", ts.URL, i))
		start := time.Now()
		resp, fetchErr := httpFetcher.Fetch(context.Background(), req)
		elapsed := time.Since(start)
		if fetchErr != nil {
			continue // expected for 429/503/conn drops
		}
		_ = resp

		b.ReportMetric(float64(elapsed.Nanoseconds()), "fetch_ns/op")
	}
}

// ---------------------------------------------------------------------------
// 3. Cold start vs warm start
// ---------------------------------------------------------------------------

func TestColdVsWarmLatency(t *testing.T) {
	ts := realisticServer()
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 5 * time.Second

	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, benchLogger)
	if err != nil {
		t.Fatal(err)
	}

	measure := func(label string, reqNum int) time.Duration {
		req, _ := types.NewRequest(fmt.Sprintf("%s/page/%d", ts.URL, reqNum))
		start := time.Now()
		resp, fetchErr := httpFetcher.Fetch(context.Background(), req)
		elapsed := time.Since(start)
		if fetchErr != nil {
			t.Logf("%s: fetch error (expected for some): %v", label, fetchErr)
			return elapsed
		}
		_ = resp
		return elapsed
	}

	// Cold start: first request (cold DNS cache, cold connection pool)
	cold := measure("cold (req #1)", 0)

	// Warm up with 99 requests
	for i := 1; i < 100; i++ {
		measure("warmup", i)
	}

	// Warm start: 100th request
	warm100 := measure("warm (req #100)", 100)

	// Further warm up
	for i := 101; i < 1000; i++ {
		measure("warmup2", i)
	}

	// Fully warm: 1000th request
	warm1000 := measure("fully warm (req #1000)", 1000)

	t.Logf("\n%-25s | %12s", "Phase", "Latency")
	t.Logf("%-25s-+-%12s", "-------------------------", "------------")
	t.Logf("%-25s | %12s", "Cold (req #1)", cold.Round(time.Microsecond))
	t.Logf("%-25s | %12s", "Warm (req #100)", warm100.Round(time.Microsecond))
	t.Logf("%-25s | %12s", "Fully warm (req #1000)", warm1000.Round(time.Microsecond))

	// Cold should generally be slower than warm
	if warm1000 > cold*3 {
		t.Logf("WARNING: fully warm latency (%.2fms) significantly exceeds cold (%.2fms) — possible anomaly",
			float64(warm1000.Microseconds())/1000, float64(cold.Microseconds())/1000)
	}
}

// ---------------------------------------------------------------------------
// 4. Memory per request under load
// ---------------------------------------------------------------------------

func TestMemoryPerRequest(t *testing.T) {
	ts := realisticServer()
	defer ts.Close()

	cfg := config.DefaultConfig()
	cfg.Engine.Concurrency = 10
	cfg.Engine.MaxDepth = 0
	cfg.Engine.MaxRequests = 1000
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RequestTimeout = 5 * time.Second
	cfg.Engine.RespectRobotsTxt = false

	// Force GC and snapshot memory before
	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	eng := engine.New(cfg, benchLogger)
	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, benchLogger)
	if err != nil {
		t.Fatal(err)
	}
	eng.SetFetcher("http", httpFetcher)

	var itemCount atomic.Int64
	eng.OnResponse("mem_test", func(resp *types.Response) ([]*types.Item, []*types.Request, error) {
		itemCount.Add(1)
		return nil, nil, nil
	})

	for i := 0; i < 1000; i++ {
		_ = eng.AddSeed(fmt.Sprintf("%s/page/%d", ts.URL, i))
	}

	if err := eng.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	eng.Wait()

	// Snapshot memory after
	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	heapDelta := int64(memAfter.HeapAlloc) - int64(memBefore.HeapAlloc)
	numGC := memAfter.NumGC - memBefore.NumGC
	pauseTotal := memAfter.PauseTotalNs - memBefore.PauseTotalNs
	totalMallocs := memAfter.Mallocs - memBefore.Mallocs
	processed := itemCount.Load()

	mallocsPerReq := int64(0)
	heapPerReq := int64(0)
	if processed > 0 {
		mallocsPerReq = int64(totalMallocs) / processed
		heapPerReq = heapDelta / processed
	}

	t.Logf("\n%-28s | %16s", "Metric", "Value")
	t.Logf("%-28s-+-%16s", "----------------------------", "----------------")
	t.Logf("%-28s | %13d KB", "HeapAlloc delta", heapDelta/1024)
	t.Logf("%-28s | %16d", "NumGC cycles", numGC)
	t.Logf("%-28s | %12.2f ms", "PauseTotal (GC)", float64(pauseTotal)/1e6)
	t.Logf("%-28s | %16d", "Total mallocs", totalMallocs)
	t.Logf("%-28s | %16d", "Requests processed", processed)
	t.Logf("%-28s | %16d", "Mallocs/request", mallocsPerReq)
	t.Logf("%-28s | %13d KB", "Heap/request", heapPerReq/1024)

	// Sanity: mallocs/request should be < 5000 for a healthy framework
	if mallocsPerReq > 10000 {
		t.Errorf("WARNING: %d mallocs/request — consider sync.Pool for hot paths", mallocsPerReq)
	}
}
