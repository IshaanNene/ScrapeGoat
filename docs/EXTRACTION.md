# Content extraction

How ScrapeGoat finds the main content of a page, how well it does, and — first —
what these numbers are not.

## Read this before the table

**The corpus is synthetic, and it was iterated on while the extractor was being
written.** That is the setup that produces optimistic numbers, and it produced
some here. Two specific reasons to discount them:

1. **Composed pages are tidier than the real web.** The corpus contains the
   structures somebody thought to include. It cannot tell you how the extractor
   behaves on markup nobody would write on purpose, which is most of the web.
2. **The corpus and the extractor were developed together.** When the first run
   scored 1.000 on every tier, the response was to make the corpus harder — which
   is the right response, but it means the corpus is shaped by knowing what the
   extractor does. An independent corpus would score lower, and the only honest
   question is by how much.

So: **0.998 measures that this handles the cases in `corpus_test.go`.** It is not
a claim about real-world accuracy. Real-page evaluation against trafilatura and
resiliparse is the outstanding work, and it is listed as such rather than quietly
skipped.

What the table *is* good for is comparison. All four extractors face the same
corpus, so the ordering between them is meaningful even where the absolute numbers
are inflated.

## Reproducing

```bash
go test ./internal/extract -run TestExtractionBenchmark -v
```

The corpus is generated from a fixed seed, so these numbers come out the same on
any machine. Every figure below is from that command and nowhere else.

## Method

**Metric: token multiset F1.** Extraction is judged on the text produced, not the
DOM nodes chosen — two extractors that select different nodes but yield the same
prose are equally good. Tokens are lowercased and stripped of punctuation, then
compared as a multiset so that emitting the nav bar forty times scores worse than
emitting it once.

Precision, recall, and F1 are all reported because each failure looks different:

| | |
|---|---|
| low precision, high recall | got the article plus the boilerplate |
| high precision, low recall | got a fragment, or missed the article |
| both low | selected the wrong block entirely |

An extractor returning the whole page body gets **perfect recall**. Reporting F1
alone would flatter it.

**Macro-averaged** — every document counts equally regardless of length, so one
tier failing completely cannot be hidden by three long pages succeeding.

**Corpus: 16 documents, 8 tiers.** Tiers 1–4 vary the markup idiom for the same
article. Tiers 5–8 are the hard cases, each chosen to defeat a specific heuristic:

| Tier | Shape | Defeats |
|---|---|---|
| 1 | `<main><article>` | — |
| 2 | `div.post > div.entry-content` | — |
| 3 | anonymous `<div>`s | class-name matching |
| 4 | `div.sidebar-widget > div.ad-container` | class-name matching, harder |
| 5 | comments longer than the article | "take the biggest block of prose" |
| 6 | one-sentence article, heavy chrome | any absolute length threshold |
| 7 | article split by inline ads | "return the single best node" |
| 8 | link directory, no article | "always return something" |

## Results

### Headline

| Extractor | Precision | Recall | **F1** |
|---|---:|---:|---:|
| whole body (no extraction) | 0.394 | 0.938 | 0.537 |
| all `<p>` tags | 0.497 | 0.896 | 0.621 |
| **selector list — what v0.1.0 shipped** | 0.438 | 0.438 | **0.438** |
| **density scoring — this package** | 0.996 | 1.000 | **0.998** |

**The shipped extractor scored below returning the entire page body.** That is the
finding that motivated this package, and it is not a close call: 0.438 against
0.537 for doing nothing at all, and 0.621 for the one-line alternative of
concatenating every `<p>`.

### Why the selector list failed

Its per-tier row is the whole story:

| Tier | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| F1 | 1.000 | 1.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 1.000 |

Not degradation — a cliff. It is **perfect** when a page happens to use
`<article>` or `.entry-content`, and returns **literally nothing** otherwise, with
no signal to the caller about which case they are in. A crawl of a site it does not
recognise produces empty results that look exactly like a site with no content.

Its 1.000 on tier 8 is an accident worth naming: it scores perfectly on the page
with no article because it returns nothing, which happens to be correct. It is
right there for the wrong reason.

### Where density scoring still loses

| Tier | F1 | |
|---|---:|---|
| 7 — article split by ads | 0.970 | includes the advertisement text |

The sibling-merging rule that recovers an article split across containers has no
way to tell an inline ad block from a paragraph block: both are prose-shaped
children of the same parent. Rejecting it would need either a text-quality signal
or a much stronger notion of what an ad looks like, and the current trade — a
little boilerplate in, rather than a third of the article out — is the right one
for a corpus builder.

## How it works

Four signals, in order of how much they contribute:

**Text density.** Each paragraph-like node scores by length and punctuation
count — commas and full stops standing in for "this is a written sentence rather
than a label". Score accrues to the node's parent, and at half weight to its
grandparent, so the container holding the most prose wins.

**Link density.** A node's score is multiplied by `1 - linkDensity`. A menu is
almost entirely anchor text; an article contains links but is not made of them.

**Uniformity.** Containers whose children are many, short, and similar in length
are penalised. This is the only signal that separates a comment thread from an
article: comments are prose, so link density does not help, and there are many of
them, so raw accumulated score favours them. Article paragraphs vary in length;
comments cluster.

**The main heading.** A candidate containing the page's `<h1>` is boosted heavily.
This is what rescues tier 6 — a one-sentence article that every length-based signal
loses to a long comment thread. Relying on `<h1>` is reading the document's own
structure rather than guessing at class names, which is the distinction that
separates this from the selector list it replaces.

### Refusing to answer

The extractor returns empty when link density exceeds 0.5 or the winning block's
share of total score falls below 0.10.

Plenty of pages have no article — tag indexes, link directories, pagination stubs.
Returning the best-scoring block anyway means a corpus quietly fills with
navigation, and nothing downstream can distinguish that from real content.
`Result.Confidence` and `Result.LinkDensity` are exported for the same reason: a
downstream filter needs to know when the extractor was unsure. The selector-based
version had no way to express doubt.

## Outstanding

- **Real-page evaluation.** The gap between this corpus and the web is the single
  largest uncertainty on this page. Needs a hand-annotated set, or an existing
  benchmark whose licence permits use.
- **Comparison against trafilatura and resiliparse.** The credible reference
  points, both Python. Requires a cross-language harness.
- **Language coverage.** The corpus is English. Punctuation-density scoring is
  weaker for languages that punctuate differently, and untested for scripts
  without spaces.
- **Tier 7's ad text**, described above.
