# Contributing to ScrapeGoat

Thank you for your interest in contributing! This guide will help you get started.

## Development Setup

### Prerequisites

- **Go 1.25+** — [Install Go](https://go.dev/dl/). `go.mod` targets 1.25.0.
- **golangci-lint v2.12.2**, matching the version CI pins:

  ```bash
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
  ```

  Both halves of that line matter.

  The `/v2/` is not optional. The v1 module path still resolves, and `@latest`
  against it installs v1.64.8 — the last v1, built with Go 1.24, which refuses to
  start against a module targeting Go 1.25. It exits in under a second with a
  config error, which is easy to read as "nothing to report". `.golangci.yml`
  opens with the story of that happening in CI for a while.

  The pinned version is not optional either. Linters disagree between releases:
  an older one reports findings CI does not have, and a newer one can miss
  findings CI does. Either way you are debugging the tool instead of the code.

  `make lint` installs the right one for you if you have none. Three places carry
  this version — here, `Makefile`, and `.github/workflows/ci.yml` — and they have
  to move together.

- **Docker & Docker Compose** (optional) — `docker-compose.yaml` runs the API
  server and Prometheus.

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
- **Lint**: Code must pass `golangci-lint run ./...`. `go vet` is not a
  substitute — it does not run `errcheck`, `staticcheck`, or `gosec`, all of which
  gate CI.
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
  provenance/        → Corpus records: content hashes, reuse signals, policy state
  extract/           → Density-based main-content extraction
  safety/            → SSRF guard: URL validation and the guarded dialer
  fetchlog/          → Record and replay of fetches
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
