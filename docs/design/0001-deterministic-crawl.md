# 0001 — Deterministic crawl and replay

**Status:** Draft

## Problem

A crawl is currently not reproducible. Run the same command twice against the same
site and you get different output: different item ordering, different link
discovery order, different counts if anything timed out. This is normal for
crawlers and it costs more than it appears to.

Four concrete failures follow from it:

**Datasets cannot be audited.** If a corpus is used to train or evaluate a model,
"where did this row come from" has no answer beyond a URL and a timestamp. There is
no way for a third party — or for the author six months later — to verify that the
stated inputs produce the stated output.

**Bugs cannot be reproduced.** The concurrency defects fixed in the v0.1.0 pass
(a dedup race, a missed wakeup on resume, a double close) were all found by
reading code, not by a failing test, because a scheduler whose behaviour depends on
goroutine timing cannot be made to fail on demand. `-race` finds data races; it does
not find *logic* races, and it only finds anything if the interleaving happens to
occur during the test run.

**Crawler policy cannot be evaluated.** Is priority-first better than
breadth-first for a given site? Does a shorter politeness delay actually improve
throughput once retries are counted? These are empirical questions with no way to
answer them, because changing the policy also changes which pages get fetched and
when — the treatment and the environment move together.

**Re-crawling is all-or-nothing.** Without a record of what was fetched and what it
contained, an incremental crawl cannot tell "unchanged" from "not yet visited".

## Constraints

- Live crawling must stay concurrent. Determinism must not cost throughput on the
  normal path.
- The public API must not require callers to thread a clock or an RNG through their
  own code.
- Storage overhead must be proportional to bytes fetched, not to bytes squared.
- Must work without any external service. A file-backed store is the baseline;
  object storage is an optimisation.

## Proposal

Make the crawl a function of three inputs:

```
crawl(seeds, policy, responses) -> (items, frontier_trace)
```

where `responses` is an oracle mapping a request to a response. Live crawling uses
the network as the oracle; replay uses a recorded store. The engine cannot tell the
difference.

Three pieces.

### 1. A content-addressed fetch log

Every response is written to a store keyed by the SHA-256 of its bytes, with an
index mapping `(url, attempt) -> digest`. Identical bytes are stored once, which
matters because re-crawls are mostly unchanged pages.

The index is the crawl's ledger: request, response digest, status, timing, and the
robots decision that permitted it.

### 2. Determinism seams

Three sources of nondeterminism, each removed the same way — inject it:

- **Clock.** `time.Now()` appears 17 times in the crawl path. Replace with a
  `Clock` interface; live uses the wall clock, replay uses a logical clock driven
  by recorded timestamps.
- **Randomness.** 11 `rand` call sites (backoff jitter, user-agent rotation,
  fingerprint selection). Replace with a seeded generator threaded from config, so
  a crawl's seed is part of its identity.
- **Iteration order.** Go randomises map iteration deliberately. Anywhere output
  order can depend on it, iterate a sorted key slice instead.

The parser is already pure — it touches neither the clock nor the network — which
is what makes this tractable rather than a rewrite.

### 3. Two tiers of reproducibility

Deliberately separated, because they cost very different amounts:

**Tier 1 — reproducible output.** Given a fetch log, the extracted dataset is
bit-identical. Requires pure extraction, canonical serialisation, and a total order
on output records. Does *not* require deterministic scheduling: if extraction is
per-page pure, the set of results does not depend on the order pages were
processed, so sorting at the end is sufficient.

**Tier 2 — reproducible crawl.** Given a fetch log, the frontier evolves
identically: same pop order, same discovery sequence. Requires a total order on
frontier entries (priority, then discovery index, then URL hash — never arrival
time) and a deterministic consumer. Replay runs single-threaded; live crawling
stays concurrent and gets Tier 1 only.

Tier 1 delivers the dataset guarantee and is worth doing on its own. Tier 2 is what
makes simulation testing and policy evaluation possible.

## Alternatives considered

