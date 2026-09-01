# Software Requirements Specification (SRS)

## ScrapeGoat — Next-Generation Web Scraping & Crawling Toolkit

> **Historical document.** This records the requirements the project was designed
> against, not what it ships today. Several requirements here were implemented and
> later removed — the AI-processing features in F4 in particular: `internal/ai/` now
> lives in `examples/aicrawl/`, and `internal/llmextract/` has been deleted. AI is a
> derivation tier, not a pipeline stage. For what actually runs, read the feature
> table in [README.md](README.md); for what is planned, [ROADMAP.md](ROADMAP.md).

| Field          | Value                                              |
|----------------|-----------------------------------------------------|
| **Version**    | 1.0                                                 |
| **Date**       | 2026-03-01                                          |
| **Author**     | Ishaan Nene                                         |
| **Repository** | https://github.com/IshaanNene/ScrapeGoat            |
| **Language**   | Go 1.24+                                            |
| **License**    | MIT                                                 |

---

## Table of Contents

1. [Introduction](#1-introduction)
2. [Overall Description](#2-overall-description)
3. [System Features](#3-system-features)
4. [External Interface Requirements](#4-external-interface-requirements)
5. [Non-Functional Requirements](#5-non-functional-requirements)
6. [Data Requirements](#6-data-requirements)
7. [Appendices](#7-appendices)

---

## 1. Introduction

### 1.1 Purpose

This document defines the software requirements for **ScrapeGoat**, an enterprise-grade web scraping and crawling toolkit written in Go. It serves as the authoritative reference for the system's capabilities, constraints, interfaces, and quality attributes.

### 1.2 Scope

ScrapeGoat provides:

- A **CLI tool** for crawling, indexing, and AI-powered content extraction from websites.
- A **Go SDK** for embedding scraping capabilities into third-party applications.
- A **modular internal architecture** with pluggable fetchers, parsers, pipelines, storage backends, and AI integrations.

ScrapeGoat is designed for developers, data engineers, SEO analysts, and researchers who need reliable, configurable, and scalable web data extraction.

### 1.3 Definitions, Acronyms, and Abbreviations

| Term              | Definition                                                       |
|-------------------|------------------------------------------------------------------|
| **Crawl**         | Recursively follow links from seed URLs to discover pages        |
| **Scrape**        | Extract structured data from downloaded web pages                |
| **Seed URL**      | The starting URL(s) from which crawling begins                   |
| **Frontier**      | Priority queue of URLs waiting to be fetched                     |
| **Deduplication** | Ensuring each URL is fetched at most once per crawl              |
| **Pipeline**      | Chain of middleware processors that transform scraped items      |
| **robots.txt**    | Standard file that specifies which pages crawlers may access     |
| **NER**           | Named Entity Recognition — extracting people, places, orgs      |
| **LLM**           | Large Language Model — AI model for text understanding           |
| **Checkpoint**    | Persisted crawl state enabling pause/resume                      |
| **JSONL**         | JSON Lines — one JSON object per line, streamable format         |

### 1.4 References

- [RFC 9309 — Robots Exclusion Protocol](https://www.rfc-editor.org/rfc/rfc9309)
- [Prometheus Exposition Format](https://prometheus.io/docs/instrumenting/exposition_formats/)
- [Cobra CLI Framework](https://github.com/spf13/cobra)
- [goquery (CSS Selectors)](https://github.com/PuerkitoBio/goquery)
- [htmlquery (XPath)](https://github.com/antchfx/htmlquery)

---

## 2. Overall Description

### 2.1 Product Perspective

ScrapeGoat is a **standalone, self-contained toolkit** — it does not depend on external databases, message queues, or cloud services for its core functionality. Optional integrations include:

- **Ollama / OpenAI** for AI-powered content processing.
- **Prometheus** for metrics export.
- **Docker** for containerized deployment.
- **Proxy servers** for IP rotation.

### 2.2 Product Functions (High Level)

| # | Function                    | Description                                                   |
|---|-----------------------------|---------------------------------------------------------------|
| F1 | Web Crawling               | Concurrent, depth-limited crawling with link discovery        |
| F2 | Data Extraction            | CSS, XPath, and regex-based structured data extraction        |
| F3 | Search Engine Indexing      | Full-text indexing with headings, meta, and link graph        |
| F4 | AI-Powered Processing      | LLM-based summarization, NER, and sentiment analysis         |
| F5 | Multi-Format Export        | JSON, JSONL, and CSV output with streaming writes             |
| F6 | Proxy Rotation             | Round-robin / random proxy with health checking               |
| F7 | Checkpoint Persistence     | Atomic pause/resume of crawl state                            |
| F8 | Observability              | Prometheus metrics and health endpoints                       |
| F9 | Go SDK                     | Embeddable library with functional options pattern             |
| F10 | Configuration System      | YAML config + CLI flags + environment variable overrides       |

### 2.3 User Characteristics

| User Type          | Technical Level | Primary Use Case                                 |
|--------------------|-----------------|--------------------------------------------------|
| **Developer**      | High            | Embed SDK in Go applications, custom parse rules |
| **Data Engineer**  | High            | Large-scale data extraction pipelines            |
| **SEO Analyst**    | Medium          | Site auditing, content indexing                  |
| **Researcher**     | Medium          | Academic data collection, corpus building        |
| **CLI Power User** | Medium          | Ad-hoc scraping via terminal commands            |

### 2.4 Constraints

| Constraint                   | Detail                                                        |
|------------------------------|---------------------------------------------------------------|
| **Language**                 | Go 1.24+ required                                             |
| **Platform**                 | Linux, macOS, Windows (amd64/arm64)                           |
| **Network**                  | Requires internet access for crawling                         |
| **AI Features**              | Require running Ollama instance or OpenAI API key             |
| **robots.txt**               | Respected by default — can be disabled via config             |
| **Legal**                    | Users are responsible for complying with target site ToS      |

### 2.5 Assumptions and Dependencies

- Target websites serve HTML content accessible via HTTP/HTTPS.
- Go toolchain is available on the build machine.
- For AI features: Ollama is running locally, or a valid OpenAI API key is provided.
- For Docker deployment: Docker and Docker Compose are installed.

---

## 3. System Features

### 3.1 Web Crawling Engine (F1)

**Description**: The core engine manages concurrent crawlers, URL frontier, deduplication, domain throttling, and lifecycle management.

#### 3.1.1 Functional Requirements

| ID       | Requirement                                                                         | Priority |
|----------|-------------------------------------------------------------------------------------|----------|
| FR-1.1   | Accept one or more seed URLs as input                                               | Must     |
| FR-1.2   | Crawl pages concurrently up to a configurable worker count (default: 10)            | Must     |
| FR-1.3   | Limit crawl depth to a configurable maximum (default: 3)                            | Must     |
| FR-1.4   | Enforce per-domain politeness delay (default: 1s)                                   | Must     |
| FR-1.5   | Deduplicate URLs to prevent re-fetching                                             | Must     |
| FR-1.6   | Support domain allowlist/denylist filtering                                         | Must     |
| FR-1.7   | Limit total requests to a configurable maximum (0 = unlimited)                      | Must     |
| FR-1.8   | Retry failed requests up to a configurable count (default: 3)                       | Must     |
| FR-1.9   | Handle graceful shutdown on SIGINT/SIGTERM with state preservation                  | Must     |
| FR-1.10  | Support URL priority queue (frontier) with depth-based ordering                     | Should   |
| FR-1.11  | Track per-domain statistics (requests, responses, errors, last fetch time)          | Should   |
| FR-1.12  | Report crawl statistics on completion (requests, items, errors, bytes, duration)     | Must     |

### 3.2 Data Extraction / Parsing (F2)

**Description**: Extract structured data from HTML responses using multiple parser strategies.

#### 3.2.1 Functional Requirements

| ID       | Requirement                                                                         | Priority |
|----------|-------------------------------------------------------------------------------------|----------|
| FR-2.1   | Extract data using CSS selectors (via goquery)                                      | Must     |
| FR-2.2   | Extract data using XPath expressions (via htmlquery)                                | Must     |
| FR-2.3   | Extract data using named regex groups                                               | Must     |
| FR-2.4   | Auto-extract structured data: JSON-LD, OpenGraph, Twitter Cards                     | Should   |
| FR-2.5   | Composite parser merges results from all parser types into a single item            | Must     |
| FR-2.6   | Discover and follow links found in HTML `<a>` tags                                  | Must     |
| FR-2.7   | Support configurable parse rules via YAML                                           | Must     |
| FR-2.8   | Auto-detect page structure when no rules are specified                              | Should   |

### 3.3 Search Engine Indexing (F3)

**Description**: Index websites with full-text content, headings hierarchy, metadata, and link graph for search/retrieval.

#### 3.3.1 Functional Requirements

| ID       | Requirement                                                                         | Priority |
|----------|-------------------------------------------------------------------------------------|----------|
| FR-3.1   | Extract: URL, title, description, keywords, canonical URL, language                 | Must     |
| FR-3.2   | Extract heading hierarchy (h1, h2, h3)                                              | Must     |
| FR-3.3   | Extract full body text with word count                                              | Must     |
| FR-3.4   | Extract outbound links and images                                                   | Must     |
| FR-3.5   | Generate content hash for change detection                                          | Should   |
| FR-3.6   | Timestamp each indexed document                                                     | Must     |
| FR-3.7   | Output as JSONL (one document per line)                                             | Must     |
| FR-3.8   | Configurable max pages (default: 500)                                               | Must     |

### 3.4 AI-Powered Crawling (F4)

**Description**: Enhance crawled data with LLM-powered summarization, named entity recognition, and sentiment analysis.

#### 3.4.1 Functional Requirements

| ID       | Requirement                                                                         | Priority |
|----------|-------------------------------------------------------------------------------------|----------|
| FR-4.1   | Support Ollama as a local LLM provider                                              | Must     |
| FR-4.2   | Support OpenAI as a cloud LLM provider                                              | Must     |
| FR-4.3   | Support custom OpenAI-compatible API endpoints                                      | Should   |
| FR-4.4   | Generate ~200-word summaries of page content                                        | Must     |
| FR-4.5   | Extract named entities: people, organizations, locations                            | Must     |
| FR-4.6   | Classify sentiment: positive, negative, neutral, mixed                              | Must     |
| FR-4.7   | Content filtering via LLM relevance evaluation                                      | Should   |
| FR-4.8   | Configurable AI model, temperature, and max tokens                                  | Should   |

### 3.5 Processing Pipeline (F5)

**Description**: Chain of middleware processors that transform, filter, and enrich scraped items before storage.

#### 3.5.1 Functional Requirements

| ID       | Requirement                                                                         | Priority |
|----------|-------------------------------------------------------------------------------------|----------|
| FR-5.1   | Whitespace trimming on all string fields                                            | Must     |
| FR-5.2   | HTML sanitization (strip tags from field values)                                    | Must     |
| FR-5.3   | PII redaction (emails, phone numbers)                                               | Should   |
| FR-5.4   | Field filtering (keep/drop specific fields)                                         | Must     |
| FR-5.5   | Field renaming (map old field names to new)                                         | Must     |
| FR-5.6   | Required field validation (drop items missing required fields)                      | Must     |
| FR-5.7   | Item deduplication (drop duplicates by configurable key)                            | Must     |
| FR-5.8   | Default value injection for missing fields                                          | Should   |
| FR-5.9   | Date and currency normalization                                                     | Should   |
| FR-5.10  | Extensible: custom middleware via the `Middleware` interface                         | Must     |

### 3.6 Multi-Format Storage (F6)

**Description**: Persist scraped data in multiple output formats.

#### 3.6.1 Functional Requirements

| ID       | Requirement                                                                         | Priority |
|----------|-------------------------------------------------------------------------------------|----------|
| FR-6.1   | JSON output (pretty-printed array)                                                  | Must     |
| FR-6.2   | JSONL output (one JSON object per line, streamable)                                 | Must     |
| FR-6.3   | CSV output with automatic header detection                                          | Must     |
| FR-6.4   | Configurable output directory                                                       | Must     |
| FR-6.5   | Batch writing for performance (configurable batch size, default: 100)               | Should   |

### 3.7 Proxy Rotation (F7)

**Description**: Route requests through rotating proxy servers to distribute load and avoid IP bans.

#### 3.7.1 Functional Requirements

| ID       | Requirement                                                                         | Priority |
|----------|-------------------------------------------------------------------------------------|----------|
| FR-7.1   | Round-robin proxy rotation                                                          | Must     |
| FR-7.2   | Random proxy rotation                                                               | Must     |
| FR-7.3   | Proxy health checking                                                               | Should   |
| FR-7.4   | Auto-rotate on request failure                                                      | Should   |
| FR-7.5   | Configurable proxy list via YAML                                                    | Must     |

### 3.8 Checkpoint Persistence (F8)

**Description**: Persist crawl state to disk at regular intervals, enabling pause and resume.

#### 3.8.1 Functional Requirements

| ID       | Requirement                                                                         | Priority |
|----------|-------------------------------------------------------------------------------------|----------|
| FR-8.1   | Auto-checkpoint at configurable intervals (default: 60s)                            | Must     |
| FR-8.2   | Checkpoint on graceful shutdown (SIGINT/SIGTERM)                                    | Must     |
| FR-8.3   | Resume from last checkpoint on restart                                              | Must     |
| FR-8.4   | Atomic checkpoint writes (no corruption on crash)                                   | Must     |

### 3.9 Observability (F9)

**Description**: Expose runtime metrics for monitoring crawl health and performance.

#### 3.9.1 Functional Requirements

| ID       | Requirement                                                                         | Priority |
|----------|-------------------------------------------------------------------------------------|----------|
| FR-9.1   | Prometheus-compatible `/metrics` endpoint                                           | Must     |
| FR-9.2   | `/health` endpoint returning server status                                          | Must     |
| FR-9.3   | Configurable metrics port (default: 9090)                                           | Must     |
| FR-9.4   | Track: requests sent/failed, items scraped/dropped, bytes downloaded, active workers| Must     |

### 3.10 Go SDK (F10)

**Description**: Public Go package enabling developers to embed ScrapeGoat in their own applications.

#### 3.10.1 Functional Requirements

| ID       | Requirement                                                                         | Priority |
|----------|-------------------------------------------------------------------------------------|----------|
| FR-10.1  | Functional options pattern for configuration (`WithConcurrency`, `WithMaxDepth`, etc)| Must    |
| FR-10.2  | `OnHTML` callback for CSS-based element matching                                    | Must     |
| FR-10.3  | `Start(url)` to begin crawling                                                     | Must     |
| FR-10.4  | `Wait()` to block until crawl completion                                            | Must     |
| FR-10.5  | `Stats()` to retrieve crawl statistics                                              | Must     |
| FR-10.6  | Element helpers: `Attr()`, `Text()`, `Follow()`, `Item.Set()`                       | Must     |

### 3.11 Configuration System (F11)

**Description**: Layered configuration with YAML files, CLI flags, and environment variable overrides.

#### 3.11.1 Functional Requirements

| ID       | Requirement                                                                         | Priority |
|----------|-------------------------------------------------------------------------------------|----------|
| FR-11.1  | Load configuration from YAML file                                                   | Must     |
| FR-11.2  | CLI flags override YAML values                                                      | Must     |
| FR-11.3  | Environment variables override YAML values (prefix: `SCRAPEGOAT_`)                  | Must     |
| FR-11.4  | Provide sensible defaults when no config is specified                                | Must     |
| FR-11.5  | Validate configuration and report clear errors                                      | Must     |
| FR-11.6  | `config` subcommand to display current effective configuration                      | Should   |

---

## 4. External Interface Requirements

### 4.1 Command-Line Interface

| Command      | Syntax                                         | Description                             |
|--------------|------------------------------------------------|-----------------------------------------|
| `crawl`      | `scrapegoat crawl <url> [flags]`               | Crawl and extract data from websites    |
| `search`     | `scrapegoat search <url> [flags]`              | Index website for search/retrieval      |
| `ai-crawl`   | `scrapegoat ai-crawl <url> [flags]`            | AI-powered crawl with summarization     |
| `config`     | `scrapegoat config`                            | Display current configuration           |
| `version`    | `scrapegoat version`                           | Print version information               |

#### CLI Flags (crawl)

| Flag                | Short | Default       | Type     |
|---------------------|-------|---------------|----------|
| `--depth`           | `-d`  | `3`           | int      |
| `--concurrency`     | `-n`  | `10`          | int      |
| `--delay`           |       | `1s`          | duration |
| `--format`          | `-f`  | `json`        | string   |
| `--output`          | `-o`  | `./output`    | string   |
| `--max-requests`    | `-m`  | `0`           | int      |
| `--max-retries`     |       | `3`           | int      |
| `--allowed-domains` |       | (all)         | string   |
| `--user-agent`      |       | (built-in)    | string   |
| `--config`          | `-c`  |               | string   |
| `--verbose`         | `-v`  | `false`       | bool     |

### 4.2 Go SDK API

```go
// Construction
crawler := scrapegoat.NewCrawler(opts ...Option)

// Options
scrapegoat.WithConcurrency(n int)
scrapegoat.WithMaxDepth(d int)
scrapegoat.WithDelay(d time.Duration)
scrapegoat.WithOutput(format, path string)
scrapegoat.WithAllowedDomains(domains ...string)
scrapegoat.WithProxy(urls ...string)
scrapegoat.WithRobotsRespect(bool)
scrapegoat.WithMaxRequests(n int)
scrapegoat.WithUserAgent(ua string)
scrapegoat.WithVerbose()

// Callbacks
crawler.OnHTML(selector string, fn func(*Element))

// Lifecycle
crawler.Start(url string) error
crawler.Wait()
crawler.Stats() Stats
```

### 4.3 Configuration File (YAML)

Top-level keys: `engine`, `fetcher`, `proxy`, `parser`, `pipeline`, `storage`, `ai`, `logging`, `metrics`.

### 4.4 HTTP Endpoints (Metrics Server)

| Endpoint    | Method | Response                          |
|-------------|--------|-----------------------------------|
| `/health`   | GET    | `200 OK` with status JSON         |
| `/metrics`  | GET    | Prometheus text exposition format  |

---

## 5. Non-Functional Requirements

### 5.1 Performance

| Metric                    | Target                                          |
|---------------------------|-------------------------------------------------|
| Frontier push/pop         | ≥ 7M ops/sec                                    |
| Dedup lookup              | ≥ 13M ops/sec                                   |
| Pipeline (3 stages)       | ≥ 2.5M ops/sec                                  |
| CSS parse                 | ≥ 1.4M ops/sec                                  |
| Composite parser          | ≥ 50K ops/sec                                   |
| Concurrent workers        | Configurable 1–1000+                            |
| Memory                    | < 500 MB for 100K-page crawls                   |

### 5.2 Reliability

| Requirement                                                              | Priority |
|--------------------------------------------------------------------------|----------|
| Checkpoint persistence with atomic writes prevents data loss on crash    | Must     |
| Graceful shutdown preserves all in-flight state                          | Must     |
| Retry logic with configurable backoff for transient HTTP errors          | Must     |
| Proxy auto-rotation on failure maintains crawl continuity                | Should   |

### 5.3 Security

| Requirement                                                              | Priority |
|--------------------------------------------------------------------------|----------|
| robots.txt compliance by default                                         | Must     |
| PII redaction pipeline middleware (emails, phone numbers)                | Should   |
| TLS verification enabled by default (configurable insecure mode)         | Must     |
| No credential storage — API keys via environment variables only          | Must     |

### 5.4 Scalability

| Requirement                                                              | Priority |
|--------------------------------------------------------------------------|----------|
| Linear throughput scaling with worker count                              | Must     |
| Per-domain throttling prevents overwhelming individual targets           | Must     |
| Batch storage writes reduce I/O overhead                                 | Should   |

### 5.5 Maintainability

| Requirement                                                              | Priority |
|--------------------------------------------------------------------------|----------|
| Modular architecture with clean interface boundaries                     | Must     |
| Structured logging with slog (JSON and text formats)                     | Must     |
| Unit tests for engine, parser, and pipeline (23 tests, 100% pass rate)   | Must     |
| Benchmarks for all performance-critical components                       | Should   |

### 5.6 Portability

| Requirement                                                              | Priority |
|--------------------------------------------------------------------------|----------|
| Cross-platform: Linux, macOS, Windows                                    | Must     |
| Docker image for containerized deployment                                | Should   |
| Single static binary with no runtime dependencies                        | Must     |

---

## 6. Data Requirements

### 6.1 Crawl Output Item Schema

```json
{
  "url": "string — source page URL",
  "timestamp": "string — ISO 8601 fetch time",
  "fields": {
    "<rule_name>": "extracted value (string or array)"
  }
}
```

### 6.2 Search Index Document Schema

```json
{
  "url": "string",
  "title": "string",
  "description": "string",
  "keywords": "string",
  "canonical": "string",
  "language": "string",
  "h1": ["string"],
  "h2": ["string"],
  "h3": ["string"],
  "body_text": "string",
  "word_count": "number",
  "outbound_links": ["string"],
  "images": ["string"],
  "content_hash": "string",
  "indexed_at": "string — ISO 8601"
}
```

### 6.3 AI-Enhanced Item Schema (extends 6.1)

```json
{
  "summary": "string — ~200 word summary",
  "entities": {
    "people": ["string"],
    "organizations": ["string"],
    "locations": ["string"]
  },
  "sentiment": "string — positive | negative | neutral | mixed"
}
```

---

## 7. Appendices

### 7.1 Project Structure

```
ScrapeGoat/
├── cmd/scrapegoat/          # CLI entry point (crawl, search, ai-crawl, version, config)
├── pkg/scrapegoat/          # Public Go SDK
├── internal/
│   ├── engine/              # Scheduler, frontier, dedup, checkpoint, robots
│   ├── fetcher/             # HTTP fetcher, browser fetcher, proxy rotation, stealth
│   ├── parser/              # CSS, XPath, regex, structured data (JSON-LD, OG)
│   ├── pipeline/            # Middleware chain: trim, sanitize, PII, dedup, filter
│   ├── storage/             # JSON, JSONL, CSV file writers + database backend
│   ├── ai/                  # LLM client, summarizer, NER, sentiment, content filter
│   ├── config/              # YAML + env config loading and validation
│   ├── observability/       # Prometheus metrics server
│   ├── api/                 # HTTP API server
│   ├── dashboard/           # Web dashboard
│   ├── seo/                 # SEO audit tools
│   ├── media/               # Media downloader
│   ├── monitor/             # Change monitoring
│   ├── repl/                # Interactive REPL
│   ├── plugin/              # Plugin registry
│   ├── automation/          # Browser automation
│   ├── distributed/         # Distributed crawling (master node)
│   └── types/               # Shared types (Item, Request, Response, errors)
├── examples/                # Ready-to-run example scrapers
├── tests/                   # Integration tests
├── configs/                 # Default YAML configuration
├── scripts/                 # Test runner scripts
├── Dockerfile               # Container build
├── docker-compose.yaml      # Dev services
└── Makefile                 # Build, test, lint commands
```

### 7.2 Key Dependencies

| Dependency          | Purpose                          | Version    |
|---------------------|----------------------------------|------------|
| `spf13/cobra`       | CLI command framework            | v1.10.2    |
| `spf13/viper`       | Configuration management         | v1.21.0    |
| `PuerkitoBio/goquery`| CSS selector parsing            | v1.11.0    |
| `antchfx/htmlquery` | XPath expression evaluation      | v1.3.5     |
| `go-rod/rod`        | Headless browser automation      | v0.116.2   |
| `go-rod/stealth`    | Anti-detection stealth mode      | v0.4.9     |
| `andybalholm/brotli`| Brotli decompression             | v1.2.0     |
| `mongo-driver`      | MongoDB BSON (data serialization)| v1.17.9    |

### 7.3 Test Matrix

| Suite                                       | Tests | Status   |
|---------------------------------------------|-------|----------|
| Engine (frontier, dedup, stats)             | 7     | ✅ PASS   |
| Parser (CSS, XPath, regex, structured)      | 8     | ✅ PASS   |
| Pipeline (trim, sanitize, PII, dates, dedup)| 9     | ✅ PASS   |
| **Total**                                   | **23**| **✅ ALL PASS** |
