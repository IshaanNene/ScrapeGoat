# ScrapeGoat TypeScript SDK

Typed TypeScript/JavaScript client for the [ScrapeGoat](https://github.com/IshaanNene/ScrapeGoat) REST API.

## Installation

```bash
npm install scrapegoat-sdk
```

## Quick Start

```typescript
import { ScrapeGoat } from 'scrapegoat-sdk';

const sg = new ScrapeGoat({ baseUrl: 'http://localhost:8080', apiKey: 'sk-your-key' });

// Crawl a website
const result = await sg.crawl({ url: 'https://example.com', depth: 2 });
console.log(`Crawled ${result.pagesCrawled} pages, found ${result.itemsCount} items`);
```

## LLM Extraction

```typescript
const result = await sg.extract({
  url: 'https://example.com/product',
  schema: { title: 'string', price: 'number', inStock: 'boolean' },
  model: 'gpt-4o',
});
console.log(result.data); // { title: "Widget", price: 29.99, inStock: true }
```

## Job Management

```typescript
// Fire-and-forget crawl
const { jobId } = await sg.crawl({ url: 'https://example.com', wait: false });

// Check status later
const job = await sg.getJob(jobId);
console.log(job.status); // 'running' | 'completed' | ...

// List & cancel
const jobs = await sg.listJobs();
await sg.cancelJob(jobId);
```

## Error Handling

```typescript
import { AuthenticationError, RateLimitError, NotFoundError } from 'scrapegoat-sdk';

try {
  await sg.crawl({ url: 'https://example.com' });
} catch (e) {
  if (e instanceof RateLimitError) {
    console.log(`Retry after ${e.retryAfter}s`);
  }
}
```

## Building

```bash
cd sdks/typescript
npm install
npm run build
```
