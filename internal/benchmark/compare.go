package benchmark

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Benchmark runs performance comparisons and generates formatted results.
// Measures requests/sec, latency percentiles, bytes transferred, and error rates.
type Benchmark struct {
	logger *slog.Logger
}

// BenchmarkConfig configures a benchmark run.
type BenchmarkConfig struct {
	URL         string
	Concurrency int
	Duration    time.Duration
	Requests    int
	Headers     map[string]string
}

// BenchmarkResult holds the results of a benchmark run.
type BenchmarkResult struct {
	Tool        string        `json:"tool"`
	URL         string        `json:"url"`
	Concurrency int           `json:"concurrency"`
	Duration    time.Duration `json:"duration"`
	TotalReqs   int64         `json:"total_requests"`
	SuccessReqs int64         `json:"success_requests"`
	FailedReqs  int64         `json:"failed_requests"`
	ReqsPerSec  float64       `json:"requests_per_second"`
	AvgLatency  time.Duration `json:"avg_latency"`
	P50Latency  time.Duration `json:"p50_latency"`
	P95Latency  time.Duration `json:"p95_latency"`
	P99Latency  time.Duration `json:"p99_latency"`
	MinLatency  time.Duration `json:"min_latency"`
	MaxLatency  time.Duration `json:"max_latency"`
	BytesRecv   int64         `json:"bytes_received"`
	Throughput  float64       `json:"throughput_mbps"`
	ErrorRate   float64       `json:"error_rate"`
}

// NewBenchmark creates a new benchmark runner.
func NewBenchmark(logger *slog.Logger) *Benchmark {
	return &Benchmark{logger: logger.With("component", "benchmark")}
}

// Run executes a benchmark with the given configuration.
func (b *Benchmark) Run(cfg *BenchmarkConfig) *BenchmarkResult {
	b.logger.Info("benchmark starting",
		"url", cfg.URL,
		"concurrency", cfg.Concurrency,
		"duration", cfg.Duration,
	)

	var (
		totalReqs   atomic.Int64
		successReqs atomic.Int64
		failedReqs  atomic.Int64
		bytesRecv   atomic.Int64
		wg          sync.WaitGroup
		latencies   = make([]time.Duration, 0, 10000)
		mu          sync.Mutex
	)

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        cfg.Concurrency * 2,
			MaxIdleConnsPerHost: cfg.Concurrency * 2,
			MaxConnsPerHost:     cfg.Concurrency * 2,
		},
	}

	deadline := time.After(cfg.Duration)
	done := make(chan struct{})

	go func() {
		<-deadline
		close(done)
	}()

	start := time.Now()

	for i := 0; i < cfg.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}

				if cfg.Requests > 0 && totalReqs.Load() >= int64(cfg.Requests) {
					return
				}

				reqStart := time.Now()
				req, err := http.NewRequest("GET", cfg.URL, nil)
				if err != nil {
					failedReqs.Add(1)
					totalReqs.Add(1)
					continue
				}

				for k, v := range cfg.Headers {
					req.Header.Set(k, v)
				}

				resp, err := client.Do(req)
				elapsed := time.Since(reqStart)
				totalReqs.Add(1)

				if err != nil {
					failedReqs.Add(1)
				} else {
					successReqs.Add(1)
					bytesRecv.Add(resp.ContentLength)
					resp.Body.Close()
				}

				mu.Lock()
				latencies = append(latencies, elapsed)
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	// Calculate results
	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	result := &BenchmarkResult{
		Tool:        "ScrapeGoat",
		URL:         cfg.URL,
		Concurrency: cfg.Concurrency,
		Duration:    elapsed,
		TotalReqs:   totalReqs.Load(),
		SuccessReqs: successReqs.Load(),
		FailedReqs:  failedReqs.Load(),
		BytesRecv:   bytesRecv.Load(),
	}

	if elapsed > 0 {
		result.ReqsPerSec = float64(result.TotalReqs) / elapsed.Seconds()
		result.Throughput = float64(result.BytesRecv) / elapsed.Seconds() / 1024 / 1024
	}

	if result.TotalReqs > 0 {
		result.ErrorRate = float64(result.FailedReqs) / float64(result.TotalReqs) * 100
	}

	if len(latencies) > 0 {
		var total time.Duration
		for _, l := range latencies {
			total += l
		}
		result.AvgLatency = total / time.Duration(len(latencies))
		result.MinLatency = latencies[0]
		result.MaxLatency = latencies[len(latencies)-1]
		result.P50Latency = latencies[len(latencies)*50/100]
		result.P95Latency = latencies[len(latencies)*95/100]

		p99Idx := len(latencies) * 99 / 100
		if p99Idx >= len(latencies) {
			p99Idx = len(latencies) - 1
		}
		result.P99Latency = latencies[p99Idx]
	}

	b.logger.Info("benchmark complete",
		"total_requests", result.TotalReqs,
		"requests_per_sec", fmt.Sprintf("%.0f", result.ReqsPerSec),
		"avg_latency_ms", result.AvgLatency.Milliseconds(),
		"error_rate", fmt.Sprintf("%.2f%%", result.ErrorRate),
	)

	return result
}

