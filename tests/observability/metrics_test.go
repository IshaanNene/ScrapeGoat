package observability

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	obs "github.com/IshaanNene/ScrapeGoat/internal/observability"
)

var testLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

// scrape renders the current exposition output.
func scrape(t *testing.T, m *obs.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics endpoint returned %d", rec.Code)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("read metrics body: %v", err)
	}
	return string(body)
}

// ---------------------------------------------------------------------------
// 1. Gauges must be exported as gauges, not counters
// ---------------------------------------------------------------------------

// TestGaugesAreTypedAsGauges is the regression test for the hand-written
// exposition, which hardcoded "# TYPE <name> counter" for every metric. Declaring a
// gauge as a counter makes rate() over it produce garbage, which is a silent
// dashboard bug rather than a visible failure.
func TestGaugesAreTypedAsGauges(t *testing.T) {
	t.Parallel()

	m := obs.NewMetrics(testLogger)
	m.SetFrontierDepth(7)
	m.SetActiveWorkers(3)
	// A CounterVec emits nothing until a child series exists, so the counter
	// assertions below need at least one observation.
	m.RecordRequest("example.com", "ok")
	out := scrape(t, m)

	for _, name := range []string{"scrapegoat_frontier_depth", "scrapegoat_active_workers"} {
		want := "# TYPE " + name + " gauge"
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in exposition — a gauge exported as a counter breaks rate()", want)
		}
	}

	for _, name := range []string{"scrapegoat_requests_total", "scrapegoat_bytes_downloaded_total"} {
		want := "# TYPE " + name + " counter"
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in exposition", want)
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Latency is a histogram, so quantiles are answerable
// ---------------------------------------------------------------------------

func TestFetchDurationIsAHistogram(t *testing.T) {
	t.Parallel()

	m := obs.NewMetrics(testLogger)
	m.RecordResponse("example.com", "http", 200, 150*time.Millisecond, 2048)
	out := scrape(t, m)

	if !strings.Contains(out, "# TYPE scrapegoat_fetch_duration_seconds histogram") {
		t.Error("fetch duration is not a histogram — p95 latency cannot be computed")
	}
	if !strings.Contains(out, "scrapegoat_fetch_duration_seconds_bucket") {
		t.Error("histogram buckets are missing from the exposition")
	}
	if !strings.Contains(out, `scrapegoat_fetch_duration_seconds_sum{fetcher="http"}`) {
		t.Error("histogram is not labelled by fetcher")
	}
}

// ---------------------------------------------------------------------------
// 3. Labels carry the dimensions that make Prometheus worth using
// ---------------------------------------------------------------------------

func TestMetricsCarryLabels(t *testing.T) {
	t.Parallel()

	m := obs.NewMetrics(testLogger)
	m.RecordRequest("example.com", "ok")
	m.RecordRequest("other.example", "error")
	m.RecordResponse("example.com", "http", 404, 20*time.Millisecond, 512)
	out := scrape(t, m)

	for _, want := range []string{
		`scrapegoat_requests_total{domain="example.com",outcome="ok"} 1`,
		`scrapegoat_requests_total{domain="other.example",outcome="error"} 1`,
		`scrapegoat_responses_total{domain="example.com",status="404"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing labelled series %q\n--- exposition ---\n%s", want, out)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. Counters accumulate
// ---------------------------------------------------------------------------

func TestCountersAccumulate(t *testing.T) {
	t.Parallel()

	m := obs.NewMetrics(testLogger)
	for i := 0; i < 5; i++ {
		m.RecordItem("scraped")
	}
	m.RecordItem("dropped")
	m.RecordResponse("example.com", "http", 200, time.Second, 1000)
	m.RecordResponse("example.com", "http", 200, time.Second, 2000)

	out := scrape(t, m)

	if !strings.Contains(out, `scrapegoat_items_total{outcome="scraped"} 5`) {
		t.Errorf("scraped counter did not accumulate\n%s", out)
	}
	if !strings.Contains(out, `scrapegoat_items_total{outcome="dropped"} 1`) {
		t.Error("dropped counter missing")
	}
	if !strings.Contains(out, "scrapegoat_bytes_downloaded_total 3000") {
		t.Error("bytes counter did not sum across responses")
	}
}

// ---------------------------------------------------------------------------
// 5. A nil recorder is a no-op, so metrics stay optional
// ---------------------------------------------------------------------------

func TestNilMetricsIsSafe(t *testing.T) {
	t.Parallel()

	var m *obs.Metrics // deliberately nil

	// The engine holds a possibly-nil *Metrics; every recording call must tolerate
	// it, or metrics stop being optional and every test has to construct one.
	m.RecordRequest("example.com", "ok")
	m.RecordResponse("example.com", "http", 200, time.Second, 1)
	m.RecordItem("scraped")
	m.SetFrontierDepth(1)
	m.SetActiveWorkers(1)
	m.RecordProxyRotation()
	m.RecordProxyError()
}

// ---------------------------------------------------------------------------
// 6. Go runtime collectors are registered
// ---------------------------------------------------------------------------

func TestRuntimeCollectorsRegistered(t *testing.T) {
	t.Parallel()

	out := scrape(t, obs.NewMetrics(testLogger))
	for _, want := range []string{"go_goroutines", "go_memstats_alloc_bytes"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing runtime metric %q — a stuck crawl is much harder to debug without it", want)
		}
	}
}

// ---------------------------------------------------------------------------
// 7. Registries are independent
// ---------------------------------------------------------------------------

func TestMetricsIsolation(t *testing.T) {
	t.Parallel()

	m1 := obs.NewMetrics(testLogger)
	m2 := obs.NewMetrics(testLogger)

	m1.RecordItem("scraped")

	if strings.Contains(scrape(t, m2), `scrapegoat_items_total{outcome="scraped"}`) {
		t.Error("recording on one Metrics leaked into another")
	}
}

// ---------------------------------------------------------------------------
// 8. The HTTP server serves the exposition
// ---------------------------------------------------------------------------

func TestMetricsServer(t *testing.T) {
	m := obs.NewMetrics(testLogger)
	m.RecordItem("scraped")

	const port = 19291
	if err := m.StartServer(port, "/metrics"); err != nil {
		t.Fatalf("start metrics server: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/metrics", port))
	if err != nil {
		t.Fatalf("scrape metrics endpoint: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), `scrapegoat_items_total{outcome="scraped"} 1`) {
		t.Errorf("served exposition missing the recorded metric:\n%s", body)
	}

	healthResp, err := http.Get(fmt.Sprintf("http://localhost:%d/health", port))
	if err == nil {
		defer healthResp.Body.Close()
		if healthResp.StatusCode != 200 {
			t.Errorf("health status=%d, want 200", healthResp.StatusCode)
		}
	}
}
