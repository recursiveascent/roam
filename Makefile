.PHONY: build clean test check release-check release-verify release help

BIN := build/roam

build: ## Build roam
	go build -o $(BIN) .

clean: ## Remove build artifacts
	rm -rf build dist

test: ## Run tests
	go test ./...

check: test ## Run all checks
	go vet ./...
	go fix -diff ./...
	$(MAKE) -C ./internal/netmon/testdata

release-check: ## Build and validate a snapshot release
	./scripts/release-check.sh

release-verify: ## Verify the published release and Homebrew formula
	./scripts/release-verify.sh

release: ## Publish a GitHub release
	./scripts/goreleaser.sh release --clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
