# ScrapeGoat 🕷️

**Production-ready web scraping and crawling toolkit written in Go.**

ScrapeGoat combines ideas from Scrapy, Colly, and modern crawler services into a single, high-performance platform with clean interfaces, concurrent workers, and a pluggable middleware pipeline.

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Tests](https://img.shields.io/badge/tests-23%2F23%20pass-brightgreen.svg)](#testing)

---

## Features

| Feature | Details |
|---------|---------|
|  **High-Performance Crawling** | Concurrent workers, per-domain throttling |
|  **CSS, XPath & Regex Extraction** | goquery CSS selectors, XPath, named regex groups |
|  **Smart URL Management** | Priority queue, deduplication, domain filters |
|  **robots.txt Compliance** | Automatic parsing and crawl-delay support |
|  **Multi-Format Export** | JSON, JSONL, CSV with streaming writes |
|  **Search Engine Mode** | Full-text index with headings, meta, link graph |
|  **AI-Powered Crawling** | Summarize, NER, sentiment via Ollama/OpenAI |
|  **Prometheus Metrics** | Built-in `/metrics` & `/health` endpoints |
|  **Proxy Rotation** | Round-robin / random with health checking |
|  **Checkpoint Persistence** | Pause/resume crawls with atomic state |
|  **Graceful Shutdown** | SIGINT/SIGTERM handling with state preservation |

---

## Architecture

```mermaid
graph LR
    CLI-->Engine
    Engine-->Scheduler
    Scheduler-->Workers
    Workers-->Fetcher
    Fetcher-->Parser
    Parser-->Pipeline
    Pipeline-->Storage
    Parser-.->|"new URLs"|Frontier
    Frontier-.->Workers

    style Engine fill:#E67E22,color:#fff
    style Workers fill:#E67E22,color:#fff
    style Fetcher fill:#9B59B6,color:#fff
    style Parser fill:#1ABC9C,color:#fff
    style Pipeline fill:#E74C3C,color:#fff
    style Storage fill:#3498DB,color:#fff
```

Every major component is behind a Go interface — swap in custom fetchers, parsers, storage backends, or pipeline middleware.

→ **[Full architecture documentation](docs/architecture.md)**

---

## Quick Start

### Install

```bash
git clone https://github.com/IshaanNene/ScrapeGoat
cd ScrapeGoat
make build
```

### Basic Crawl

```bash
# Crawl a website (depth 2, JSON output)
./bin/scrapegoat crawl https://quotes.toscrape.com --depth 2

# Limit pages (great for testing)
./bin/scrapegoat crawl https://news.ycombinator.com --depth 1 --max-requests 30

# Single page (depth 0 = just the seed URL)
./bin/scrapegoat crawl https://en.wikipedia.org/wiki/Web_scraping --depth 0

# Multiple seed URLs
./bin/scrapegoat crawl https://quotes.toscrape.com https://books.toscrape.com --depth 1

# High concurrency, JSONL output
./bin/scrapegoat crawl https://quotes.toscrape.com \
  --depth 2 \
  --concurrency 20 \
  --delay 200ms \
  --format jsonl \
  --output ./results

# CSV output
./bin/scrapegoat crawl https://books.toscrape.com --depth 0 --format csv

# Custom User-Agent
./bin/scrapegoat crawl https://example.com --user-agent "MyBot/1.0 (+https://mybot.example.com)"

# Verbose logging (see every request)
./bin/scrapegoat crawl https://quotes.toscrape.com --depth 1 --verbose

# Show current configuration
./bin/scrapegoat config
```

### `crawl` Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--depth` | `-d` | `3` | Maximum crawl depth |
| `--concurrency` | `-n` | `10` | Concurrent workers |
| `--delay` | | `1s` | Politeness delay per domain |
| `--format` | `-f` | `json` | Output format: `json`, `jsonl`, `csv` |
| `--output` | `-o` | `./output` | Output directory |
| `--max-requests` | `-m` | `0` (unlimited) | Maximum total requests |
| `--max-retries` | | `3` | Retries per failed request (`0` = fail fast) |
| `--allowed-domains` | | (all) | Comma-separated domains to stay within | 
| `--user-agent` | | (built-in) | Custom User-Agent string |
| `--config` | `-c` | | YAML config file path |
| `--verbose` | `-v` | | Enable debug logging |

> [!TIP]
> Use `--allowed-domains` when crawling Wikipedia, GitHub, or any site linked to many external pages. Without it, the crawler follows every outbound link.

```bash
# Stay locked to English Wikipedia only
./bin/scrapegoat crawl https://en.wikipedia.org/wiki/Web_scraping \
  --depth 1 --max-requests 30 --allowed-domains en.wikipedia.org --delay 300ms
```

---

### Search Engine Mode

Index a website with full-text, headings, meta, and link graph:

```bash
# Index a website (JSONL output, one document per page)
./bin/scrapegoat search https://go.dev

# Custom depth and page limit
./bin/scrapegoat search https://en.wikipedia.org/wiki/Artificial_intelligence \
  --depth 2 \
  --max-pages 100 \
  --output ./wiki_index

# Index multiple domains
./bin/scrapegoat search https://docs.python.org https://docs.rust-lang.org
```

Each indexed document contains: `url`, `title`, `description`, `keywords`, `canonical`, `language`, `h1/h2/h3`, `body_text`, `word_count`, `outbound_links`, `images`, `content_hash`, `indexed_at`.

`search` Flags: `--depth` (`-d`, default 3), `--concurrency` (`-n`, default 10), `--delay` (default 200ms), `--max-pages` (default 500), `--allowed-domains` (comma-separated, optional), `--output` (`-o`, default `./output/search_index`).

---

### AI-Powered Crawling

Crawl + summarize + entity extraction + sentiment analysis (requires an LLM):

```bash
# With Ollama (local, no API key needed)
ollama serve &
ollama pull llama3.2
./bin/scrapegoat ai-crawl https://news.ycombinator.com

# With OpenAI
OPENAI_API_KEY=sk-... ./bin/scrapegoat ai-crawl https://techcrunch.com --llm openai --model gpt-4o-mini

# Custom endpoint (any OpenAI-compatible API)
./bin/scrapegoat ai-crawl https://example.com \
  --llm custom \
  --llm-endpoint http://localhost:8080 \
  --model mistral
```

Each item gets: `summary` (200-word summary), `entities` (people, orgs, locations), `sentiment` (positive/negative/neutral/mixed).

`ai-crawl` Flags: `--depth` (`-d`, default 2), `--concurrency` (`-n`, default 5), `--delay` (default 500ms), `--max-pages` (default 50), `--llm` (`ollama`/`openai`/`custom`), `--model`, `--llm-endpoint`, `--output` (`-o`).

---

## Library (Go SDK)

Embed ScrapeGoat directly in your Go application:

```go
package main

import (
    "fmt"
    "strings"
    "time"

    scrapegoat "github.com/IshaanNene/ScrapeGoat/pkg/scrapegoat"
)

func main() {
    crawler := scrapegoat.NewCrawler(
        scrapegoat.WithConcurrency(5),
        scrapegoat.WithMaxDepth(2),
        scrapegoat.WithDelay(500 * time.Millisecond),
        scrapegoat.WithOutput("json", "./output"),
        scrapegoat.WithAllowedDomains("quotes.toscrape.com"),
        scrapegoat.WithMaxRequests(100),
    )

    // Follow pagination links
    crawler.OnHTML("li.next a[href]", func(e *scrapegoat.Element) {
        e.Follow(e.Attr("href"))
    })

    // Extract quotes
    crawler.OnHTML(".quote", func(e *scrapegoat.Element) {
        text := strings.TrimSpace(e.Selection.Find(".text").Text())
        author := strings.TrimSpace(e.Selection.Find(".author").Text())
        if text != "" {
            e.Item.Set("quote", text)
            e.Item.Set("author", author)
        }
    })

    if err := crawler.Start("https://quotes.toscrape.com"); err != nil {
        panic(err)
    }
    crawler.Wait()

    fmt.Println("Stats:", crawler.Stats())
}
```

### SDK Options

```go
scrapegoat.WithConcurrency(10)               // parallel workers
scrapegoat.WithMaxDepth(3)                   // crawl depth
scrapegoat.WithDelay(500 * time.Millisecond) // politeness delay
scrapegoat.WithOutput("jsonl", "./out")      // format + path
scrapegoat.WithAllowedDomains("example.com") // domain filter
scrapegoat.WithProxy("http://p1:8080", "http://p2:8080") // proxy rotation
scrapegoat.WithRobotsRespect(true)           // obey robots.txt
scrapegoat.WithMaxRequests(500)              // request cap
scrapegoat.WithUserAgent("MyBot/1.0")        // custom UA
scrapegoat.WithVerbose()                     // debug logging
```

---

## Ready-Made Examples

Run these directly — no configuration needed:

```bash
# Hacker News top stories (rank, title, URL, points, author, comments)
go run ./examples/hackernews/

# E-commerce product scraper (books.toscrape.com → title, price, rating, stock)
go run ./examples/ecommerce/

# GitHub trending repos (name, description, language, stars, forks)
go run ./examples/github/

# Wikipedia knowledge extractor (title, summary, categories, references)
go run ./examples/wikipedia/

# Multi-site news aggregator (HN + Techmeme + dev.to → headlines, previews)
go run ./examples/news/

# Search engine indexer (provide your own URLs)
go run ./examples/search/ https://go.dev
go run ./examples/search/ https://en.wikipedia.org/wiki/Web_scraping

# AI crawler (requires Ollama or OpenAI)
go run ./examples/aicrawl/ https://news.ycombinator.com
go run ./examples/aicrawl/ https://en.wikipedia.org/wiki/Artificial_intelligence
```

---

## YAML Configuration

```yaml
# configs/default.yaml
engine:
  concurrency: 10
  max_depth: 5
  request_timeout: 30s
  politeness_delay: 1s
  respect_robots_txt: true
  max_retries: 3

fetcher:
  type: http
  follow_redirects: true
  max_body_size: 10485760  # 10MB

storage:
  type: json            # json | jsonl | csv
  output_path: ./output

proxy:
  enabled: false
  rotation: round_robin # round_robin | random
  urls: []

metrics:
  enabled: false
  port: 9090
  path: /metrics

parser:
  rules:
    - name: title
      selector: "h1"
      type: css
    - name: emails
      pattern: '[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}'
      type: regex
```

Use with: `./bin/scrapegoat crawl https://example.com --config configs/default.yaml`

---

## Testing

```bash
# Unit tests (fast, no network)
bash scripts/test.sh unit

# All benchmarks
bash scripts/test.sh bench

# Live integration tests (requires network)
bash scripts/test.sh integration

# Live crawl examples
bash scripts/test.sh crawl

# Full suite
bash scripts/test.sh all
```

### Test Results

| Suite | Tests | Status |
|-------|-------|--------|
| Engine (frontier, dedup, stats) | 7 | ✅ PASS |
| Parser (CSS, XPath, regex, structured) | 8 | ✅ PASS |
| Pipeline (trim, sanitize, PII, dates, dedup) | 9 | ✅ PASS |
| **Total** | **23** | **✅ 23/23 PASS** |

### Benchmarks (Apple M4 Pro)

| Component | Ops/sec | Latency |
|-----------|---------|---------|
| Frontier Push/Pop | 7.2M | 543 ns |
| Dedup Lookup | 13.9M | 255 ns |
| Pipeline (3 stages) | 2.6M | 1.4 µs |
| CSS Parse | 1.4M | 2.5 µs |
| Composite Parser | 54K | 66 µs |

---

## Prometheus Metrics

Enable via config or env:

```bash
# With metrics server
SCRAPEGOAT_METRICS_ENABLED=true ./bin/scrapegoat crawl https://example.com

# Query while crawling
curl http://localhost:9090/health
curl http://localhost:9090/metrics
```

---

## Docker

```bash
# Build image
make docker-build

# Start with Docker Compose (for Redis/monitoring)
make docker-up
```

---

## Project Structure

```
scrapegoat/
├── bin/scrapegoat           # Compiled binary
├── cmd/scrapegoat/          # CLI (crawl, search, ai-crawl, version, config)
├── pkg/scrapegoat/          # Public SDK for embedding
├── internal/
│   ├── engine/            # Scheduler, frontier, dedup, checkpoint, robots
│   ├── fetcher/           # HTTP fetcher, proxy rotation
│   ├── parser/            # CSS / XPath / regex / structured data (JSON-LD, OG)
│   ├── pipeline/          # Middleware: trim, sanitize, PII redact, date/currency normalize
│   ├── storage/           # JSON, JSONL, CSV writers
│   ├── ai/                # LLM client, summarizer, NER, sentiment
│   ├── config/            # YAML + env config loading
│   └── observability/     # Prometheus metrics
├── examples/
│   ├── hackernews/        # HN top stories
│   ├── ecommerce/         # books.toscrape.com products
│   ├── github/            # GitHub trending repos
│   ├── wikipedia/         # Wikipedia deep crawl
│   ├── news/              # Multi-site news aggregator
│   ├── search/            # Search engine indexer
│   └── aicrawl/           # AI-powered crawler
├── tests/                 # Integration tests
├── configs/               # Default YAML config
└── scripts/test.sh        # Test runner
```

---

## Development

```bash
make build      # Build binary
make test       # Run all tests
make lint       # Run golangci-lint
make clean      # Clean build artifacts
make deps       # Download and tidy modules
make docker-up  # Start dev services
```

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, code style, testing, and PR guidelines.

---

## License

MIT
