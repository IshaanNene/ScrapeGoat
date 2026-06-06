.PHONY: build test lint run clean docker-build docker-up help

BINARY_NAME=scrapegoat
BUILD_DIR=./bin
MAIN_PATH=./cmd/scrapegoat
GOLANGCI_LINT_VERSION ?= v1.64.8
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
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	fi
	@if command -v golangci-lint > /dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		"$(GOLANGCI_LINT_BIN)" run ./...; \
	fi

clean: ## Clean build artifacts
	rm -rf $(BUILD_DIR)
	go clean -cache

docker-build: ## Build Docker image
	docker build -t scrapegoat:$(VERSION) .

docker-up: ## Start dev services
	docker-compose up -d

docker-down: ## Stop dev services
	docker-compose down

deps: ## Download dependencies
	go mod download
	go mod tidy
