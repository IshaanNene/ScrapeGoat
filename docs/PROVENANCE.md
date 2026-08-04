# Provenance

A crawl can record, for every page, where it came from and what the source said
about how it may be used.

```bash
scrapegoat crawl https://example.com --corpus ./corpus.jsonl
```

```
  Corpus:    3 records -> ./corpus.jsonl
             3 from sources that asked to be excluded from AI training
             1 carry an explicit licence
```

## Why

A crawled dataset is defensible exactly as far as it can answer two questions:
where did this come from, and were you allowed to take it? Both have to be
answered at fetch time. A page can change its mind afterwards — add a `noai` tag,
tighten its robots.txt, disappear — and the crawl cannot go back and ask.

That question is asked far more often than it was two years ago, and "we scraped
it" is no longer an answer.

## What a record looks like

```json
{
  "schema_version": 1,
  "url": "https://example.com/article",
  "canonical_url": "https://example.com/article",
  "content_hash": "4b5752f262eb5ccf7fb42030…",
  "fetched_at": "2026-08-04T17:48:00.564331+05:30",
  "status_code": 200,
  "mime_type": "text/html",
  "text": "Reserved This page reserves its rights against text and data mining.",
  "title": "Reserved",
  "language": "en",
  "extraction_confidence": 0.667,
  "robots_allowed": true,
  "ai_directives": {
    "robots_present": true,
    "agents_addressed": ["gptbot"],
    "agents_blocked": ["gptbot"],
    "vendors_blocked": ["OpenAI"]
  },
  "signals": { "noai": true, "tdm_reservation": 1 },
  "crawl_id": "./crawl.log"
}
```

`content_hash` is the join key. It addresses the raw body in a
[fetch log](REPLAY.md), so a record is not merely asserted — someone else can
re-derive it from bytes they can verify:

```bash
scrapegoat crawl https://example.com --record ./crawl.log --corpus ./corpus.jsonl
scrapegoat verify ./crawl.log
```

## Recorded, never enforced

Nothing is filtered out. A crawler that silently dropped every restricted page
would produce a corpus whose gaps are invisible, and a downstream user who wanted
a different policy would have no way to apply it.

So the summary counts. A corpus that had excluded those pages would report zero
and look clean. Filtering is a decision for whoever builds the dataset, taken in
full view of what each source asked for:

```bash
jq 'select(.signals.noai != true and .signals.tdm_reservation != 1)' corpus.jsonl
```

## Distinctions the schema keeps

**Silence is not permission.** `tdm_reservation` is absent when the page said
nothing, and `0` when it explicitly permitted mining. Those are different answers,
and in some jurisdictions the difference is the whole question. A boolean would
have manufactured consent.

**`noindex` is not an AI opt-out.** It is a statement about search engines.
Treating it as an opt-out would put words in the source's mouth, so it is recorded
and excluded from the restrictive test.

**Permitted and restrictive are independent.** A crawl can be entirely within
robots.txt on a site that blocks every AI crawler it has heard of.
`robots_allowed` records what governed *this* crawl; `ai_directives` records what
the site said to everyone. Conflating them loses information in both directions.

**No robots.txt is not an empty one.** Both permit the crawl. Only one is a
statement, and `robots_present` keeps them apart.

## What is collected

| Signal | Source |
|---|---|
| `noindex`, `nofollow`, `noai`, `noimageai` | `<meta name="robots">`, `X-Robots-Tag` |
| TDM reservation and policy | `<meta name="tdm-reservation">`, `TDM-Reservation` header |
| Licence | `<link rel="license">`, `<meta name="license">`, `Link` header |
| Canonical URL | `<link rel="canonical">` |
| AI directives | robots.txt groups naming AI crawlers |

Where a header and the page disagree, the more restrictive reading wins. The cost
of wrongly including a page that asked to be left out is not symmetric with the
cost of wrongly excluding one.

## robots.txt is parsed twice

The engine parses robots.txt to decide whether *this crawler* may fetch *this
URL*, and correctly discards every group aimed at somebody else. Provenance needs
exactly what that discards: a site blocking GPTBot and CCBot while leaving us free
has expressed the most relevant fact about a corpus built from it.

Widening the enforcement parser would put a reporting concern inside the code path
that decides whether a fetch is permitted, which is the last place to add
incidental complexity. Instead the duplication carries a guard:
`TestReportAgreesWithEnforcement` drives the real `RobotsManager` and the report
over the same files and asserts they see the same groups.

## Limits

- **JSONL, not Parquet.** Parquet is the right final format and is on the
  [roadmap](../ROADMAP.md); it brings a schema definition and a column-type
  mapping to get wrong, and blocking provenance on that would be backwards. JSONL
  loads into `datasets`, DuckDB, and Polars today, and converting later is a
  read-and-rewrite rather than a re-crawl.
- **No language detection.** `language` comes from `<html lang>` or
  `Content-Language`, both of which are frequently a template default nobody
  updated. It is what the page claimed, not what the text is.
- **Licence detection is a signal, not a determination.** A `rel="license"` link
  is evidence; it is not a legal conclusion, and no attempt is made to normalise
  licence identifiers.
- **AI agent names date.** An agent missing from the known list still appears in
  the parsed groups, so the record stays complete — only the summary line needs
  the list to be current.
- **Records are streamed, not ordered.** Unlike the extracted-item output there is
  no `deterministic_order` for the corpus: records are keyed by content hash, which
  is order-independent, and a corpus is the largest thing the crawler produces.
