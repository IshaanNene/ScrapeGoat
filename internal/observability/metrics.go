// Package observability exposes ScrapeGoat's operational metrics.
//
// Metrics are backed by prometheus/client_golang rather than a hand-written text
// exposition. The previous implementation emitted `# TYPE <name> counter` for every
// metric — including gauges like queue depth and active workers, where `rate()`
// produces nonsense — carried no labels, and had no histograms, so p95 fetch latency
// (the number a crawler is actually judged on) could not be expressed at all.
// Hand-rolling the format also forfeited the Go runtime collectors and guaranteed
// drift from the spec.
package observability

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the crawler's Prometheus collectors.
//
// Construct one with NewMetrics and hand it to engine.New. An engine given a nil
// Metrics records nothing, which keeps metrics genuinely optional rather than
// requiring every caller and test to build one.
type Metrics struct {
	registry *prometheus.Registry
	logger   *slog.Logger

	// RequestsTotal counts fetch attempts by domain and outcome. `outcome` is
	// "ok", "error", or "retry" — a status code alone cannot distinguish a
	// transport failure from an HTTP error.
	RequestsTotal *prometheus.CounterVec

	// ResponsesTotal counts responses by domain and status code. The code is a
	// label rather than a metric per class, so 404-vs-410 stays visible.
	ResponsesTotal *prometheus.CounterVec

	// FetchDuration is the histogram that makes p50/p95/p99 latency answerable.
	// Buckets span 10 ms to ~60 s: the range between a warm CDN and a timeout.
	FetchDuration *prometheus.HistogramVec

	// ResponseSize tracks downloaded body sizes, which is how you notice a site
	// that has started serving an interstitial instead of content.
	ResponseSize *prometheus.HistogramVec

	// ItemsTotal counts pipeline outcomes: scraped, dropped, stored.
	ItemsTotal *prometheus.CounterVec

	// BytesDownloaded is the cumulative transfer counter.
	BytesDownloaded prometheus.Counter

	// FrontierDepth and ActiveWorkers are gauges — they go down as well as up,
	// which is exactly what the old exposition could not express.
	FrontierDepth prometheus.Gauge
	ActiveWorkers prometheus.Gauge

	// ProxyRotations and ProxyErrors track proxy pool health.
	ProxyRotations prometheus.Counter
	ProxyErrors    prometheus.Counter
}

// NewMetrics creates and registers the collector set.
func NewMetrics(logger *slog.Logger) *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		registry: reg,
		logger:   logger.With("component", "metrics"),

		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "scrapegoat_requests_total",
			Help: "Fetch attempts, by domain and outcome (ok, error, retry).",
		}, []string{"domain", "outcome"}),

		ResponsesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "scrapegoat_responses_total",
			Help: "Responses received, by domain and HTTP status code.",
		}, []string{"domain", "status"}),

		FetchDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "scrapegoat_fetch_duration_seconds",
			Help:    "Time from request dispatch to response body read, by fetcher type.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"fetcher"}),

		ResponseSize: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "scrapegoat_response_size_bytes",
			Help:    "Decompressed response body size.",
			Buckets: prometheus.ExponentialBuckets(1024, 4, 8), // 1 KiB → ~16 MiB
		}, []string{"fetcher"}),

		ItemsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "scrapegoat_items_total",
			Help: "Pipeline item outcomes (scraped, dropped, stored).",
		}, []string{"outcome"}),

		BytesDownloaded: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "scrapegoat_bytes_downloaded_total",
			Help: "Total bytes downloaded.",
		}),

		FrontierDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "scrapegoat_frontier_depth",
			Help: "URLs currently queued in the frontier.",
		}),

		ActiveWorkers: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "scrapegoat_active_workers",
			Help: "Workers currently processing a request.",
		}),

		ProxyRotations: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "scrapegoat_proxy_rotations_total",
			Help: "Times the proxy pool rotated to a different proxy.",
		}),

		ProxyErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "scrapegoat_proxy_errors_total",
			Help: "Proxy connection failures.",
		}),
	}

	reg.MustRegister(
		m.RequestsTotal,
		m.ResponsesTotal,
		m.FetchDuration,
		m.ResponseSize,
		m.ItemsTotal,
		m.BytesDownloaded,
		m.FrontierDepth,
		m.ActiveWorkers,
		m.ProxyRotations,
		m.ProxyErrors,
	)

	// Go runtime and process collectors: goroutine count, heap, GC pause, open FDs.
	// Free with a real registry, and the first thing anyone debugging a stuck crawl
	// wants to see.
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return m
}

// Registry exposes the underlying registry, for tests and for callers that want to
// register collectors of their own.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Handler returns the HTTP handler serving the exposition endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}

// ServeHTTP serves metrics in the Prometheus exposition format.
func (m *Metrics) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.Handler().ServeHTTP(w, r)
}

// --- Recording helpers ---
//
// These exist so callers do not repeat label-name string literals at every call
// site, which is where label typos turn into silently-missing time series.

// RecordRequest records a dispatched request.
func (m *Metrics) RecordRequest(domain, outcome string) {
	if m == nil {
		return
	}
	m.RequestsTotal.WithLabelValues(domain, outcome).Inc()
}

// RecordResponse records a completed fetch: status, duration, and size.
func (m *Metrics) RecordResponse(domain, fetcher string, status int, d time.Duration, size int64) {
	if m == nil {
		return
	}
	m.ResponsesTotal.WithLabelValues(domain, strconv.Itoa(status)).Inc()
	m.FetchDuration.WithLabelValues(fetcher).Observe(d.Seconds())
	m.ResponseSize.WithLabelValues(fetcher).Observe(float64(size))
	m.BytesDownloaded.Add(float64(size))
}

// RecordItem records a pipeline outcome: "scraped", "dropped", or "stored".
func (m *Metrics) RecordItem(outcome string) {
	if m == nil {
		return
	}
	m.ItemsTotal.WithLabelValues(outcome).Inc()
}

// SetFrontierDepth publishes the current queue depth.
func (m *Metrics) SetFrontierDepth(n int) {
	if m == nil {
		return
	}
	m.FrontierDepth.Set(float64(n))
}

// SetActiveWorkers publishes the current busy-worker count.
func (m *Metrics) SetActiveWorkers(n int) {
	if m == nil {
		return
	}
	m.ActiveWorkers.Set(float64(n))
}

// RecordProxyRotation records a proxy switch.
func (m *Metrics) RecordProxyRotation() {
	if m == nil {
		return
	}
	m.ProxyRotations.Inc()
}

// RecordProxyError records a proxy failure.
func (m *Metrics) RecordProxyError() {
	if m == nil {
		return
	}
	m.ProxyErrors.Inc()
}

// StartServer starts the metrics HTTP server.
func (m *Metrics) StartServer(port int, path string) error {
	mux := http.NewServeMux()
	mux.Handle(path, m.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	addr := fmt.Sprintf(":%d", port)
	m.logger.Info("metrics server starting", "addr", addr, "path", path)

	go func() {
		server := &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
		}
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			m.logger.Error("metrics server error", "error", err)
		}
	}()

	return nil
}
