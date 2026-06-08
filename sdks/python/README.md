# ScrapeGoat Python SDK

Typed Python client for the [ScrapeGoat](https://github.com/IshaanNene/ScrapeGoat) REST API.

## Installation

```bash
pip install scrapegoat
```

Or install from source:

```bash
cd sdks/python
pip install -e ".[dev]"
```

## Quick Start

```python
from scrapegoat import ScrapeGoat

sg = ScrapeGoat("http://localhost:8080", api_key="sk-your-key")

# Crawl a website
result = sg.crawl("https://example.com", depth=2)
print(f"Crawled {result.pages_crawled} pages, found {result.items_count} items")
for item in result.items:
    print(item)
```

## Async Support

```python
import asyncio
from scrapegoat import AsyncScrapeGoat

async def main():
    async with AsyncScrapeGoat("http://localhost:8080", api_key="sk-...") as sg:
        result = await sg.crawl("https://example.com")
        job = await sg.get_job(result.job_id)
        print(job.status)

asyncio.run(main())
```

## LLM Extraction

```python
result = sg.extract(
    "https://example.com/product",
    schema={"title": "string", "price": "number", "in_stock": "boolean"},
    model="gpt-4o",
)
print(result.data)  # {"title": "Widget", "price": 29.99, "in_stock": True}
```

## SEO Audit

```python
audit = sg.seo_audit("https://example.com")
print(f"Score: {audit.score}/100")
for issue in audit.issues:
    print(f"  [{issue['severity']}] {issue['type']}")
```

## Error Handling

```python
from scrapegoat.client import AuthenticationError, RateLimitError, NotFoundError

try:
    result = sg.crawl("https://example.com")
except AuthenticationError:
    print("Bad API key")
except RateLimitError as e:
    print(f"Rate limited, retry after {e.retry_after}s")
except NotFoundError:
    print("Job not found")
```

## Running Tests

```bash
cd sdks/python
pip install -e ".[dev]"
pytest tests/ -v
```
