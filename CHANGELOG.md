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

### Added (Phase 3 — provenance)

- **`crawl --corpus` writes a provenance record per page**: where it came from,
  what was extracted, and what the source said about reuse — page-level `noai` /
  `noimageai`, the W3C TDM reservation, declared licences, and the AI-specific
  directives in the site's robots.txt. See [docs/PROVENANCE.md](docs/PROVENANCE.md).

  Signals are **recorded, never enforced**. A crawler that silently dropped
  restricted pages would produce a corpus whose gaps are invisible, and a
  downstream user wanting a different policy would have no way to apply it. The
  end-of-crawl summary counts them instead — a corpus that had excluded them would
  print zero and look clean.

  `content_hash` addresses the raw body in the fetch log, so a record is not
  merely asserted: with `--record` alongside `--corpus`, a third party can
  re-derive every record from bytes they can verify.

- **Two distinctions the schema keeps.** `tdm_reservation` is a pointer, so "the
  page said 0" stays distinct from "the page said nothing" — in some jurisdictions
  those differ, and a boolean would manufacture consent. And `noindex` is a
  statement about search engines, so it is recorded but excluded from the
  restrictive test rather than read as an AI opt-out.

- **robots.txt is parsed a second time, for reporting.** The engine's parser
  correctly discards every group aimed at another agent; provenance needs exactly
  that. `TestReportAgreesWithEnforcement` drives the real `RobotsManager` and the
  report over the same files and asserts they agree, so the duplication carries a
  guard rather than a comment.

- **Four fuzz targets** over robots.txt, page signals, headers, and record
  construction, all wired into CI. Each consumes bytes chosen by the site being
  crawled, and the invariants asserted are the ones a corpus depends on: a garbled
  robots.txt must not be reported as permission, and a malformed page must not come
  back claiming a reservation it never made.

### Fixed (Phase 3)

- **Extracted text lost block boundaries.** goquery's `Text()` concatenates
  descendant text with nothing between it, so `<h1>Reserved</h1><p>This page…</p>`
  flattened to `ReservedThis page…` — fabricating a token that appears in no
  document, which every tokeniser, language detector, and dedupe hash downstream
  would inherit. The eight-tier extraction benchmark is byte-identical before and
  after the fix, which is the finding: the synthetic corpus never produces adjacent
  blocks whose text would fuse.

- **A config file's `storage.output_path` and `storage.type` were unreachable.**
  `-o` defaults to `./output` and `-f` to `json`, and both were assigned
  unconditionally, so a crawl configured to write to one place wrote to another.
  Third instance of this trap after `max_depth`; same `flagChanged` guard.

### Added (Phase 2 — record & replay)

- **`crawl --record`, `scrapegoat replay`, and `scrapegoat verify`.** A crawl can
  write a content-addressed log of everything it fetched and later be re-run from
  it, opening no sockets. With `storage.deterministic_order` set, the recorded and
  replayed runs produce byte-identical output — asserted end to end in
  `cmd/scrapegoat/replay_test.go` by comparing the SHA-256 of the output files,
  under `-race` as well as without. See [docs/REPLAY.md](docs/REPLAY.md).

  `internal/fetchlog` holds the pieces: a SHA-256-addressed object store with
  crash-safe writes and read-time verification, an append-only JSONL ledger that
  survives a truncated tail, and a `Recorder`/`Player` pair that both satisfy the
  engine's `Fetcher` interface — so the engine cannot tell whether it is talking to
  the network or to a log.

  Failures are recorded alongside successes. A crawl that hit three 503s before a
  200 took a different path through backoff and the circuit breaker, so a log
  holding only the 200 would replay a crawl that never happened.

- **`scrapegoat verify` re-hashes every stored body** against the address it is
  filed under and checks every ledger entry against the store, so tampering and bit
  rot are caught rather than replayed. This is what makes a published dataset
  checkable by someone who does not trust whoever published it.

