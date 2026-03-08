# Middleware System

ScrapeGoat has two middleware pipelines:

1. **Request Middleware** — intercepts requests before/after fetching
2. **Item Pipeline Middleware** — processes extracted items before storage

## Request Middleware

Request middleware runs on every HTTP request, enabling anti-bot measures, rate limiting, and request transformation.

### Interface

```go
type RequestMiddleware interface {
    Name() string
    Priority() int  // lower = runs first
    ProcessRequest(req *types.Request) *types.Request
    ProcessResponse(resp *types.Response) *types.Response
}
```

### Built-in Request Middleware

| Middleware | Priority | Purpose |
|-----------|----------|---------|
| `RateLimitMiddleware` | 10 | Global rate limiting |
| `ProxyRotationMiddleware` | 20 | Per-request proxy selection |
| `RetryMiddleware` | 50 | Exponential backoff retries |
| `RequestFingerprintMiddleware` | 90 | Browser fingerprint diversification |
| `HeaderRotationMiddleware` | 100 | Rotate Accept/Language/Encoding headers |
| `CaptchaDetectionMiddleware` | 200 | Detect CAPTCHA pages |
| `CloudflareDetectionMiddleware` | 210 | Detect Cloudflare challenges |

### Configuration

```yaml
middleware:
  request:
    - name: header_rotation
      enabled: true
    - name: rate_limit
      enabled: true
      options:
        requests_per_second: 5
    - name: proxy_rotation
      enabled: true
    - name: captcha_detection
      enabled: true
```

### Custom Middleware

```go
type MyMiddleware struct{}

func (m *MyMiddleware) Name() string  { return "my_middleware" }
func (m *MyMiddleware) Priority() int { return 150 }

func (m *MyMiddleware) ProcessRequest(req *types.Request) *types.Request {
    req.Headers.Set("X-Custom", "value")
    return req
}

func (m *MyMiddleware) ProcessResponse(resp *types.Response) *types.Response {
    return resp
}
```

## Item Pipeline Middleware

Pipeline middleware processes scraped items before they reach storage.

### Interface

```go
type Middleware interface {
    Name() string
    Process(item *types.Item) (*types.Item, error) // return nil to drop
}
```

### Built-in Pipeline Middleware

| Middleware | Purpose |
|-----------|---------|
| `TrimMiddleware` | Strip whitespace from strings |
| `RequiredFieldsMiddleware` | Drop items missing fields |
| `DedupMiddleware` | Drop duplicate items |
| `FieldFilterMiddleware` | Keep only specified fields |
| `FieldRenameMiddleware` | Rename fields |
| `DefaultValueMiddleware` | Set defaults for missing fields |

### Configuration

```yaml
pipeline:
  middlewares:
    - name: trim
    - name: required_fields
      options:
        fields: [title, price]
    - name: dedup
      options:
        key: url
```
