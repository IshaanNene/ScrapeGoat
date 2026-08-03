# Performance

Numbers, methodology, and how to reproduce them.

The project previously described itself as "high-performance" and claimed "10x
Scrapy throughput" while publishing no measurements at all — 31 benchmarks existed
and not one number was ever stated. Both claims are gone from the README. What is
below is measured, reproducible, and comes with its caveats attached.

## Environment

Every figure on this page was produced on:

| | |
|---|---|
| CPU | Apple M4 Pro (14 cores) |
| OS / arch | darwin / arm64 |
| Go | 1.26.1 |
| Command | `go test -run XXX -bench <name> ./internal/engine` |

These are single-machine microbenchmarks. They characterise the scheduler, not
end-to-end crawl throughput, which on any real target is dominated by network
latency and the politeness delay rather than by anything in this process.

## Frontier wake latency

The number that governs the scheduler is **wake latency**: a worker has finished a
fetch, arrives at an empty frontier, and parks. A new URL is discovered. How long
until the worker has it?

| Implementation | Wake latency |
|---|---|
| Polling (`TryPop` + 50 ms sleep) | **~49.1 ms** |
| Event-driven (`Pop` on a notify channel) | **~3.0 µs** |

Reproduce:

```bash
go test -run XXX -bench BenchmarkWakeLatency -benchtime=60x ./internal/engine
```

Both benchmarks live in `internal/engine/wake_latency_bench_test.go`. The polling
one reproduces the loop that actually shipped, so the comparison is against real
prior code rather than an estimate of it.

### Why this needed a purpose-built benchmark

A throughput benchmark does **not** measure this, and initially reported the two
implementations as roughly equal:

```
BenchmarkFrontierPopWaiting-14           200      83.12 ns/op
BenchmarkFrontierPollingBaseline-14      200      90.42 ns/op
```

Those numbers are real and they are useless. If the producer runs ahead of the
consumer, the queue is never empty, `TryPop` hits on its first try every time, and
the 50 ms sleep never executes. The benchmark measured a code path that a busy
crawl rarely takes, and would have supported the conclusion that the poll loop was
fine.

The wake-latency benchmarks force the parked state before each measurement, which
is the state a worker is in every time the frontier drains — routine at any depth
limit, and constant near the end of a crawl.

**A caveat on the 49.1 ms.** The harness pushes shortly after the consumer enters
its sleep, so it measures close to the worst case. Under uniformly-distributed
arrivals the old implementation's *median* would be about 25 ms, with 50 ms the
ceiling. Either way the comparison against ~3 µs holds; the honest statement is
"tens of milliseconds versus microseconds", not a single ratio.

*Correction: an earlier commit message quoted "~25 ms → 142 ns" for this change.
The 142 ns came from the throughput benchmark above, which — as this section
explains — does not measure wake latency. The figures on this page supersede it.*

## Frontier throughput

Uncontended enqueue/dequeue, with an item already queued:

```
BenchmarkFrontierPopReady-14      200000      24.64 ns/op      24 B/op     1 allocs/op
BenchmarkFrontierPopWaiting-14    200000      99.04 ns/op      32 B/op     1 allocs/op
```

The single allocation per operation is the heap item. Removing it would need a
free list, which is not obviously worth the complexity at 25 ns.

## Deduplication

```
BenchmarkDedupExact-14    200000     410.8 ns/op     264 B/op      4 allocs/op
BenchmarkDedupBloom-14    200000     389.1 ns/op     392 B/op     11 allocs/op
```

Both are dominated by URL canonicalisation and SHA-256, not by the data structure —
which is why the two are within noise of each other on time. The difference that
matters is memory:

| Strategy | Memory | False-positive rate |
|---|---|---|
| Exact (map of hashes) | ~40 bytes/URL | 0 |
| Bloom | **1.20 bytes/URL** | 0.97% measured (1% target) |

Measured over 200,000 URLs by `TestBloomDedupIsMemoryBounded`; the false-positive
rate by `TestBloomDedupFalsePositiveRateIsHonest`, both in
`internal/engine/dedup_strategy_test.go`. Assertions, not estimates — the previous
Bloom implementation kept a filter *and* a full exact set, so its advertised
"10–100× memory saving" was not real, and only a measurement would have caught that.

The cost is not free: at 1%, roughly one URL in a hundred is treated as
already-seen and silently never crawled. Exact remains the default;
`engine.dedup_strategy: bloom` is for crawls where an exact set will not fit and
the alternative is running out of memory.

### The obvious next optimisation

Dedup allocates 4 times per URL: canonicalisation, the SHA-256 sum, and
`hex.EncodeToString` producing a 64-byte string used only as a map key. Switching
the key to `[32]byte` would remove the hex encoding entirely. Not done yet —
`Export`/`Import` serialise those hex strings into checkpoints, so it is a format
change, not a local one.

## What is not measured here

Stated so the absence is not mistaken for a claim:

- **End-to-end crawl throughput.** No published pages/second figure. Producing an
  honest one needs a controlled target and a defined workload; against a live site
  it would measure the site.
- **Comparisons to Scrapy, Colly, or Crawlee.** `internal/benchmark` contains a
  comparison harness, but nothing has been run under conditions fair enough to
  publish. The former "10x Scrapy" claim had no measurement behind it at all.
- **Memory under a long crawl.** The per-domain throttle and circuit-breaker maps
  are now LRU-bounded, but there is no soak test demonstrating a flat heap over
  hours.

## Running the full set

```bash
go test -run XXX -bench . -benchmem ./internal/...
```

For comparisons across a change, use `benchstat` rather than eyeballing single
runs — the wake-latency benchmarks in particular vary by a microsecond or two
between runs:

```bash
go test -run XXX -bench . -count=10 ./internal/engine > old.txt
# apply change
go test -run XXX -bench . -count=10 ./internal/engine > new.txt
benchstat old.txt new.txt
```