- **`storage.deterministic_order` puts output records in a total order** — fetch
  time, then URL, then spider, then a canonical encoding of the fields — instead
  of the order concurrent workers happened to finish in. Off by default, because
  it requires buffering every record and streaming is why the JSONL format exists;
  `scrapegoat replay` sets it for itself, and JSON output was always buffered so
  it is now always ordered.

  This closes the third of RFC 0001's three requirements for Tier 1. The first
  version of record/replay had the other two and passed its byte-comparison test
  anyway, because the two runs happened to interleave identically; it only failed
  once `-race` perturbed the timing. The records were never wrong — the file was
  a different permutation of them.

### Fixed (Phase 2)

- **Items are dated from the response, not from when the parser ran.**
  `types.NewItem` stamped `time.Now()`, which reached the output as `_timestamp`
  and differed on every line between two runs over identical bytes. Items now take
  their timestamp from the response's fetch time, applied centrally in the
  scheduler so callbacks and third-party parsers get it too. The field now means
  "when this data was observed", which is what a consumer of a scraped record
  wants regardless of replay.

- **robots.txt no longer bypasses the registered fetcher.** `RobotsManager` held
  its own HTTP client, so a replay reached out to the live site for robots.txt and
  the replayed crawl's policy decisions depended on what the site said today. It
  now routes through the engine's fetcher, which also means a recorded crawl
  records its robots fetches.

- **`max_depth` in a config file was unreachable.** `applyCLIOverrides` assigned
  `cfg.Engine.MaxDepth = depth` unconditionally, so cobra's flag default (3)
  overwrote the config value unless `-d` was also passed. It now applies only when
  the flag was actually set.

- **Closing a `Recorder` twice reported a failure on a clean shutdown.** The engine
  closes the fetchers it holds and the caller closes the log it opened; both are
  correct, so `Close` is now idempotent.

- **`RobotsManager` now takes a context.** A cancelled crawl no longer waits on a
  robots.txt round trip to learn whether it was allowed to make a request it is
  not going to make.

- **Dead `checkURL` in `cmd/scrapegoat` removed.** Left over from the change
  detector's move to `contrib/`, and it carried an unguarded `http.Client` — the
  same class of SSRF bypass that was fixed in `internal/seo`.

### Changed (lint)

- **The lint configuration was calibrated against the codebase it lints.** It had
  been written aspirationally and never run to green: 564 findings on the branch
  before this commit, which is the same as having no gate at all. Four settings
  were wrong rather than strict —

  - `forbidigo` was policing the determinism boundary repo-wide with no notion of
    where the boundary is, so it flagged `internal/clock` (the package that
    *implements* the clock), plus `contrib/`, `tests/`, and `cmd/`. Removed;
    `scripts/check-determinism.sh` was already the real gate, knows the boundary,
    and supports a per-line opt-out.
  - `misspell` under `locale: UK` flagged the CSS property `color` and the value
    `center`; under `locale: US` it flagged the prose, which is British
    throughout. Locale removed, leaving it to catch actual typos.
  - `errcheck`'s `check-blank` flagged `_ = f()`, turning the explicit
    acknowledgement of a discarded error into the offence.
  - `govet`'s `shadow` fired on `if err := f(); err != nil` inside functions that
    already had an `err` — the most common shape in Go, and not a bug in any of
    the ~44 places it fired.

  The remaining ~150 genuine findings are fixed in code, not silenced: unchecked
  errors now checked or logged, `errors.As` in place of error type assertions,
  dead code deleted, unchecked type assertions given explicit invariants, and the
  graceful-shutdown pattern extracted into a documented helper. Lint is green.

### Changed (Phase 0)

- **The engine takes time and randomness from injected sources.** First step of
  [docs/design/0001-deterministic-crawl.md](docs/design/0001-deterministic-crawl.md):
  `internal/clock` supplies `Clock`, `Timer`, and `Ticker`, threaded through the
  frontier, throttler, circuit breaker, robots manager, checkpoint manager, and
  autoscaled pool. Backoff jitter comes from an injected `*rand.Rand`. Constructors
  take `nil` to mean "the real one", so callers are unaffected. Enforced by
  `scripts/check-determinism.sh` in CI, because breaking the boundary is silent —
  nothing fails, the crawl just stops being reproducible.