// ComparisonTable generates a markdown comparison table from benchmark results.
func ComparisonTable(results []*BenchmarkResult) string {
	var sb strings.Builder

	sb.WriteString("| Tool | Requests/sec | Avg Latency | P95 Latency | P99 Latency | Error Rate | Throughput |\n")
	sb.WriteString("|------|-------------|-------------|-------------|-------------|------------|------------|\n")

	for _, r := range results {
		sb.WriteString(fmt.Sprintf("| **%s** | %.0f req/s | %s | %s | %s | %.2f%% | %.2f MB/s |\n",
			r.Tool,
			r.ReqsPerSec,
			formatDuration(r.AvgLatency),
			formatDuration(r.P95Latency),
			formatDuration(r.P99Latency),
			r.ErrorRate,
			r.Throughput,
		))
	}

	return sb.String()
}

// SaveResults saves benchmark results to a JSON file.
func SaveResults(results []*BenchmarkResult, path string) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// formatDuration formats a duration in a human-readable way.
func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%.0fµs", float64(d.Microseconds()))
	}
	if d < time.Second {
		return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000)
	}
	return fmt.Sprintf("%.2fs", d.Seconds())
}

// DefaultBenchmarkConfig returns a sensible default configuration.
func DefaultBenchmarkConfig(url string) *BenchmarkConfig {
	return &BenchmarkConfig{
		URL:         url,
		Concurrency: 10,
		Duration:    10 * time.Second,
		Headers: map[string]string{
			"User-Agent": "ScrapeGoat-Benchmark/1.0",
		},
	}
}

// PrintResult prints a formatted benchmark result to stdout.
func PrintResult(r *BenchmarkResult) {
	fmt.Printf("\n📊 Benchmark Results: %s\n", r.Tool)
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("  URL:              %s\n", r.URL)
	fmt.Printf("  Concurrency:      %d\n", r.Concurrency)
	fmt.Printf("  Duration:         %s\n", r.Duration.Round(time.Millisecond))
	fmt.Printf("  Total Requests:   %d\n", r.TotalReqs)
	fmt.Printf("  Success:          %d\n", r.SuccessReqs)
	fmt.Printf("  Failed:           %d\n", r.FailedReqs)
	fmt.Printf("  Requests/sec:     %.0f\n", r.ReqsPerSec)
	fmt.Printf("  Avg Latency:      %s\n", formatDuration(r.AvgLatency))
	fmt.Printf("  P50 Latency:      %s\n", formatDuration(r.P50Latency))
	fmt.Printf("  P95 Latency:      %s\n", formatDuration(r.P95Latency))
	fmt.Printf("  P99 Latency:      %s\n", formatDuration(r.P99Latency))
	fmt.Printf("  Error Rate:       %.2f%%\n", r.ErrorRate)
	fmt.Printf("  Throughput:       %.2f MB/s\n", r.Throughput)
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")
}
