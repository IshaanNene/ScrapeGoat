# MCP Server — AI Agent Integration

ScrapeGoat includes a built-in [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server that allows AI agents like Claude, GPT-4, Cursor, and Cline to use ScrapeGoat as a native crawling and extraction tool.

## Quick Start

```bash
# Stdio mode (for Claude Desktop / Cursor)
scrapegoat mcp

# HTTP mode (for programmatic access)
scrapegoat mcp --transport=http --port=8090 --api-key=your-secret-key
```

## Available Tools

| Tool | Description |
|------|-------------|
| `scrapegoat_crawl` | Crawl a URL with configurable depth, concurrency, and domain filtering |
| `scrapegoat_extract` | Extract structured data from a URL using CSS selectors or auto-extraction |
| `scrapegoat_search` | Full-text search crawl — crawl a site and find pages matching a query |
| `scrapegoat_screenshot` | Take a screenshot of a URL via headless browser |
| `scrapegoat_batch` | Submit multiple URLs for async crawling, returns a job ID |
| `scrapegoat_job_status` | Poll async job status and retrieve results |
| `scrapegoat_sitemap` | Fetch and parse a sitemap.xml |
| `scrapegoat_seo_audit` | Run SEO audit on a URL (score 0-100, issues list) |

## Claude Desktop Configuration

Add to your Claude Desktop config file (`~/Library/Application Support/Claude/claude_desktop_config.json` on macOS):

```json
{
  "mcpServers": {
    "scrapegoat": {
      "command": "scrapegoat",
      "args": ["mcp"],
      "env": {}
    }
  }
}
```

If ScrapeGoat is not in your PATH, use the full path:

```json
{
  "mcpServers": {
    "scrapegoat": {
      "command": "/usr/local/bin/scrapegoat",
      "args": ["mcp"]
    }
  }
}
```

## Cursor Configuration

Add to your Cursor MCP settings (`.cursor/mcp.json` in your project, or global settings):

```json
{
  "mcpServers": {
    "scrapegoat": {
      "command": "scrapegoat",
      "args": ["mcp"]
    }
  }
}
```

## Cline Configuration

In VS Code with Cline, go to **Cline Settings → MCP Servers** and add:

```json
{
  "scrapegoat": {
    "command": "scrapegoat",
    "args": ["mcp"],
    "disabled": false
  }
}
```

## HTTP Transport

For programmatic access or multi-user environments, use HTTP transport:

```bash
export SCRAPEGOAT_API_KEY=your-secret-key
scrapegoat mcp --transport=http --port=8090
```

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/mcp` | JSON-RPC 2.0 endpoint |
| GET | `/mcp/sse` | Server-Sent Events for streaming |
| GET | `/health` | Health check |

### Authentication

HTTP transport **requires** an API key. Include it in requests:

```bash
curl -X POST http://localhost:8090/mcp \
  -H "Content-Type: application/json" \
  -H "X-API-Key: your-secret-key" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

### SSE Streaming

Connect to `/mcp/sse` for real-time updates during long-running crawls:

```bash
curl -N http://localhost:8090/mcp/sse \
  -H "X-API-Key: your-secret-key"
```

## Tool Examples

### Crawl a website

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "scrapegoat_crawl",
    "arguments": {
      "url": "https://books.toscrape.com",
      "max_depth": 2,
      "max_pages": 20
    }
  }
}
```

### Extract data with schema

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tools/call",
  "params": {
    "name": "scrapegoat_extract",
    "arguments": {
      "url": "https://books.toscrape.com/catalogue/a-light-in-the-attic_1000/index.html",
      "schema": {
        "fields": [
          {"name": "title", "selector": "h1", "type": "string"},
          {"name": "price", "selector": ".price_color", "type": "string"},
          {"name": "availability", "selector": ".availability", "type": "string"}
        ]
      }
    }
  }
}
```

### SEO audit

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "tools/call",
  "params": {
    "name": "scrapegoat_seo_audit",
    "arguments": {
      "url": "https://example.com"
    }
  }
}
```

## Docker

```bash
docker run -i scrapegoat mcp  # stdio mode

docker run -p 8090:8090 \
  -e SCRAPEGOAT_API_KEY=your-key \
  scrapegoat mcp --transport=http --port=8090
```
