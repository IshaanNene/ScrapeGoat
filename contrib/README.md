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
| `benchmark` | Comparison harness | Never run under conditions fair enough to publish; see [docs/PERFORMANCE.md](../docs/PERFORMANCE.md). |

## What was here

`antibot`, `automation`, `plugin`, `dashboard` and `repl` have been deleted rather
than kept at arm's length. The reasoning above applies more strongly to them than to
what remains: moving a subsystem out of the binary reduces what it can break, but it
does not stop it needing dependency bumps, CI time, or a reader's attention.

`plugin` is the clearest case — this file already described it as "an extension point
with no real extensions" whose storage backends log rather than write. `antibot` had
no callers at all. `automation` duplicated `internal/fetcher/browser.go`. `dashboard`
was a status page reachable through a `dashboard` subcommand that never existed in
this binary. `repl` was a development tool.

They are in git history. Restoring one is cheap; carrying five indefinitely on the
chance that one gets used is not.

## Same module, deliberately

`contrib/` is a directory in the main module, not a separate one.

A separate module would be the stronger boundary, and it was the first plan. It
does not work here: Go's `internal/` rule is per-module, so `contrib/benchmark` —
which needs `internal/engine`, `internal/config`, and `internal/fetcher` — could not
import them. Making that
compile would mean promoting those packages to the public API purely to satisfy a
directory boundary, which would *expand* the supported surface. That is the exact
opposite of what the split is for.

So the boundary is convention plus tooling rather than the compiler: `contrib` is
absent from the binary, from the README, and from the determinism check's scope.

## Status

Unmaintained unless something here becomes load-bearing again. Bugs are not
tracked, and breaking changes in core may break these without notice. If you
depend on one of them, say so in an issue — that is information worth having.
