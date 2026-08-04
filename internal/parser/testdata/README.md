# Parser test corpus

`pages/` holds HTML documents; `golden/` holds the parser's expected output for
each, one JSON file per page.

## Why this exists

The parser was previously tested only against inline string literals in Go source
— that is, against well-formed HTML written by the same person who wrote the
parser. Real pages are not well-formed, and handling malformation *is* the problem
domain. A test suite that never sees a broken document cannot tell you whether the
parser handles broken documents.

Each page here targets a specific hazard rather than being a random capture:

| File | What it exercises |
|---|---|
| `01_well_formed_article.html` | The happy path: title, meta, canonical, OpenGraph, headings, links |
| `02_unclosed_tags.html` | Unclosed `<p>`, `<li>`, `<div>`, `<title>` — extremely common in the wild |
| `03_attribute_soup.html` | Unquoted, mixed-quoted, uppercase, empty, and whitespace-padded attributes |
| `04_json_ld_product.html` | Valid JSON-LD with nested offer and rating objects |
| `05_broken_json_ld.html` | Truncated, non-JSON, and empty JSON-LD blocks in valid HTML |
| `06_base_tag_relative_links.html` | `<base href>` changing relative link resolution; fragment- and query-only hrefs |
| `07_dangerous_hrefs.html` | `javascript:`, `data:`, `file:`, and the cloud metadata endpoint |
| `08_entities_and_encoding.html` | Named, numeric, and invalid entities; bare `&`; non-ASCII and emoji |
| `09_table_and_lists.html` | Tables with and without headers, and a ragged `colspan` row |
| `10_empty_and_minimal.html` | An essentially empty document |
| `11_deeply_nested.html` | 20 levels of nesting, probing recursion limits |
| `12_duplicate_and_missing_meta.html` | Duplicate `<title>`, duplicate `<h1>`, images with missing and empty `alt` |

## Golden files

Golden files record what the parser currently produces. They are a change detector,
not a specification: a diff means behaviour changed, and you decide whether the
change is a fix or a regression. Several of them encode behaviour that is arguably
wrong — that is deliberate, because pinning current behaviour is what makes an
unintended change visible.

## What this corpus has already caught

- **`<base href>` was ignored.** `06_base_tag_relative_links.html` showed every
  relative link resolving against the document URL instead of the base, so a site
  serving content from a CDN path would have been crawled entirely at the wrong
  host — silently, as a crawl that finds nothing rather than an error. Fixed in
  `CSSParser.extractLinks`, with focused cases in `TestBaseTagResolution`.

Two behaviours the corpus records that look wrong but are not:

- **An unclosed `<title>` swallows the rest of the document** (`02_unclosed_tags`).
  `<title>` is RCDATA, so everything up to a `</title>` that never arrives is the
  title. Browsers do the same thing.
- **Attribute rules return raw values, link discovery returns absolute URLs**
  (`12_duplicate_and_missing_meta` has `images: ["/a.png", ...]`). Extraction rules
  hand back what the attribute said; only link discovery resolves.

## Regenerating

Regenerate after an intentional change:

```bash
go test ./internal/parser -run TestGolden -update
```

Then **read the diff**. A golden update that is not reviewed is worse than no
golden file at all: it converts every regression into a silently accepted new
baseline.
