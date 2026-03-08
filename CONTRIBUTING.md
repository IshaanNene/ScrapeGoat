# Contributing to ScrapeGoat

Thank you for your interest in contributing! This guide will help you get started.

## Development Setup

### Prerequisites

- **Go 1.21+** — [Install Go](https://go.dev/dl/)
- **golangci-lint** — `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`
- **Docker & Docker Compose** (optional, for Redis/Postgres)

### Getting Started

```bash
git clone https://github.com/IshaanNene/ScrapeGoat.git
cd ScrapeGoat
make deps    # Download and tidy modules
make build   # Build the binary
make test    # Run all tests
```

## Code Style

- **Format**: All Go code must be formatted with `gofmt`
- **Lint**: Code must pass `golangci-lint run ./...`
- **Naming**: Follow [Go naming conventions](https://go.dev/doc/effective_go#names)
- **Comments**: All exported types and functions must have doc comments
- **Errors**: Use `fmt.Errorf` with `%w` for error wrapping

## Testing

All changes must include tests:

```bash
# Run unit tests with race detection
make test

# Run specific package tests
go test -v ./internal/engine/
go test -v ./internal/parser/

# Run benchmarks
go test -bench=. -benchmem ./internal/engine/
```

### Test Guidelines

- Unit tests go in `_test.go` files alongside the code they test
- Integration tests (requiring network) go in `tests/`
- Use table-driven tests where appropriate
- Test both success and failure paths
- Benchmarks for performance-critical code (frontier, dedup, parser)

## Pull Request Process

1. **Fork** the repository and create a feature branch from `main`
2. **Write tests** for your changes
3. **Run the full test suite**: `make test`
4. **Run the linter**: `make lint`
5. **Write a clear PR description** explaining what and why
6. **One PR per feature** — keep changes focused

### Commit Messages

Use clear, descriptive commit messages:

```
feat: add XPath selector support to parser
fix: handle 429 rate limiting with Retry-After header
docs: add architecture diagram to docs/
test: add benchmark for frontier push/pop
```

## Project Structure

```
cmd/scrapegoat/      → CLI entry point
pkg/scrapegoat/      → Public SDK for embedding
internal/
  engine/            → Core orchestrator, scheduler, frontier, dedup
  fetcher/           → HTTP/browser fetchers, proxy rotation
  parser/            → CSS/XPath/regex/structured data parsers
  pipeline/          → Middleware chain for item processing
  storage/           → JSON/JSONL/CSV output writers
  config/            → YAML + env config loading
  ai/                → LLM integration
  observability/     → Prometheus metrics
examples/            → Ready-to-run scraper examples
tests/               → Integration tests
docs/                → Architecture docs, documentation site
```

## Where to Contribute

- **New parser types** — add to `internal/parser/`
- **New pipeline middleware** — implement the `Middleware` interface in `internal/pipeline/`
- **New storage backends** — implement the `Storage` interface in `internal/storage/`
- **New examples** — add to `examples/` with a descriptive directory name
- **Bug fixes** — check open issues or report new ones
- **Documentation** — improvements always welcome

## Questions?

Open an issue or start a discussion — we're happy to help!
