# The cost of keeping a corpus fresh

Numbers, methodology, and how to reproduce them.

Conditional requests were argued for on a claim about other people's traffic:
Cloudflare reports that over half of AI-crawler requests re-fetch pages that have
not changed. That is a reason to build the feature, not a measurement of it. This
page is the measurement.

## Environment

| | |
|---|---|
| Target | `books.toscrape.com` — static nginx, a purpose-built scraping sandbox |
| Date | 2026-09-01 |
| Client | ScrapeGoat at `044f85c`, one request per second, concurrency 2 |
| Network | Domestic broadband, single machine |

## What was measured

Two crawls of the same 50 pages, back to back. The first had nothing to compare
against; the second was given the first one's corpus.

```bash
# Build a corpus.
scrapegoat crawl https://books.toscrape.com/ \
  --depth 3 --max-requests 50 --concurrency 2 --delay 1s \
  --allowed-domains books.toscrape.com --output ./first

# Refresh it.
scrapegoat crawl https://books.toscrape.com/ \
  --depth 3 --max-requests 60 --concurrency 2 --delay 1s \
  --allowed-domains books.toscrape.com --output ./second \
  --since ./first/corpus.jsonl
```

| | Full crawl | Refresh with `--since` |
|---|---|---|
| Requests | 51 | 50 |
| Response bodies | 1,175,873 B | **0 B** |
| Pages confirmed unchanged | — | 50 of 50 |
| Wall clock | 49.3 s | 49.8 s |

Response header sizes are not something the crawler counts, so they were measured
separately with `curl` against one page of the same site:

| | Response headers | Body | Total |
|---|---|---|---|
| `200 OK` | 256 B | 9,279 B | 9,535 B |
| `304 Not Modified` | 187 B | 0 B | **187 B** |

Putting those together over the 50-page corpus:

| | Bytes | Per page |
|---|---|---|
| Full refresh | 1,188,673 B | 23,773 B |
| Conditional refresh | 9,350 B | 187 B |
| **Reduction** | **127×** | **99.21% fewer bytes** |

## Extrapolated: 100,000 pages, refreshed daily for thirty days

Three million page-checks. The saving depends entirely on how much of the corpus
actually changes, so it is given as a function of that rather than as one number.

| Daily change rate | Bytes transferred | vs unconditional |
|---|---|---|
| 0% (fully static) | 0.56 GB | 127.1× |
| 1% | 1.27 GB | 56.0× |
| 2% | 1.99 GB | 35.9× |
| 5% | 4.13 GB | 17.3× |
| 10% | 7.69 GB | 9.3× |
| 100% (nothing cacheable) | 71.88 GB | 1.0× |

Unconditionally, the same thirty days cost **71.88 GB** regardless.

## What this does not say

**The request count is unchanged.** Three million checks are three million
requests either way. Conditional requests reduce bytes and downstream work, not
traffic. If the binding constraint is a rate limit, a politeness delay, or a
server's opinion about how often it wants to hear from you, this feature does not
help at all.

**Wall clock was unchanged**, for the same reason: 49.3 s against 49.8 s, both
governed by the one-second politeness delay rather than by transfer time. The
saving shows up as bandwidth and as work not done — no body to decompress, no DOM
to build, no extraction to run — not as a faster crawl.

**This is a best case, and deliberately so.** Every one of the 50 pages carried
both an `ETag` and a `Last-Modified`, and none of them changed between the two
crawls. A real corpus contains pages whose servers issue no validator at all;
those cost a full fetch however often you ask, and `--since` prints how many of
them there are before the crawl starts for exactly that reason.

**One site, one server.** `books.toscrape.com` is static nginx. A CMS that
regenerates a page on every request, or one that issues a new `ETag` for an
unchanged page, will produce a worse ratio, and the failure would be invisible
except as bytes that did not fall.

**The per-page average is unrepresentative on purpose.** 23,773 B/page mixes
listing pages of ~50 KB with product pages of ~9 KB. The single-page `curl`
measurement is given above so the ratio can be recomputed against whatever page
size a different corpus actually has.

## Reproducing it

The two crawl commands above, then:

```bash
# Header sizes for a 200 and a 304 on the same URL.
U=https://books.toscrape.com/catalogue/a-light-in-the-attic_1000/index.html
curl -s -o /dev/null -w 'status=%{http_code} hdr=%{size_header} body=%{size_download}\n' "$U"
ETAG=$(curl -s -o /dev/null -D - "$U" | grep -i '^etag:' | tr -d '\r' | cut -d' ' -f2-)
curl -s -o /dev/null -H "If-None-Match: $ETAG" \
  -w 'status=%{http_code} hdr=%{size_header} body=%{size_download}\n' "$U"
```

Be polite about it: the numbers above came from 101 requests at one per second.
