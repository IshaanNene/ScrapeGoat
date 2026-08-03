# contrib

Working code that is not part of the core.

These packages build and their tests pass. They are not in the `scrapegoat`
binary, not in the README's feature list, and not covered by the guarantees the
core makes.

## Why they moved here

ScrapeGoat had seventeen subsystems and twenty CLI commands. Each was competent;
together they said "this project does many things," which is not a claim worth
making. Focus is not decoration — it has two concrete costs when it is absent:

**Every subsystem is one the core's invariants have to hold across.** The engine
now takes time and randomness from injected sources so a crawl can be replayed
(see [docs/design/0001-deterministic-crawl.md](../docs/design/0001-deterministic-crawl.md)).
Keeping the SEO auditor and the change detector in core would mean either making
them deterministic too, or having a codebase where the central invariant holds in
some places and not others — which is worse than not having the invariant, because
it cannot be relied on.

**Feature surface is a maintenance liability that compounds.** A dashboard nobody
uses still breaks CI, still needs its dependencies bumped, still appears in the
security review.

## What is here

| Package | What it is | Why it is not core |
|---|---|---|
| `seo` | Meta-tag auditor, backlink extractor | A different product that happens to parse HTML. Sitemap discovery *is* crawling, so it stayed in core as `internal/sitemap`. |
| `crawlgraph` | SQLite URL graph, DOT/Mermaid export | Adjacent to the fetch log the determinism work introduces, and likely superseded by it. |
| `changedetect` | Webhook-based page monitoring | Incremental recrawl is on the roadmap, but driven by change-frequency estimation rather than polling with webhooks. |
| `antibot` | Block-detection patterns, stealth profiles | Had no callers at all. `internal/fetcher/fingerprint` covers the part that was doing real work. |
| `automation` | go-rod browser helpers | The used browser path is `internal/fetcher/browser.go`. |
| `plugin` | Plugin registry, experimental storage stubs | The S3/Kafka/Postgres backends log rather than write. An extension point with no real extensions. |
| `dashboard` | Web status page | — |
| `repl` | Interactive shell | A development tool, not product surface. |
| `benchmark` | Comparison harness | Never run under conditions fair enough to publish; see [docs/PERFORMANCE.md](../docs/PERFORMANCE.md). |

## Same module, deliberately

`contrib/` is a directory in the main module, not a separate one.

A separate module would be the stronger boundary, and it was the first plan. It
does not work here: Go's `internal/` rule is per-module, so `contrib/repl`,
`contrib/benchmark`, and `contrib/plugin` — which need `internal/engine`,
`internal/config`, and `internal/fetcher` — could not import them. Making that
compile would mean promoting those packages to the public API purely to satisfy a
directory boundary, which would *expand* the supported surface. That is the exact
opposite of what the split is for.

So the boundary is convention plus tooling rather than the compiler: `contrib` is
absent from the binary, from the README, and from the determinism check's scope.

## Status

Unmaintained unless something here becomes load-bearing again. Bugs are not
tracked, and breaking changes in core may break these without notice. If you
depend on one of them, say so in an issue — that is information worth having.
