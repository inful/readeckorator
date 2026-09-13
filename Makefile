# Makefile for readeckorator.
#
# Targets are intentionally minimal; anything heavier is delegated to
# lefthook or goreleaser. Use `make help` to list targets.

GO              ?= go
BINARY_NAME     ?= readeckorator
BIN_DIR         ?= bin
CMD_PKG         ?= ./cmd/readeckorator
LDFLAGS         ?= -s -w -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Compile the binary into bin/.
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_PKG)

.PHONY: test
test: ## Run all tests.
	$(GO) test ./...

.PHONY: test-race
test-race: ## Run all tests with race detector.
	$(GO) test -race ./...

.PHONY: test-cover
test-cover: ## Run all tests with coverage report.
	$(GO) test -coverprofile=coverage.out ./... && $(GO) tool cover -func=coverage.out

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet ./...

.PHONY: lint
lint: ## Run golangci-lint.
	golangci-lint run

.PHONY: fmt
fmt: ## Run gofmt -s -w.
	$(GO) fmt ./...

.PHONY: tidy
tidy: ## Run go mod tidy.
	$(GO) mod tidy

.PHONY: sqlc
sqlc: ## Generate sqlc Go code from SQL queries (skipped until sqlc.yaml exists).
	if [ -f sqlc.yaml ]; then sqlc generate; else echo "sqlc.yaml not configured yet — skipping"; fi

.PHONY: verify
verify: sqlc test lint ## Full gate: sqlc diff + tests + lint.

.PHONY: hooks
hooks: ## Install lefthook git hooks.
	lefthook install

.PHONY: release
release: ## Cut a release via goreleaser (snapshot by default).
	goreleaser release --clean --snapshot

.PHONY: release-local
release-local: ## Build goreleaser artifacts locally without publishing.
	goreleaser release --clean --snapshot --skip=publish

.PHONY: clean
clean: ## Remove build artifacts.
	rm -rf $(BIN_DIR) dist coverage.out

.PHONY: run
run: build ## Build and run the one-shot classifier.
	./$(BIN_DIR)/$(BINARY_NAME) run

