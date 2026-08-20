.PHONY: build clean test check release help

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

release: ## Publish a GitHub release
	env -u NIX_CFLAGS_COMPILE -u NIX_LDFLAGS -u SDKROOT -u DEVELOPER_DIR goreleaser release --clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