**Record only the final output, hash it, and call that provenance.** Cheapest
option, and it is what most pipelines do. Rejected: a hash of the output proves the
output has not been altered since it was written. It says nothing about where it
came from, and it cannot be replayed, so it does not help with bugs, testing, or
policy evaluation. It answers the weakest version of the question.

**Use WARC as the primary store rather than a content-addressed one.** WARC is the
interchange format of the archiving world and ScrapeGoat should read and write it
regardless. Rejected as the *primary* store: WARC is append-only and
record-oriented, with no deduplication of identical payloads across records and no
efficient random access by URL. A re-crawl of a mostly-unchanged site would store
every unchanged page again. Content-addressed storage with a WARC import/export
path gets both.

**Make the whole engine single-threaded and declare it deterministic.** Simple, and
genuinely reproducible. Rejected: it gives up the concurrency that is the point of
writing a crawler in Go, and a system that is only correct in its slow mode has not
been tested in the mode it actually runs in. The two-tier split keeps the fast path
fast and the verifiable path verifiable.

**Deterministic scheduling in live crawls via a virtual clock and cooperative
scheduling.** This is the FoundationDB approach and it would make live crawls fully
reproducible. Rejected for now, not on principle: it requires every I/O operation to
go through a scheduler-aware layer, which in Go means either a custom runtime or
threading a context-like handle through every call. The cost is large and the
benefit over replay-based determinism is small, because the interesting bugs are
reproducible from a recorded log. Revisit if fault injection needs finer control
than the response oracle can express.

**Skip determinism; write more tests instead.** Rejected because it does not
address the failure. The concurrency bugs in this codebase survived a 260-test suite
with `-race` in CI. More tests of the same kind would have found more of the same
things. The gap is not test count, it is that the system cannot be made to fail on
demand.

## Consequences

**Easier:**

- Datasets ship with a manifest that a third party can verify by replaying.
- Concurrency bugs become reproducible: record the log, replay with a different
  interleaving seed, assert the invariant. This is the substrate for deterministic
  simulation testing.
- Crawler policies become measurable — hold the response log fixed, vary the
  policy, compare. Currently impossible.
- Incremental re-crawl gets its prerequisite: a record of what was fetched and what
  it contained.

**Harder:**

- Every new code path in the crawl must take its clock and randomness from the
  injected sources. This is a standing discipline, not a one-time change, and it
  needs a lint rule to hold — a single `time.Now()` in a new middleware silently
  breaks replay.
- Storage cost. A crawl now retains its raw bytes. Content addressing makes this
  proportional to *distinct* bytes, but it is not free, and there needs to be a
  retention policy.
- The `Clock` and `Rand` seams appear in internal constructor signatures. Kept out
  of `pkg/scrapegoat` so callers are unaffected.

**Forecloses:**

- Anything that makes extraction impure — a parser that fetches a sub-resource, or
  consults the clock, breaks Tier 1. If that is ever needed, it must be a separate
  enrichment stage operating on the extracted records, not part of parsing.

## Open questions

- **Retention.** A billion-page crawl's fetch log is tens of terabytes. Retain
  bodies, or retain only digests and re-fetch on demand? A digest-only log still
  supports verification but not replay. Probably a per-crawl policy.
- **Response oracle for the browser fetcher.** Headless Chromium does its own
  network I/O and is not covered by the fetch log. Either proxy it through the
  recorder, or mark browser-fetched pages as non-replayable and exclude them from
  the reproducibility claim. The second is honest and much cheaper; the first is
  correct.
- **Ordering under retry.** A request retried after a backoff re-enters the
  frontier. Its discovery index is stable, but should its *position* reflect the
  original discovery or the retry? This changes replay order and needs deciding
  before Tier 2 is implemented.
- **Manifest signing.** Cosign is already used for release artifacts. Reusing it
  for crawl manifests is attractive, but a crawl manifest is signed by whoever ran
  the crawl, not by the project, so the trust model is different and needs its own
  thought.
