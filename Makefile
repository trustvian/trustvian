MODULE   := github.com/trustvian/trustvian
BINARY   := trustvian
BIN_DIR  := bin
CMD      := ./cmd/trustvian
GO       := go

.DEFAULT_GOAL := help

.PHONY: help build run demo baseline-demo test test-race bench vet fmt fmt-check tidy coverage install clean check examples \
	compose-up compose-down compose-smoke recovery-drill integration-postgres \
	check-modules check-platform-boundary release-dry-run vulncheck container-build container-scan sbom \
	pr-title

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

build: ## Build the trustvian CLI to bin/trustvian
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/$(BINARY) $(CMD)

run: build ## Build and run the CLI, e.g. make run ARGS="analyze events.json"
	./$(BIN_DIR)/$(BINARY) $(ARGS)

demo: build ## Run analyze against the bundled example fixture
	./$(BIN_DIR)/$(BINARY) analyze cmd/trustvian/testdata/normal.json

baseline-demo: build ## Run baseline build against the bundled example corpus
	./$(BIN_DIR)/$(BINARY) baseline build cmd/trustvian/testdata/corpus.json

test: ## Run all tests
	$(GO) test ./...

test-race: ## Run all tests with the race detector
	$(GO) test -race ./...

bench: ## Run all benchmarks with allocation stats
	$(GO) test -run '^$$' -bench . -benchmem ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format all Go files in place
	gofmt -w .

fmt-check: ## Fail if any Go file is not gofmt-formatted
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi

tidy: ## Tidy go.mod/go.sum
	$(GO) mod tidy

coverage: ## Run tests with coverage and write an HTML report
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "coverage report: coverage.html"

install: ## Install the CLI to GOBIN (or GOPATH/bin)
	$(GO) install $(CMD)

clean: ## Remove build, coverage, and release artifacts
	rm -rf $(BIN_DIR) coverage.out coverage.html dist

check: fmt-check vet build test-race ## Full local gate: fmt-check, vet, build, race tests

COMPOSE_DIR  := deployments/docker-compose
COMPOSE      := docker compose -f $(COMPOSE_DIR)/compose.yaml
# Matches .env.example. Override on the command line to point the
# integration suites at a different database.
POSTGRES_DSN ?= postgres://trustvian:change-me@localhost:5433/trustvian?sslmode=disable

# One source for the image repository, shared with the release workflow.
# Overridable, but never hand-spelled in two places — see
# scripts/image-name.sh for why the owner is normalized.
IMAGE        ?= $(shell ./scripts/image-name.sh)
IMAGE_TAG    ?= local

# GOWORK=off on every module, including the root.
#
# This matters more than it looks. go.work is gitignored, so a developer has
# one and CI does not. With the workspace active, MVS raises the root
# module's dependency versions to satisfy every workspace member — which
# silently masked a reachable vulnerability in the root module that CI then
# reported. Scanning the way CI resolves is the only result worth trusting.
vulncheck: ## Scan every module for reachable vulnerabilities, exactly as CI resolves them
	GOWORK=off $(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...
	cd processor && GOWORK=off $(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...
	cd examples  && GOWORK=off $(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...
	cd platform  && GOWORK=off $(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

container-build: ## Build the official container image locally — nothing is pushed
	docker buildx build --platform linux/amd64 --load -t $(IMAGE):$(IMAGE_TAG) .

container-scan: container-build ## Scan the local container image for fixable CRITICAL/HIGH
	@mkdir -p dist
	docker save $(IMAGE):$(IMAGE_TAG) -o dist/image.tar
	docker run --rm -v $(PWD)/dist:/w aquasec/trivy:latest image \
		--input /w/image.tar --severity CRITICAL,HIGH --ignore-unfixed \
		--exit-code 1 --scanners vuln

sbom: ## Build with an SPDX SBOM attestation and extract it to dist/ (needs a docker-container builder; see docs/supply-chain.md)
	@mkdir -p dist
	docker buildx build --platform linux/amd64 --sbom=true --provenance=mode=max \
		--output type=oci,dest=dist/image-oci.tar -t $(IMAGE):$(IMAGE_TAG) .
	./scripts/extract-sbom.sh dist/image-oci.tar dist/sbom.spdx.json
	@echo "SBOM: dist/sbom.spdx.json"

check-platform-boundary: ## Verify the core/platform boundary (see docs/adr/0022-core-platform-boundary.md)
	./scripts/check-platform-boundary.sh

check-modules: ## Verify module publication invariants (see docs/release-guide.md)
	./scripts/check-modules.sh

pr-title: ## Check a pull request title against docs/COMMIT_CONVENTION.md — make pr-title TITLE='feat(store): add persistence'
	@./scripts/check-pr-title.sh "$(TITLE)"

release-dry-run: ## Build the full release artifact matrix locally — no tag, no credentials, no upload
	./scripts/release-build.sh

compose-up: ## Start the reference Docker Compose deployment (see deployments/docker-compose/README.md)
	$(COMPOSE) up -d --build

compose-down: ## Stop the reference deployment, KEEPING the learned baseline volume
	$(COMPOSE) down

compose-smoke: ## Run the reference deployment's end-to-end smoke test
	./$(COMPOSE_DIR)/smoke-test.sh

# Deliberately no `make backup` / `make restore`: restore needs an explicit
# target database, and a Make target is the wrong place to hide which
# database an operation touches. Run scripts/backup-postgres.sh and
# scripts/restore-postgres.sh directly (docs/operations.md).
recovery-drill: ## Run the backup -> restore -> cutover -> readiness drill on the reference deployment
	./$(COMPOSE_DIR)/recovery-drill.sh

integration-postgres: ## Run the PostgreSQL integration+stress suites against the Compose database
	$(COMPOSE) up -d postgres
	TRUSTVIAN_TEST_POSTGRES_DSN='$(POSTGRES_DSN)' $(GO) test -race ./...

examples: ## Run every examples/* program and fail if any exits non-zero
	@for d in examples/*/; do \
		if [ -f "$$d/main.go" ]; then \
			echo "==> $$d"; \
			(cd "$$d" && go run .) || exit 1; \
		fi; \
	done
