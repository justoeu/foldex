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
# $(CURDIR), never $(PWD) — see scripts/test-make-migrate-path.sh for what
# broke. Digest-pinned like every other CI image (CLAUDE.md §4): the DAST now
# pulls this one, and a version tag is mutable.
# The DSN as the MIGRATE CONTAINER must see it.
#
# `POSTGRES_HOST=localhost` is a supported setting (CLAUDE.md §8: the backend's
# compose service aliases `localhost` to the host gateway via extra_hosts, so a
# Postgres on the developer's machine works). The migrate CLI runs in a
# container of its own with no such alias, where `localhost` is the container
# itself — so `make migrate-up` dialled nothing and failed with "connection
# refused" on an instance whose backend was connected and healthy.
#
# `--add-host=localhost:host-gateway` does NOT fix it: Docker appends to
# /etc/hosts and the pre-existing `127.0.0.1 localhost` line still wins the
# lookup (verified — the name resolves to ::1 with the alias present). The host
# has to be rewritten in the URL, which also covers an explicitly overridden
# DB_URL rather than only the one derived from POSTGRES_HOST.
MIGRATE_DB_URL := $(subst @localhost:,@host.docker.internal:,$(subst @127.0.0.1:,@host.docker.internal:,$(DB_URL)))
MIGRATE := docker run --rm --network $(COMPOSE_NETWORK) \
	--add-host=host.docker.internal:host-gateway \
	-v $(CURDIR)/db/migrations:/migrations \
	migrate/migrate:v4.17.0@sha256:4d017c6fb5997127093648cab09e63d377997125c3d3dcca18e5d1c847da49fa

COVERAGE_MIN ?= 85
# Packages measured for coverage. Excludes cmd (server boot and the RustFS
# bootstrap helper), internal/db (requires real DB), and internal/testdb.
# authctxtest is test scaffolding (it supplies the principal handler tests
# need), so it is excluded for the same reason testdb is: instrumenting a
# helper that exists only for tests deflates the number that gates real code.
COVER_PKGS = $(shell go list ./internal/... | grep -v -E 'testdb$$|/db$$|authctxtest$$|spantest$$' | paste -sd, -)

# Detect Colima Docker socket automatically so testcontainers works.
DOCKER_HOST ?= $(shell docker context inspect colima -f '{{.Endpoints.docker.Host}}' 2>/dev/null)
TESTCONTAINERS_ENV = TESTCONTAINERS_RYUK_DISABLED=true TESTCONTAINERS_HOST_OVERRIDE=localhost

# Package parallelism for the integration suite.
#
# Go defaults `-p` to GOMAXPROCS, and every package that touches the database
# starts its own Postgres, so on a 10-core laptop the default asks Docker for
# ~19 containers at once. That is not a throughput win: past a handful, the
# daemon slows down, container startup stretches from seconds to over a minute,
# and packages begin blowing Go's 10-minute timeout — reported as a hung test,
# which is the least useful description of "your Docker was busy" imaginable.
#
# Four is enough to keep the cores fed (most of the wall clock is I/O against
# Postgres) while leaving the daemon responsive. Override for a machine with
# room: `make test-integration TEST_PARALLEL=8`.
TEST_PARALLEL ?= 4
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

fmt-check: ## Fail if any Go file under ./internal or ./cmd is not gofmt-clean
	@unformatted=$$(gofmt -l ./internal ./cmd); \
	if [ -n "$$unformatted" ]; then \
	  echo "gofmt: the following files are not formatted:"; echo "$$unformatted"; \
	  echo "run 'gofmt -w ./internal ./cmd'"; exit 1; \
	fi

test: ## Run UNIT tests only (no Docker)
	go test ./...

test-integration: ## Run unit + integration tests (Docker required)
	$(TESTCONTAINERS_ENV) go test -tags=integration -p $(TEST_PARALLEL) -timeout=25m ./...

coverage-run: ## Run all tests with coverage instrumentation (no threshold gate)
	# `-covermode=atomic` is required when running multi-package coverage with
	# `-coverpkg=...` — default `set`/`count` produces a deflated total because
	# each test binary writes its own profile and the merge averages instead
	# of taking the union.
	# `-count=1` disables the test cache. Without it, cached pass results
	# from a previous run reuse the OLD coverage profile, so changing what is
	# instrumented (or which tests exercise which lines) silently shows the
	# wrong total. Worth the rerun cost for honest numbers.
	$(TESTCONTAINERS_ENV) go test -tags=integration -p $(TEST_PARALLEL) -covermode=atomic -coverpkg=$(COVER_PKGS) -coverprofile=coverage.out -count=1 -timeout=25m ./...

coverage-check: ## Read coverage.out and fail if below COVERAGE_MIN (default 85)
	@test -f coverage.out || (echo "coverage.out missing — run 'make coverage-run' first" && exit 1)
	@total=$$(go tool cover -func=coverage.out | tail -1 | awk '{print $$3}' | tr -d '%'); \
	echo "Total coverage: $$total% (min $(COVERAGE_MIN)%)"; \
	awk -v t=$$total -v m=$(COVERAGE_MIN) 'BEGIN{exit (t+0 < m+0)}' \
	  || (echo "FAIL: coverage $$total% < $(COVERAGE_MIN)%"; exit 1)

coverage: coverage-run coverage-check ## Run tests + enforce coverage gate (kept for local convenience)

coverage-html: coverage ## Open coverage report in the browser
	go tool cover -html=coverage.out

# @, because make echoes the expanded recipe before running it — and the
# expansion carries POSTGRES_PASSWORD inside DB_URL. The DAST runs this in a
# retained Actions log, and an operator runs it in their terminal. migrate's own
# output is unaffected; only the command line is suppressed. The DSN is still
# visible in `ps` on the machine that runs it, which is a smaller blast radius
# than a stored log and is not fixable without overriding the image entrypoint.
migrate-up: ## Apply all pending migrations
	@$(MIGRATE) -path /migrations -database "$(MIGRATE_DB_URL)" up

migrate-down: ## Revert the most recent migration
	@$(MIGRATE) -path /migrations -database "$(MIGRATE_DB_URL)" down 1

migrate-create: ## Create a new migration: make migrate-create NAME=add_x
	$(MIGRATE) create -ext sql -dir /migrations -seq $(NAME)

.PHONY: help tidy run build fmt-check test test-integration coverage coverage-run coverage-check coverage-html migrate-up migrate-down migrate-create
