# ScrapeGoat Roadmap

This file tracks work that is **designed but not yet integrated**, and work that is planned but not
yet written. The rule for the README is simple: if a capability is listed in the feature table, it
runs in the crawl path. If it is on this page, it does not — however complete the code behind it may
look.

---

## Designed, not yet integrated

These subsystems exist in the tree, compile, and have unit tests. None of them is reachable from a
running crawl. They are listed here so that reading `internal/engine/` does not mislead you about
what the product actually does.

### Autoscaled worker pool — `internal/engine/autoscale.go`

`AutoscaledPool.Evaluate()` returns a `ScaleDecision` based on queue depth and system load, modelled
on Crawlee's autoscaled pool. Nothing consumes the decision. `Scheduler.Start()` launches exactly
`config.Engine.Concurrency` goroutines and that number never changes for the life of the crawl.

**To integrate:** have the scheduler own a resizable worker set — spawn on scale-up, and signal
individual workers to exit via a per-worker quit channel on scale-down — then drive it from
`Evaluate()` on a ticker. This must land *after* the frontier is event-driven, because a poll-loop
worker cannot be cheaply parked.

### Distributed tracing — `internal/observability/tracing.go`

A hand-rolled `Tracer`/`Span`/`SpanExporter`. `go.mod` contains no OpenTelemetry dependency, so this
cannot export OTLP and cannot reach Jaeger, Tempo, or Honeycomb. It also does not propagate W3C
`traceparent`, including across the master/worker HTTP boundary where a trace would be most useful.

**To integrate:** replace with `go.opentelemetry.io/otel` + OTLP exporter, and inject/extract
`traceparent` on the distributed HTTP hop.

---

## Planned

### Near term

- **Per-domain rate-limiter slots.** Politeness delay is currently enforced with `time.Sleep` while
  holding the per-domain mutex, *after* a worker has already dequeued. One slow domain parks the
  whole pool and starves every other domain. Replace with `golang.org/x/time/rate` limiters per
  domain and a frontier partitioned so workers only dequeue from domains that have a token.
- **Exponential backoff with jitter, and a circuit breaker** on the retry path.
- **`AllowedDomains` suffix matching.** Today the check is exact-match, so `example.com` rejects
  `www.example.com`. Needs public-suffix awareness.
- **HTTP/2.** The custom `TLSClientConfig`/`DialContext` silently disables it. Re-enabling costs
  nothing and removes an obvious bot fingerprint.
- **`testdata/` corpus and golden files** for the parser, captured from real pages.

### Medium term

- **Deterministic termination.** The idle monitor is a 600 ms heuristic; replace with an in-flight
  counter incremented before dequeue.
- **HTTP caching** — ETag / If-Modified-Since / Cache-Control.
- **Real anti-bot fingerprinting** — uTLS-driven JA3/JA4 matching the advertised User-Agent, and
  browser-accurate HTTP/2 SETTINGS and header ordering. This is what would make the anti-bot claim
  in the README true rather than aspirational.
- **Plugin system** via WASM or real Go plugins, replacing the current stubs.
- **`goleak` in `TestMain`** across packages.

### Longer term

- Pluggable frontier: in-memory heap / on-disk (Pebble) / distributed (Redis Streams or NATS
  JetStream) with consistent-hash partitioning by registrable domain.
- Incremental recrawl driven by per-URL change-frequency estimation.
- Published benchmark methodology and numbers in `docs/PERFORMANCE.md`.
