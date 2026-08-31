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

### Distributed master/worker — `internal/distributed/`

A Redis-backed task queue with at-least-once delivery and recovery of tasks abandoned by dead
workers. What it does not have is a crawl: `cmd/scrapegoat/main.go` sets the worker's crawl function
to a body that logs the task and returns `nil`. `internal/distributed` imports `internal/engine`
zero times, so there is no shared frontier, no shared dedup set, and no shared politeness state —
two workers pointed at one site would each enforce the delay against their own half of the traffic.

`scrapegoat scale N` is worse than unwired: it issues `GET /api/scale` without ever sending `N`, and
then prints a success message naming the count it did not change.

**To integrate:** the coordination has to be over crawl *state*, not over URL batches. That means an
event ledger the workers append to and read membership from, which is the same substrate resume
needs. Until that exists, distribution divides a crawl into pieces that cannot be polite to each
other.

---

## Planned

### Near term

- **`PopReady` is O(n) per dequeue.** The scan over `f.pq` has no `break`, so it walks every queued
  entry on every dequeue while holding `f.mu` — and the readiness probe calls through to
  `limiterFor`, which takes the throttler's lock and moves the domain to the front of the LRU. A
  supposedly non-consuming probe therefore mutates eviction order, and on a wide crawl can evict the
  slots it is only inspecting. Replace with domain-bucketed ready-sets and a heap of domains keyed
  by earliest-ready time, plus a read-only `Ready` path that does not touch the LRU.
- **Deterministic retry jitter.** `backoffFor` draws from the engine's shared random source under a
  lock, so delays are handed out in worker-arrival order. A replay with the same seed reproduces the
  same multiset of delays but not the same assignment of delays to requests. Derive each delay from
  the request's own identity instead, and the lock becomes unnecessary too.

### Medium term

- **Deterministic termination.** The idle monitor is a 600 ms heuristic; replace with an in-flight
  counter incremented before dequeue. `idleWorkers.Add(1)` also currently precedes `PopReady` and
  `Add(-1)` follows it, so a worker holding a just-dequeued request is briefly counted as idle.
- **HTTP caching** — ETag / If-Modified-Since / Cache-Control.
- **Browser-accurate HTTP/2 SETTINGS and header ordering.** uTLS-driven JA3/JA4 matching the
  advertised User-Agent has shipped, and the README is honest that it closes one signal and nothing
  more. These are the next two tells. Note the explicit decision not to pursue this past the point
  of diminishing returns — an evasion arms race against funded adversaries is not a position this
  project can hold.
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

**Largely shipped.** `internal/extract` replaced the CSS-selector list with text-density
and link-density scoring per DOM node, so extraction no longer depends on what anyone
named a class. It runs in the crawl path.

What remains is the evaluation. `docs/EXTRACTION.md` still lists a real-page comparison
against trafilatura and resiliparse as outstanding, and an extractor's quality claim is
only as good as its evaluation — publish the results including the cases where this
loses. Extracting the package as a standalone Go module is the natural occasion: Go has
no trafilatura-equivalent, it is independently useful and citable, and it is the best
available funnel into the rest of the project.

### 2. Near-duplicate detection

Current dedup is exact-URL only. A corpus needs *content* dedup: the same article
syndicated across twenty sites, the same page under a dozen tracking-parameter
variants, boilerplate-only pages. MinHash with LSH banding for near-duplicates,
SimHash for cheap fuzzy comparison.

This is the difference between "a pile of pages" and "a dataset". FineWeb's own
pipeline spends most of its complexity here.

### 3. Corpus output with provenance — **shipped**

JSON, JSONL, and CSV are fine for scraped records and wrong for a corpus.

- ✅ **Parquet** — `--corpus out.parquet`, flat columns, zstd, loadable by
  `datasets`, DuckDB, and Polars. JSONL remains available by extension.
- ✅ **A stable record schema** — versioned, with URL, canonical URL, fetch
  timestamp, HTTP status, content hash, extracted text, language, and extraction
  confidence.
- ✅ **Provenance for every record** — what robots.txt said at fetch time, which
  AI directives were present, any licence signal found, and the crawler identity
  used.

Provenance is not bookkeeping. It is what makes a dataset defensible when someone
asks where the data came from, and that question is being asked far more often
than it was two years ago.

See [docs/PROVENANCE.md](docs/PROVENANCE.md).

### 4. Compliance as a first-class feature — **shipped**

- ✅ **AI-specific directives** — `GPTBot`, `CCBot`, `Google-Extended`,
  `ClaudeBot`, `Applebot-Extended` and others, recorded per record with the
  vendor behind each. An agent missing from the known list still appears in the
  parsed groups, so the record stays complete as the list dates.
- ✅ **Page-level signals** — `noai` / `noimageai` in both `<meta name="robots">`
  and `X-Robots-Tag`, plus the W3C TDM reservation in meta and header form.
- ✅ **A machine-readable per-crawl compliance report** —
  `--compliance-report out.json`: totals, every restricted URL with its content
  hash and the grounds, sites blocking AI crawlers grouped by host, and warnings
  for anything that should not have happened.

The report records what happened; it does not certify that what happened was
right. No field says "compliant", because that is a judgement about a particular
use in a particular jurisdiction and a crawler is not in a position to make it.

Signals are recorded, never enforced. A crawler that silently dropped restricted
pages would produce a corpus whose gaps are invisible, and a downstream user
wanting a different policy would have no way to apply it.

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

### 7. The site

`docs/` is the GitHub Pages site, and it is part of the project rather than a
brochure bolted onto it. Rebuilt to show the evidence — measured numbers, the
JA3 hashes from captured bytes, the extraction comparison including where the
shipped version lost — instead of adjectives.

Two constraints it keeps: **no build step and no third-party requests**. The
previous stylesheet opened with an `@import` to Google Fonts, which is a
third-party request on every page load for a project whose own README is about
not sending data where it need not go. System fonts render instantly and leak
nothing, and a site with no toolchain cannot rot behind a toolchain nobody
reinstalls.

Outstanding: the phase list is maintained by hand and will drift from ROADMAP.md.
Generating it from this file at release time would fix that.

### 8. MCP tools shaped for corpora

The current tools are crawl-shaped: crawl, extract, screenshot, sitemap. An agent
building or querying a corpus wants different verbs — search *within* what has
been crawled, fetch a page as clean markdown, ask what a source's licence and
robots status were. Streaming results rather than one large blob, and a
capability-scoped tool surface so an agent can be given read-only access.
