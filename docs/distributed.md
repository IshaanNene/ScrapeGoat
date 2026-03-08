# Distributed Crawling

ScrapeGoat supports distributed crawling via a master/worker architecture.

## Architecture

```
┌─────────────┐
│   Master    │
│  (coordinator)│
└──────┬──────┘
       │ HTTP API
  ┌────┴────┐
  │  Redis  │  (task queue)
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
