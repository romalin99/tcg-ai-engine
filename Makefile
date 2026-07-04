# ══════════════════════════════════════════════════════════════════════════════
# Makefile — tcg-ai-engine
#
# Usage:
#   make              → Show this help
#   make build        → Default build
#   make debug        → Build with debug symbols
#   make release      → Build stripped release binary
#   make run          → Build and run the application
#   make clean        → Remove build artifacts
#   make modclean     → Clean module/build/test cache
#   make deps         → Tidy and verify dependencies
#   make deps-tidy    → Tidy go.mod & go.sum
#   make deps-upgrade → Upgrade all dependencies to latest
#   make deps-verify  → Verify dependency checksums
#   make fmt          → Format code
#   make lint         → Run linter
#   make test         → Run unit tests
#   make testv        → Run verbose tests
#   make cover        → Generate coverage report
#   make bench        → Run benchmarks
#   make check        → Run fmt + lint + tests
#   make swagger      → Regenerate Swagger docs
#   make mock         → Generate mocks
#   make mock-install → Install mock tools
#   make mock-clean   → Remove generated mocks
#   make status       → Print project statistics
#   make version      → Print Go toolchain version
# ══════════════════════════════════════════════════════════════════════════════

# ── Variables ─────────────────────────────────────────────────────────────────
APP     := ai-rulex-engine
BUILD   := ./cmd/api
MAIN    := $(BUILD)/main.go
OUTPUT  := $(BUILD)/$(APP)
GO      := go
GCFLAGS := "all=-N -l"
LDFLAGS := "-s -w"

MOCKERY := $(shell command -v mockery 2>/dev/null)
MOCKGEN  := $(shell command -v mockgen 2>/dev/null)

# ── Help (default) ────────────────────────────────────────────────────────────
.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available targets
	@awk 'BEGIN{FS=":.*##"} /^[a-zA-Z][a-zA-Z0-9_-]*:.*##/{printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ── Build ─────────────────────────────────────────────────────────────────────
.PHONY: build debug release default

default: build ## Alias for build (same as make build)

build: ## Build with default options
	@$(GO) version
	@echo "→ Building default binary..."
	$(GO) build -o $(OUTPUT) $(MAIN)

debug: ## Build with debug symbols (no optimisation, no inlining)
	@$(GO) version
	@echo "→ Building DEBUG binary..."
	$(GO) build -o $(OUTPUT) -gcflags $(GCFLAGS) $(MAIN)

release: ## Build stripped release binary
	@$(GO) version
	@echo "→ Building RELEASE binary (stripped)..."
	$(GO) build -o $(OUTPUT) -ldflags $(LDFLAGS) $(MAIN)

# ── Run & Clean ───────────────────────────────────────────────────────────────
.PHONY: run clean modclean

run: build ## Build then run the application
	@echo "→ Running $(APP)..."
	@$(OUTPUT)

clean: ## Remove build output and log files
	@rm -f $(OUTPUT) rulex-engine.log

modclean: ## Clean module/build/test cache and tidy
	$(GO) clean -cache -modcache -testcache && $(GO) mod tidy

# ── Dependencies ──────────────────────────────────────────────────────────────
.PHONY: deps deps-tidy deps-upgrade deps-verify

deps: deps-tidy deps-verify ## Tidy and verify dependencies
	@echo "✅ Dependencies up to date"

deps-tidy: ## Tidy go.mod and go.sum (remove unused, add missing)
	@echo "→ Tidying modules..."
	@$(GO) mod tidy
	@echo "✅ go mod tidy done"

deps-upgrade: ## Upgrade all dependencies to latest minor/patch versions
	@echo "→ Upgrading dependencies..."
	@$(GO) get -u ./...
	@$(GO) mod tidy
	@echo "✅ Dependencies upgraded"

deps-verify: ## Verify checksums of downloaded dependencies
	@echo "→ Verifying modules..."
	@$(GO) mod verify
	@echo "✅ All modules verified"

# ── Code Quality ──────────────────────────────────────────────────────────────
.PHONY: fmt optimize lint test testv cover bench check status

fmt: ## Format code (gofumpt + golangci-lint fmt)
	@gofumpt -l -w .
	@golangci-lint fmt ./...

optimize: ## Fix struct alignment, format code, and update deprecated APIs
	@fieldalignment -fix ./...
	@gofumpt -l -w .
	@golangci-lint fmt ./...
	@$(GO) fix ./...
	@$(GO) mod tidy