- **Output order no longer depends on map iteration.** `Item.Keys` returns sorted
  keys, response callbacks dispatch in name order, and `FieldRenameMiddleware`
  applies renames in sorted order. The last was a correctness bug rather than a
  cosmetic one: a chained mapping (`a->b` with `b->c`) resolved differently between
  runs on identical input.
- **Nine subsystems moved to `contrib/`**: SEO auditing, crawl-graph export, change
  detection, anti-bot patterns, browser automation helpers, the plugin registry,
  the dashboard, the REPL, and the benchmark harness. They build and their tests
  pass; they are not in the binary. Sitemap discovery stayed in core as
  `internal/sitemap` — it is part of crawling. Reasoning in
  [contrib/README.md](contrib/README.md).
- **The CLI is 20 commands to 10.** Removed: `search`, `ai-crawl`, `dashboard`,
  `benchmark`, `graph`, `replay`, `watch`, `diff`. `replay` in particular had to go
  early: the name is needed for deterministic replay, and the old command generated
  a re-crawl URL list from a graph. **Breaking.**
- **The `scrapegoat_seo_audit` MCP tool and `POST /v1/seo-audit` are removed.**
  **Breaking.**

### Added (P1)

- **Per-domain rate-limiter slots.** `Frontier.PopReady` dequeues only requests
  whose domain is off cooldown, so a throttled domain no longer parks a worker.
  The old throttle slept while holding the domain's mutex, *after* dequeue, which
  made effective concurrency 1 on a single-domain crawl. Domain state is
  LRU-bounded; it previously grew forever.
- **Jittered exponential backoff and a per-domain circuit breaker.** Retries had
  no delay at all, and the one delay that existed (`Retry-After`) ran inside the
  worker. Backoff now happens on a timer and is counted in the termination
  condition so retries cannot be dropped by the idle monitor.
- **A real `RedisQueue`**, replacing the placeholder that delegated to an
  in-memory queue. BLMOVE-based at-least-once delivery, `Ack`/`Nack` on the
  `Queue` interface, and a reaper that recovers tasks abandoned by dead workers.
  It errors instead of falling back when Redis is unreachable. Tested against a
  real Redis in CI.
- **`crawl --resume`**, wiring `CheckpointManager.Load`, which had no caller.
  Restores the dedup set before seeding, so a re-run continues rather than
  restarting.
- **Selectable Bloom deduplication** (`engine.dedup_strategy: bloom`), now
  measuring 1.20 bytes/URL against ~40 for the exact set. The previous
  implementation kept a filter *and* a full exact set, so its advertised memory
  saving was not real.
- **`docs/PERFORMANCE.md`** with measured numbers, methodology, and an explicit
  list of what is *not* measured.
- **A malformed-HTML corpus with golden files** (`internal/parser/testdata/`).

### Fixed (P1)

- **`allowed_domains` matched by string equality**, so `example.com` rejected
  `www.example.com` and every subdomain — a crawl that found nothing and reported
  success. Now suffix matching on a label boundary, with public-suffix awareness
  so a rule cannot span a registrable boundary.
- **`<base href>` was ignored** during link resolution, so relative links on any
  page using one resolved against the wrong host. Caught by the new corpus.
- **`Retry-After: -1` produced a negative backoff.** Caught by a fuzz target.

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
  putting a floor of tens of milliseconds under every dequeue. `Frontier.Pop` is now
  event-driven, waking on the `Push` that supplies the work. Measured wake latency
  on the parked-worker path: **~49 ms → ~3 µs**. Idle CPU drops to zero. See
  [docs/PERFORMANCE.md](docs/PERFORMANCE.md) for methodology — an earlier draft of
  this entry quoted 142 ns from a throughput benchmark that does not measure wake
  latency at all.
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
