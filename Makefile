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

help: ## Show this help
	@awk 'BEGIN{FS=":.*##"; printf "\nTargets:\n\n"} /^[a-zA-Z_-]+:.*?##/{printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

env: ## Create .env from .env.example if missing
	@test -f $(ENV_FILE) || cp .env.example $(ENV_FILE)
	@echo "$(ENV_FILE) ready"

db-up: env ## Start Postgres (separate compose project)
	$(COMPOSE_DB) up -d

db-down: ## Stop Postgres (keep volume)
	$(COMPOSE_DB) down

db-nuke: ## Stop Postgres AND drop its volume (destructive)
	$(COMPOSE_DB) down -v

db-logs: ## Tail Postgres logs
	$(COMPOSE_DB) logs -f

up: env db-up ## Start the full stack: Postgres + backend + web
	$(COMPOSE_APP) up -d --build

apps-up: env ## Start only backend + web (assumes Postgres already running)
	$(COMPOSE_APP) up -d --build

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

restart-backend: ## Rebuild + restart the backend container
	$(COMPOSE_APP) up -d --build backend

restart-web: ## Rebuild + restart the web container
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

.PHONY: help env up apps-up down stop-all nuke logs ps \
        db-up db-down db-nuke db-logs \
        restart-backend restart-web migrate-up migrate-down seed psql healthz \
        test-backend test-integration coverage-backend test-web coverage-web test-all coverage-all
