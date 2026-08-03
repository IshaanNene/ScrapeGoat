package observability

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"testing"

	"github.com/IshaanNene/ScrapeGoat/internal/engine"
	"github.com/IshaanNene/ScrapeGoat/internal/fetcher"
	obs "github.com/IshaanNene/ScrapeGoat/internal/observability"
	"github.com/IshaanNene/ScrapeGoat/internal/parser"
	"github.com/IshaanNene/ScrapeGoat/internal/testutil"
)

// seriesValue pulls the value of a single series out of the exposition text.
func seriesValue(t *testing.T, exposition, series string) float64 {
	t.Helper()
	re := regexp.MustCompile("(?m)^" + regexp.QuoteMeta(series) + `\s+(\S+)$`)
	match := re.FindStringSubmatch(exposition)
	if match == nil {
		t.Fatalf("series %q not present in exposition:\n%s", series, exposition)
	}
	v, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		t.Fatalf("parse value for %q: %v", series, err)
	}
	return v
}

// TestEngineActuallyRecordsMetrics is the end-to-end regression test for the
// metrics endpoint reporting permanent zeroes.
//
// The recorder used to be a local variable in cmd/scrapegoat that was never handed
// to the engine, so /metrics came up, answered scrapes, and reported 0 for every
// counter throughout a million-page crawl. Asserting that the collectors exist is
// not enough — this drives a real crawl and reads the numbers back out.
func TestEngineActuallyRecordsMetrics(t *testing.T) {
	const pages = 5

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><head><title>Page</title></head><body>
			<h1>Heading</h1><p>Some body text for %s.</p></body></html>`, r.URL.Path)
	}))
	defer ts.Close()

	cfg := testutil.LoopbackConfig()
	cfg.Engine.Concurrency = 2
	cfg.Engine.MaxDepth = 0
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RespectRobotsTxt = false
	cfg.Storage.OutputPath = t.TempDir()
	cfg.Storage.BatchSize = 1

	eng := engine.New(cfg, testLogger)

	metrics := obs.NewMetrics(testLogger)
	eng.SetMetrics(metrics)

	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, testLogger)
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	eng.SetFetcher("http", httpFetcher)
	eng.SetParser(parser.NewCompositeParser(testLogger))

	for i := 0; i < pages; i++ {
		if err := eng.AddSeed(fmt.Sprintf("%s/page/%d", ts.URL, i)); err != nil {
			t.Fatalf("add seed %d: %v", i, err)
		}
	}

	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	eng.Wait()

	rec := httptest.NewRecorder()
	metrics.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	exposition := rec.Body.String()

	// The domain label is the hostname without the port, matching Request.Domain().
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	host := u.Hostname()

	requests := seriesValue(t, exposition,
		fmt.Sprintf(`scrapegoat_requests_total{domain="%s",outcome="ok"}`, host))
	if requests != pages {
		t.Errorf("scrapegoat_requests_total = %v, want %d", requests, pages)
	}

	responses := seriesValue(t, exposition,
		fmt.Sprintf(`scrapegoat_responses_total{domain="%s",status="200"}`, host))
	if responses != pages {
		t.Errorf("scrapegoat_responses_total = %v, want %d", responses, pages)
	}

	bytes := seriesValue(t, exposition, "scrapegoat_bytes_downloaded_total")
	if bytes <= 0 {
		t.Errorf("scrapegoat_bytes_downloaded_total = %v, want > 0", bytes)
	}

	latencyCount := seriesValue(t, exposition,
		`scrapegoat_fetch_duration_seconds_count{fetcher="http"}`)
	if latencyCount != pages {
		t.Errorf("fetch duration observations = %v, want %d", latencyCount, pages)
	}
}

// TestEngineWithoutMetricsDoesNotPanic keeps metrics genuinely optional.
func TestEngineWithoutMetricsDoesNotPanic(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><h1>x</h1></body></html>`)
	}))
	defer ts.Close()

	cfg := testutil.LoopbackConfig()
	cfg.Engine.Concurrency = 1
	cfg.Engine.MaxDepth = 0
	cfg.Engine.PolitenessDelay = 0
	cfg.Engine.RespectRobotsTxt = false
	cfg.Storage.OutputPath = t.TempDir()

	eng := engine.New(cfg, testLogger) // no SetMetrics call

	httpFetcher, err := fetcher.NewHTTPFetcher(cfg, testLogger)
	if err != nil {
		t.Fatalf("create fetcher: %v", err)
	}
	eng.SetFetcher("http", httpFetcher)
	eng.SetParser(parser.NewCompositeParser(testLogger))

	if err := eng.AddSeed(ts.URL); err != nil {
		t.Fatalf("add seed: %v", err)
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("start engine: %v", err)
	}
	eng.Wait()
}
