# Record and replay

A crawl can write down everything it fetched, and later be re-run from that
record instead of from the network.

```bash
scrapegoat crawl https://example.com --record ./crawl.log -o ./out
scrapegoat verify ./crawl.log
scrapegoat replay ./crawl.log -o ./out-again
```

The replay opens no sockets. `./out` and `./out-again` hold the same records —
and are byte-identical when the crawl was run with `deterministic_order` set
(see [Record order](#record-order) below; `replay` always sets it for itself).

## Why this is the interesting part

Three things a crawler cannot otherwise offer follow from one decision:

- **A dataset someone else can check.** Publish the log alongside the data and a
  third party can re-derive the data and confirm no byte was altered — without
  trusting you, and without re-crawling sites that may have changed or gone away.
- **Bugs that reproduce.** A concurrency failure that showed up once in ten
  thousand pages is normally unreproducible, because the input was the live web.
  With the responses pinned, the only remaining variable is the scheduler.
- **Policies you can actually compare.** Run the same recorded responses through
  two different configurations. Any difference in the output is attributable to
  the policy, because the input was identical:

  ```bash
  scrapegoat replay ./crawl.log --config-override aggressive.yaml -o ./out-b
  ```

## What is on disk

```
crawl.log/
  index.jsonl        one line per fetch attempt, append-only
  objects/ab/cdef…   response bodies, keyed by the SHA-256 of their bytes
  manifest.json      seeds, config, config hash, version, counts
```

Bodies are content-addressed, so identical bytes are stored once. That is not a
micro-optimisation: a re-crawl is mostly unchanged pages, and it is the
difference between a log you keep and a log you delete.

The index is JSONL rather than a database. Append-only means a crash truncates at
a record boundary instead of corrupting; it streams, so a billion-entry log need
not fit in memory; and `grep` works on it at three in the morning, which is worth
more than query planning for a write-once ledger.

## Failures are recorded too

A crawl that hit three 503s before a 200 took a different path through backoff and
the circuit breaker than one that succeeded immediately. A log holding only the
200 would replay a crawl that never happened, so every attempt is an entry —
errors are first-class, with no body and their status and retryability preserved.

Replay hands back the same *sequence*: attempt 0 gets the first 503, attempt 1 the
second, attempt 2 the success. When a replay under a different retry policy asks
for an attempt the recording does not have, it falls back to attempt 0 rather than
refusing — otherwise policy comparison would be impossible, which is one of the
three reasons the log exists.

## robots.txt travels the same path

robots.txt is fetched through the registered fetcher, not through a private
client. So a recorded crawl records its robots fetches, and a replay answers "was
this allowed?" from the log rather than from the live site. Without that, a replay
is neither offline nor a faithful account of the decisions the original crawl
made — the crawl's own policy would depend on what the site says today.

## Verification

```bash
scrapegoat verify ./crawl.log
```

Re-hashes every stored body against the address it is filed under, and checks
every log entry against the store. Tampering and bit rot are caught rather than
replayed. `--json` emits the report as JSON; the exit status is non-zero when the
log is not intact.

## What is guaranteed, and what is not

**Tier 1 — reproducible output.** Given a log, the extracted dataset is
bit-identical. `cmd/scrapegoat/replay_test.go` asserts it end to end: record a
crawl, replay it, compare the SHA-256 of the output files. Run it under `-race`
too — see below for why that matters.

Three things had to change to make it true, and all three are worth knowing:

- Items are dated from the response's fetch time, not from `time.Now()` at parse
  time. `_timestamp` now means "when this data was observed", which is what a
  consumer of a scraped record wants anyway. Before this, the log reproduced the
  fetches perfectly and every output line still differed.
- Replayed responses carry the *recorded* duration and timestamp. A replay that
  reported microsecond fetches would misrepresent the run it claims to reproduce.
- Records are written in a total order rather than in the order concurrent
  workers finished. See below.

## Record order

Concurrent workers finish in whatever order the scheduler gives them, so records
reach storage in a different sequence on every run. The records themselves are
identical; the file is not. This is the third of RFC 0001's three requirements for
Tier 1, and the easiest one to believe you already have — the first version of
this feature passed its byte-comparison test purely because the two runs happened
to interleave the same way, and only failed once `-race` perturbed the timing.

`storage.deterministic_order` fixes it, sorting records by fetch time, then URL,
then spider, then a canonical encoding of the fields. The last component is not
decorative: two items extracted from one response share a timestamp and a URL, and
without it they would compare equal and fall back to arrival order.

It is **off by default**, because ordering requires holding every record until
close, and streaming output is the reason the JSONL format exists — a crawl of a
hundred million pages should not be made to buffer them all. Turn it on when the
output will be compared or published:

```yaml
storage:
  type: jsonl
  deterministic_order: true
```

`scrapegoat replay` sets it unconditionally: reproducing a recorded run is the
only thing it does, and a scheduling-dependent order would defeat that. JSON
output buffers regardless of the setting, so it is always ordered.

**Tier 2 — reproducible crawl.** The frontier evolving identically, so that the
crawl visits URLs in the same order, requires a fixed random seed and a controlled
clock throughout. The seams are in place (`engine.WithClock`, `engine.WithRand`,
enforced by `scripts/check-determinism.sh`), and the manifest records the seed
when one was fixed. Concurrent crawls currently get Tier 1 only; a manifest with
`rand_seed: 0` says so, and `replay` prints a note.

## Limits

- Recording a large crawl costs disk. Content addressing keeps re-crawls cheap,
  but a first pass stores every distinct body.
- Browser-rendered pages execute JavaScript, which is not network I/O and is not
  covered by the log.
- A log from a crawl that was killed replays the crawl that was killed. The
  manifest's missing `finished_at` is how you tell, and `replay` says so.
- Byte-identical output needs `deterministic_order`, which trades bounded memory
  for it. Without the setting you get the same records in an arbitrary order.