lint: ## Run golangci-lint
	golangci-lint run ./... --fix

# ── Testing ───────────────────────────────────────────────────────────────────
TEST_PKGS := $(shell go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)
.PHONY: test testv cover bench

test: ## Run unit tests
	@echo "→ Running tests..."
	@$(GO) test $(TEST_PKGS) -race -count=1

testv: ## Run verbose tests
	@$(GO) test -v $(TEST_PKGS)

cover: ## Generate coverage report
	@$(GO) test $(TEST_PKGS) -coverprofile=coverage.out
	@$(GO) tool cover -func=coverage.out

bench: ## Run benchmarks
	@$(GO) test $(TEST_PKGS) -bench=. -benchmem

check: fmt lint test ## Run fmt, lint, and tests

status: ## Print project statistics
	@echo "Project statistics:"
	@tokei --exclude .git --exclude vendor --exclude .github --exclude proto --exclude mocks --exclude testdata --exclude target

# ── Swagger ───────────────────────────────────────────────────────────────────
.PHONY: swagger

swagger: ## Regenerate Swagger docs (http://localhost:<port>/swagger/index.html)
	@ln -sf $(MAIN) ./main.go
	@swag fmt && swag init -g $(MAIN) -o ./docs --parseDependency --parseInternal --parseGoList
	@rm -f ./main.go

# ── Misc ──────────────────────────────────────────────────────────────────────
.PHONY: version

version: ## Print Go toolchain version
	@$(GO) version

# ── Mock Generation ───────────────────────────────────────────────────────────
.PHONY: mock mock-install mock-clean mock-verify _mockgen

mock: ## Generate all mocks (mockery preferred, falls back to mockgen)
	@if [ -n "$(MOCKERY)" ]; then \
		echo "→ Generating mocks with mockery..."; \
		$(MOCKERY) || { echo "❌ mockery failed"; exit 1; }; \
		echo "✅ Mocks generated via mockery"; \
	elif [ -n "$(MOCKGEN)" ]; then \
		echo "→ mockery not found, falling back to mockgen..."; \
		$(MAKE) --no-print-directory _mockgen; \
	else \
		echo "❌ No mock tool found. Run: make mock-install"; \
		exit 1; \
	fi

mock-install: ## Install mockery and mockgen
	@echo "→ Installing mock tools..."
	@$(GO) install github.com/vektra/mockery/v2@latest || { echo "❌ Failed to install mockery"; exit 1; }
	@$(GO) install go.uber.org/mock/mockgen@latest      || { echo "❌ Failed to install mockgen";  exit 1; }
	@echo "✅ Mock tools installed"

mock-clean: ## Remove all generated mock files and directories
	@echo "→ Cleaning generated mocks..."
	@find . -type d -name mock -not -path '*/vendor/*' -not -path '*/.git/*' -exec rm -rf {} + 2>/dev/null || true
	@find . -name 'mock_*.go' -not -path '*/vendor/*' -not -path '*/.git/*' -delete 2>/dev/null || true
	@echo "✅ Mock files removed"

mock-verify: ## Show installed mock tool versions
	@if [ -n "$(MOCKERY)" ]; then \
		echo "  ✅ mockery: $(MOCKERY)"; \
		$(MOCKERY) --version; \
	else \
		echo "  ❌ mockery not found  →  run: make mock-install"; \
	fi
	@if [ -n "$(MOCKGEN)" ]; then \
		echo "  ✅ mockgen: $(MOCKGEN)"; \
		$(MOCKGEN) --version; \
	else \
		echo "  ❌ mockgen not found  →  run: make mock-install"; \
	fi

# Internal: scan for interfaces.go files and generate mocks with mockgen.
_mockgen:
	@echo "→ Generating mocks with mockgen..."
	@find . -type f -name 'interfaces.go' -not -path '*/vendor/*' -not -path '*/.git/*' | while read -r file; do \
		dir=$$(dirname "$$file"); \
		pkg=$$(basename "$$dir"); \
		dest="$$dir/mock/mock_$$pkg.go"; \
		mkdir -p "$$dir/mock"; \
		echo "  $$file → $$dest"; \
		$(MOCKGEN) -source="$$file" -destination="$$dest" -package=mock || { echo "❌ Failed: $$file"; exit 1; }; \
	done
	@echo "✅ Mocks generated via mockgen"
