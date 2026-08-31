# Distributed Crawling

> **This does not work yet.** The document below describes the coordination layer that
> exists in `internal/distributed/`, which is real: a task queue with at-least-once
> delivery and recovery of tasks abandoned by dead workers. What it does not have is a
> crawl. The worker's crawl function logs the task and returns `nil`, and
> `internal/distributed` imports `internal/engine` zero times — so workers share no
> frontier, no dedup set, and no politeness state. Two workers pointed at one site
> would each enforce the delay against their own half of the traffic.
>
> `scrapegoat scale N` now returns an error rather than printing a success message for
> the request it never sent. See [ROADMAP.md](../ROADMAP.md) for what integrating this
> would actually require.

ScrapeGoat coordinates distributed work via a master/worker HTTP coordinator and an in-memory task queue.

## Architecture

```
┌─────────────┐
│   Master    │
│  (coordinator)│
└──────┬──────┘
       │ HTTP API
  ┌────┴────┐
  │ Queue   │  (in-memory today)
  └────┬────┘
  ┌────┼────────┐
  ▼    ▼        ▼
Worker Worker  Worker
```

## Quick Start

### 1. Start Master

```bash
scrapegoat master --addr :8081
```

### 2. Start Workers

```bash
# Terminal 1
scrapegoat worker --master http://localhost:8081 --capacity 10

# Terminal 2
scrapegoat worker --master http://localhost:8081 --capacity 10
```

### 3. Submit Tasks

```bash
curl -X POST http://localhost:8081/api/submit \
  -H "Content-Type: application/json" \
  -d '{"type":"crawl","urls":["https://example.com"],"priority":1}'
```

### 4. Check Status

```bash
curl http://localhost:8081/api/status
```

## Configuration

```yaml
distributed:
  enabled: true
  master_addr: ":8081"
  # Redis fields are placeholders until the real Redis queue backend lands.
  redis_addr: "localhost:6379"
  redis_db: 0
  redis_key: "scrapegoat:tasks"
```

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/register` | Register worker |
| POST | `/api/unregister/:id` | Unregister worker |
| POST | `/api/heartbeat` | Worker heartbeat |
| GET | `/api/tasks/:id` | Get tasks for worker |
| POST | `/api/complete` | Report task completion |
| POST | `/api/submit` | Submit new task |
| GET | `/api/status` | Cluster status |
| GET | `/api/scale` | Scaling info |

## Scaling

```bash
scrapegoat scale 5 --master http://localhost:8081
```

## Docker Compose

```yaml
services:
  master:
    image: scrapegoat:latest
    command: master --addr :8081
    ports: ["8081:8081"]

  worker:
    image: scrapegoat:latest
    command: worker --master http://master:8081
    deploy:
      replicas: 3

  redis:
    image: redis:7-alpine
    ports: ["6379:6379"]
```

> Redis appears in the example compose stack for future queue/backplane work. The current `RedisQueue` implementation falls back to the in-memory queue.
