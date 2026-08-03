# Changelog

All notable changes to ScrapeGoat are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html). While
the major version is 0, the minor version is bumped for breaking changes.

## [Unreleased]

This release is a correctness and honesty pass. Several documented features were not
wired into the running system, and several security properties a crawler needs were
absent. Both are addressed, and the README now describes only what actually runs —
everything else moved to [ROADMAP.md](ROADMAP.md).

### Security

- **SSRF guard on every outbound fetch** (`internal/safety`). Scheme allowlist,
  post-DNS blocking of loopback / RFC1918 / link-local (including
  `169.254.169.254`) / CGNAT / multicast / reserved / IPv4-mapped and NAT64-embedded
  equivalents, DNS-rebinding resistance by dialling the validated IP rather than
  re-resolving the name, and a re-check on every redirect hop. Wired into the HTTP
  fetcher, robots.txt retrieval, sitemap discovery, and the MCP tool entry points.
  Opt out with `safety.allow_private_addresses`. See [SECURITY.md](SECURITY.md).
- **Decompression bomb fixed.** The body size limit was applied to the compressed
  stream and the decompressor wrapped it, so a small gzip payload could expand
  without bound inside `io.ReadAll`. The limit now applies after decompression, with
  an explicit `ErrBodyTooLarge` rather than silent truncation, plus a 100:1
  compression-ratio cap. `gzip`/`flate` readers are now closed.
- **API server fails closed.** It refuses to start without an API key; running
  unauthenticated requires the explicit `--insecure-no-auth` flag. An empty key
  previously meant "allow everyone".
- **The MCP HTTP transport requires an API key** and refuses to start without one.
  Its `authenticate` now denies rather than allows on a missing configured key.
- **CORS is deny-by-default.** `Access-Control-Allow-Origin: *` was sent
  unconditionally, which let any page the operator visited both drive the crawl
  endpoint and read the response. Origins now come from
  `api_server.allowed_origins`.
- **WebSocket `CheckOrigin` is a real check** against the same allowlist, closing
  cross-site WebSocket hijacking. It previously returned `true` unconditionally.
- **API keys are compared in constant time** (`crypto/subtle`), closing a timing
  oracle.
- **`Retry-After: -1` no longer produces a negative back-off** — found by the new
  fuzz target.

### Fixed

- **The scheduler no longer polls.** Workers parked on a 50 ms `time.Sleep` loop,
  putting a ~25 ms median floor under every dequeue. `Frontier.Pop` is now
  event-driven, waking on the `Push` that supplies the work. Measured on the
  parked-worker path: **~25 ms → 142 ns**. Idle CPU drops to zero.
- **Dedup race.** `IsSeen` followed by `MarkSeen` was a check-then-act race, so two
  workers extracting the same link both enqueued it. Replaced with atomic
  `MarkIfUnseen`. URLs rejected by robots.txt or the domain filter are no longer
  recorded as seen.
- **`Scheduler.Resume` data race.** The gate channel was reassigned without
  synchronisation while workers were selecting on it — both a race and a permanent
  missed wakeup for any worker that read the new channel.
- **`Engine.Wait` is idempotent.** It closed channels with no guard, so a second
  call panicked with "close of closed channel".
- **`ResultsChan` no longer corrupts output.** It returned the same channel storage
  was draining, so any consumer silently stole items from the output file. It now
  registers an independent subscriber; each caller sees the full stream.
- **Retries survive wrapped errors.** `err.(*types.FetchError)` became `errors.As`,
  which had silently disabled retries for any error wrapped with `%w`.
- **API handlers honour the server's configuration.** Every handler built a fresh
  `config.DefaultConfig()`, discarding operator settings — safety policy, timeouts,
  user agents, proxy — on all API-driven fetches.
- **In-flight items are no longer dropped on shutdown.** The pipeline drains what
  has already been scraped instead of abandoning it on context cancellation.
- **HTTP/2 is enabled** (`ForceAttemptHTTP2`). The custom transport had silently
  disabled it.

### Changed

- **`internal/types` moved to `pkg/scrapegoat/types`**, and the core types are
  aliased into `pkg/scrapegoat`. Consumers previously could call methods on values
  the SDK returned but could not name their types — no variable declarations, no
  struct fields, no functions over them. **Breaking** for anyone importing the
  internal path.
- **`Option` is now an opaque interface** rather than `func(*config.Config)`, so the
  internal configuration struct is no longer part of the public contract.
  **Breaking** for anyone who wrote their own `Option` literal.
- **`NewRobotsManager` takes a `*safety.URLGuard`.** **Breaking** for direct callers.
- **Metrics are now `prometheus/client_golang`.** The hand-written exposition
  declared every metric a counter (including gauges, where `rate()` yields garbage),
  carried no labels, and had no histograms. There are now labelled counters
  (`domain`, `status`, `outcome`), latency and size histograms, real gauges, and the
  Go runtime collectors. **Breaking**: the exported field names on
  `observability.Metrics` have changed.
- The metrics recorder is **actually passed to the engine**. It was previously a
  local variable in `cmd/scrapegoat`, so `/metrics` served permanently-zero counters
  for the entire life of a crawl.

### Added

- `SetMetrics` on the engine; a nil recorder disables collection.
- `--insecure-no-auth` and `--allowed-origin` flags on `scrapegoat serve`.
- `safety` configuration block.
- **15 fuzz targets** covering HTML parsing, CSS selectors, regex patterns, URL
  canonicalisation, dedup, `robots.txt` parsing and pattern matching, the
  decompression path, `Retry-After`, the MCP JSON-RPC decoder, and the URL guard.
  Run for 60s each in CI.
- Regression tests for every bug above, including the decompression bomb, the
  metadata-endpoint SSRF, the dedup race, pause/resume, double `Wait`, and the
  `ResultsChan` fan-out.
- `SECURITY.md` with the trust boundary and known limitations, `ROADMAP.md`,
  `CHANGELOG.md`, and `.golangci.yml`.

### Removed

- `examples/linkedin_scraper/`.
- The unused `errChan` field on the engine, and the decorative `sync.Cond` and
  `notEmpty` channel on the frontier — neither was ever waited on.

### Documentation

- README no longer claims the autoscaled worker pool, Bloom filter dedup, checkpoint
  pause/resume, or OpenTelemetry tracing, none of which were reachable from a
  running crawl. They are described, with integration notes, in ROADMAP.md.
- The "10x Scrapy throughput" comparison is gone; no benchmark supported it.
- `docs/architecture.md` states that checkpointing is save-only.
