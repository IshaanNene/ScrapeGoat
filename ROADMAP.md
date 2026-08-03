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
- End-to-end crawl throughput against a controlled target, and a fair comparison
  harness against Scrapy and Colly. Scheduler and dedup numbers are already in
  [docs/PERFORMANCE.md](docs/PERFORMANCE.md).

---

## Direction: the corpus-building wedge

The generic-crawler space is crowded and commoditised. Colly has a decade of bug
fixes; Scrapy has fifteen years and an ecosystem; Firecrawl and Exa have companies.
Competing on "fetch and parse HTML" means losing on every axis that matters.

The gap worth occupying is one layer up: **turning a set of domains into a clean,
deduplicated, provenance-tracked corpus**, locally, with no per-request cost and no
data leaving the network. Almost every AI-adjacent web-data task is that task —
domain-specific pretraining and fine-tuning sets, RAG corpora, eval and benchmark
construction, RL environment scraping — and the tooling for it is mostly Python,
mostly research-grade, and mostly assumes you are starting from CommonCrawl.

The items below are ordered by how much they close that gap. Everything above this
section is maintenance of what exists; this is where the project would become
something people reach for rather than something that competes with Colly.

### 1. Content extraction that survives the real web

The single largest gap. `AutoExtractor.extractArticles` matches CSS selectors —
`article`, `.post`, `.entry-content` — which works on well-marked-up sites and
fails on most of the web, returning navigation, cookie banners, and footers as
article text.

What is needed is algorithmic main-content detection: text-density and link-density
scoring per DOM node, boilerplate removal, and a confidence signal so downstream
consumers can filter. Python has trafilatura and resiliparse; Go has nothing
comparable, which makes this both the highest-leverage internal fix and a
genuinely reusable package in its own right.

Everything downstream depends on this. Deduplication, quality filtering, and
embedding are all garbage-in-garbage-out on bad extraction.

### 2. Near-duplicate detection

Current dedup is exact-URL only. A corpus needs *content* dedup: the same article
syndicated across twenty sites, the same page under a dozen tracking-parameter
variants, boilerplate-only pages. MinHash with LSH banding for near-duplicates,
SimHash for cheap fuzzy comparison.

This is the difference between "a pile of pages" and "a dataset". FineWeb's own
pipeline spends most of its complexity here.

### 3. Corpus output with provenance

JSON, JSONL, and CSV are fine for scraped records and wrong for a corpus. Needed:

- **Parquet**, so the output is directly loadable by `datasets`, DuckDB, and Polars.
- **A stable record schema**: URL, canonical URL, fetch timestamp, HTTP status,
  content hash, extracted text, language, and extraction confidence.
- **Provenance for every record**: what robots.txt said at fetch time, which AI
  directives were present, any licence signal found, and the crawler identity used.

Provenance is not bookkeeping. It is what makes a dataset defensible when someone
asks where the data came from, and that question is being asked far more often
than it was two years ago.

### 4. Compliance as a first-class feature

`robots.txt` compliance exists. What does not:

- AI-specific directives (`GPTBot`, `CCBot`, `Google-Extended`, and successors),
  which are now how sites express intent about AI use specifically.
- Page-level signals: `noai` / `noimageai` meta tags, TDM reservation headers.
- A machine-readable per-crawl compliance report: what was respected, what was
  skipped, and why.

Most tools treat this as a checkbox. Treating it as a feature — with an auditable
report — is a real differentiator for anyone who has to justify their corpus.

### 5. Incremental recrawl

Per-URL change-frequency estimation driving recrawl priority, so a corpus is
maintained rather than rebuilt. This is what turns a one-shot tool into
infrastructure, and there is citable prior work (Cho & Garcia-Molina) to implement
against rather than inventing a heuristic.

### 6. WARC read and write

WARC is the interchange format of the web-archiving and dataset world. Writing it
makes ScrapeGoat's output usable by existing pipelines; reading it makes
ScrapeGoat usable *on* CommonCrawl, which is where essentially all large-scale
web-derived datasets actually start. This is the interoperability item — it stops
the project being an island.

### 7. MCP tools shaped for corpora

The current tools are crawl-shaped: crawl, extract, screenshot, sitemap. An agent
building or querying a corpus wants different verbs — search *within* what has
been crawled, fetch a page as clean markdown, ask what a source's licence and
robots status were. Streaming results rather than one large blob, and a
capability-scoped tool surface so an agent can be given read-only access.
