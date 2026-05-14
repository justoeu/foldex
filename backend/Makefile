SHELL := /bin/bash
.DEFAULT_GOAL := help

# Load ../.env so the Postgres credentials live in a single place.
# Variables are overridable from the shell (e.g. `DB_URL=... make migrate-up`).
ENV_FILE := ../.env
ifneq (,$(wildcard $(ENV_FILE)))
  include $(ENV_FILE)
  export
endif

POSTGRES_USER     ?= foldex
POSTGRES_PASSWORD ?= foldex
POSTGRES_DB       ?= foldex
POSTGRES_PORT     ?= 5432
POSTGRES_SSLMODE  ?= disable
# Host as seen from the migration CLI (which we run via `docker run` joined to
# the shared foldex network). When `db`, it reaches the self-hosted Postgres
# in docker-compose.db.yml. For an external DB, set POSTGRES_HOST to that
# hostname in your .env (the same value the backend uses).
POSTGRES_HOST     ?= db

# Migrations talk to Postgres through the shared `foldex` network so we never
# depend on a published host port. The migrate CLI joins it temporarily.
COMPOSE_NETWORK ?= foldex
DB_URL          ?= postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@$(POSTGRES_HOST):$(POSTGRES_PORT)/$(POSTGRES_DB)?sslmode=$(POSTGRES_SSLMODE)
MIGRATE := docker run --rm --network $(COMPOSE_NETWORK) -v $(PWD)/db/migrations:/migrations migrate/migrate:v4.17.0

COVERAGE_MIN ?= 85
# Packages measured for coverage. Excludes cmd/server (boot), internal/db
# (requires real DB), and internal/testdb (test helper).
COVER_PKGS = $(shell go list ./internal/... | grep -v -E 'testdb$$|/db$$' | paste -sd, -)

# Detect Colima Docker socket automatically so testcontainers works.
DOCKER_HOST ?= $(shell docker context inspect colima -f '{{.Endpoints.docker.Host}}' 2>/dev/null)
TESTCONTAINERS_ENV = TESTCONTAINERS_RYUK_DISABLED=true TESTCONTAINERS_HOST_OVERRIDE=localhost
ifdef DOCKER_HOST
  TESTCONTAINERS_ENV := DOCKER_HOST=$(DOCKER_HOST) $(TESTCONTAINERS_ENV)
endif

help:
	@awk 'BEGIN{FS=":.*##"; printf "\nTargets:\n\n"} /^[a-zA-Z_-]+:.*?##/{printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

tidy: ## Run go mod tidy
	go mod tidy

run: ## Run backend locally (expects local Postgres on :5432)
	go run ./cmd/server

build: ## Build binary into ./bin
	go build -o bin/foldex ./cmd/server

test: ## Run UNIT tests only (no Docker)
	go test ./...

test-integration: ## Run unit + integration tests (Docker required)
	$(TESTCONTAINERS_ENV) go test -tags=integration -timeout=10m ./...

coverage: ## Run all tests with coverage and enforce COVERAGE_MIN (default 85)
	# `-covermode=atomic` is required when running multi-package coverage with
	# `-coverpkg=...` — default `set`/`count` produces a deflated total because
	# each test binary writes its own profile and the merge averages instead
	# of taking the union.
	# `-count=1` disables the test cache. Without it, cached pass results
	# from a previous run reuse the OLD coverage profile, so changing what is
	# instrumented (or which tests exercise which lines) silently shows the
	# wrong total. Worth the rerun cost for honest numbers.
	$(TESTCONTAINERS_ENV) go test -tags=integration -covermode=atomic -coverpkg=$(COVER_PKGS) -coverprofile=coverage.out -count=1 -timeout=10m ./internal/...
	@total=$$(go tool cover -func=coverage.out | tail -1 | awk '{print $$3}' | tr -d '%'); \
	echo "Total coverage: $$total% (min $(COVERAGE_MIN)%)"; \
	awk -v t=$$total -v m=$(COVERAGE_MIN) 'BEGIN{exit (t+0 < m+0)}' \
	  || (echo "FAIL: coverage $$total% < $(COVERAGE_MIN)%"; exit 1)

coverage-html: coverage ## Open coverage report in the browser
	go tool cover -html=coverage.out

migrate-up: ## Apply all pending migrations
	$(MIGRATE) -path /migrations -database "$(DB_URL)" up

migrate-down: ## Revert the most recent migration
	$(MIGRATE) -path /migrations -database "$(DB_URL)" down 1

migrate-create: ## Create a new migration: make migrate-create NAME=add_x
	$(MIGRATE) create -ext sql -dir /migrations -seq $(NAME)

.PHONY: help tidy run build test test-integration coverage coverage-html migrate-up migrate-down migrate-create
