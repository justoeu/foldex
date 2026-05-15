SHELL := /bin/bash
.DEFAULT_GOAL := help

ENV_FILE     ?= .env
COMPOSE_APP  := docker compose -f docker-compose.yml
COMPOSE_DB   := docker compose -f docker-compose.db.yml

# Load .env so we can use vars in recipes (e.g. POSTGRES_USER).
ifneq (,$(wildcard $(ENV_FILE)))
  include $(ENV_FILE)
  export
endif

# Decide whether to spin up the bundled foldex-db (postgres:18-alpine) based
# on the user's POSTGRES_HOST in .env:
#   POSTGRES_HOST=db (or empty)                     → yes, foldex owns Postgres
#   POSTGRES_HOST=localhost / host.docker.internal /
#     external host                                 → no, user has their own DB
NEED_FOLDEX_DB := $(if $(POSTGRES_HOST),$(if $(filter db,$(POSTGRES_HOST)),yes,no),yes)

help: ## Show this help
	@awk 'BEGIN{FS=":.*##"; printf "\nTargets:\n\n"} /^[a-zA-Z_-]+:.*?##/{printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

env: ## Create .env from .env.example if missing
	@test -f $(ENV_FILE) || cp .env.example $(ENV_FILE)
	@echo "$(ENV_FILE) ready"

network: ## Ensure the shared `foldex` Docker network exists
	@docker network inspect foldex >/dev/null 2>&1 || docker network create foldex >/dev/null

db-up: env network ## Start the bundled Postgres (skip if your POSTGRES_HOST != db)
	$(COMPOSE_DB) up -d

db-down: ## Stop Postgres (keep volume)
	$(COMPOSE_DB) down

db-nuke: ## Stop Postgres AND drop its volume (destructive)
	$(COMPOSE_DB) down -v

db-logs: ## Tail Postgres logs
	$(COMPOSE_DB) logs -f

up: env network ## Start the full stack from Docker Hub (Postgres only when POSTGRES_HOST=db)
ifeq ($(NEED_FOLDEX_DB),yes)
	@$(MAKE) db-up
else
	@echo "POSTGRES_HOST=$(POSTGRES_HOST) → skipping foldex-db (using your existing Postgres)"
endif
	$(COMPOSE_APP) up -d

apps-up: env network ## Start only backend + web from Docker Hub (assumes Postgres already running)
	$(COMPOSE_APP) up -d

up-build: env network ## Build images locally from source and start the full stack (dev mode)
ifeq ($(NEED_FOLDEX_DB),yes)
	@$(MAKE) db-up
else
	@echo "POSTGRES_HOST=$(POSTGRES_HOST) → skipping foldex-db (using your existing Postgres)"
endif
	$(COMPOSE_APP) up -d --build

apps-up-build: env network ## Build apps locally and start them (dev mode, assumes Postgres running)
	$(COMPOSE_APP) up -d --build

pull: ## Refresh backend + web images from Docker Hub (does not restart)
	$(COMPOSE_APP) pull

down: ## Stop apps (Postgres keeps running — use db-down for that)
	$(COMPOSE_APP) down

stop-all: down db-down ## Stop everything (apps + Postgres)

nuke: ## Stop everything and drop the Postgres volume (destructive)
	$(COMPOSE_APP) down
	$(COMPOSE_DB) down -v

logs: ## Tail logs from backend + web
	$(COMPOSE_APP) logs -f

ps: ## Show all foldex container status (apps + Postgres)
	@$(COMPOSE_APP) ps
	@echo
	@$(COMPOSE_DB) ps

restart-backend: ## Rebuild + restart the backend container (dev mode, builds locally)
	$(COMPOSE_APP) up -d --build backend

restart-web: ## Rebuild + restart the web container (dev mode, builds locally)
	$(COMPOSE_APP) up -d --build web

migrate-up: ## Apply all pending migrations against the running db
	$(MAKE) -C backend migrate-up

migrate-down: ## Revert the most recent migration
	$(MAKE) -C backend migrate-down

seed: ## Load scripts/seed.sql into the running db
	$(COMPOSE_DB) exec -T db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB) < scripts/seed.sql

psql: ## Open psql against the running db
	$(COMPOSE_DB) exec db psql -U $(POSTGRES_USER) -d $(POSTGRES_DB)

healthz: ## Hit /healthz on the backend
	@curl -fsS http://localhost:$(BACKEND_PORT)/healthz | jq . || echo "backend not reachable"

test-backend: ## Run backend unit tests
	$(MAKE) -C backend test

test-integration: ## Run backend unit + integration tests (Docker required)
	$(MAKE) -C backend test-integration

coverage-backend: ## Backend coverage gate (>= 85%)
	$(MAKE) -C backend coverage

test-web: ## Run frontend tests
	cd web && npm test --silent

coverage-web: ## Frontend coverage gate (>= 85%)
	cd web && npm run coverage --silent

test-all: test-integration test-web ## Run every test, every layer

coverage-all: coverage-backend coverage-web ## Enforce coverage on every layer

# ── Release ─────────────────────────────────────────────────────────────
# Bumps web/package.json + extension/manifest.json, commits, and tags
# vX.Y.Z locally. Pushing the tag is intentionally a separate manual step
# (the script prints the exact command). The push triggers ci.yml which
# publishes Docker images :vX.Y.Z + :vX.Y + :vX + :latest.
release-patch: ## Bump patch (1.0.8 → 1.0.9) and tag locally
	@./scripts/release.sh patch
release-minor: ## Bump minor (1.0.8 → 1.1.0) and tag locally
	@./scripts/release.sh minor
release-major: ## Bump major (1.0.8 → 2.0.0) and tag locally
	@./scripts/release.sh major

.PHONY: help env up apps-up down stop-all nuke logs ps \
        db-up db-down db-nuke db-logs \
        restart-backend restart-web migrate-up migrate-down seed psql healthz \
        test-backend test-integration coverage-backend test-web coverage-web test-all coverage-all \
        release-patch release-minor release-major
