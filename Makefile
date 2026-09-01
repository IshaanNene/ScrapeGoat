.PHONY: build test lint run clean docker-build docker-up help

BINARY_NAME=scrapegoat
BUILD_DIR=./bin
MAIN_PATH=./cmd/scrapegoat
# Keep in step with the version pinned in .github/workflows/ci.yml. Linters
# disagree between releases: an older one reports findings CI does not have, a
# newer one can miss findings CI does, and either way the time goes on the tool
# instead of the code.
#
# This was v1.64.8 on the v1 module path, which cannot lint this repository at
# all — .golangci.yml is a v2 config, and v1 exits immediately with "you are
# using a configuration file for golangci-lint v2 with golangci-lint v1". So
# `make lint` was broken for anyone who did not already have a linter installed.
GOLANGCI_LINT_VERSION ?= v2.12.2
GOLANGCI_LINT_MODULE ?= github.com/golangci/golangci-lint/v2/cmd/golangci-lint
GOLANGCI_LINT_BIN ?= $(shell go env GOPATH)/bin/golangci-lint

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-X github.com/IshaanNene/ScrapeGoat/internal/config.Version=$(VERSION)"

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "Built $(BUILD_DIR)/$(BINARY_NAME)"

run: build ## Build and run
	$(BUILD_DIR)/$(BINARY_NAME) $(ARGS)

test: ## Run all tests
	go test ./... -v -count=1 -race -timeout 120s

test-short: ## Run tests (short mode)
	go test ./... -short -count=1 -timeout 60s

lint: ## Run linters
	@if ! command -v golangci-lint > /dev/null 2>&1 && [ ! -x "$(GOLANGCI_LINT_BIN)" ]; then \
		echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."; \
		go install $(GOLANGCI_LINT_MODULE)@$(GOLANGCI_LINT_VERSION); \
	fi
	@if command -v golangci-lint > /dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		"$(GOLANGCI_LINT_BIN)" run ./...; \
	fi

clean: ## Clean this project's build artifacts
	rm -rf $(BUILD_DIR) coverage.out
	# Deliberately NOT `go clean -cache`: that wipes the user's entire global Go
	# build cache, not this project's artifacts, and costs them a full rebuild of
	# everything else they work on. `go clean -testcache` is scoped to test results.
	go clean -testcache

docker-build: ## Build Docker image
	docker build -t scrapegoat:$(VERSION) .

docker-up: ## Start dev services
	docker-compose up -d

docker-down: ## Stop dev services
	docker-compose down

deps: ## Download dependencies
	go mod download
	go mod tidy
