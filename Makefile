SHELL := /bin/bash
.DEFAULT_GOAL := help

ENV_FILE     ?= .env
COMPOSE_APP  := docker compose -f docker-compose.yml
COMPOSE_DB   := docker compose -f docker-compose.db.yml
COMPOSE_SVC  := docker compose -f docker-compose.services.yml

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

# Same decision for the object store. It used to be answered by the app compose
# file carrying its own copy of the rustfs service, which meant `make up`
# started a store even for the operators pointing at an external one — and, far
# worse, made the store's root password a hard requirement for starting the
# backend and the web. The store now lives in docker-compose.services.yml only,
# and this decides whether to bring it along:
#   RUSTFS_ENDPOINT=rustfs:9000 (or empty)  → yes, foldex owns the store
#   anything else                           → no, the operator has their own
NEED_FOLDEX_STORAGE := $(if $(RUSTFS_ENDPOINT),$(if $(filter rustfs:9000,$(RUSTFS_ENDPOINT)),yes,no),yes)

help: ## Show this help
	@awk 'BEGIN{FS=":.*##"; printf "\nTargets:\n\n"} /^[a-zA-Z_-]+:.*?##/{printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

env: ## Create .env and persist generated local credentials if missing
	@FOLDEX_ENV_FILE="$(ENV_FILE)" FOLDEX_ENV_TEMPLATE=".env.example" bash scripts/init-env.sh
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

storage-up: env network ## Start the bundled RustFS object store (skip if RUSTFS_ENDPOINT is external)
	# --wait is load-bearing, and replaces a guarantee that was lost when the
	# store moved out of docker-compose.yml: the backend used to declare
	# `depends_on: rustfs-init: service_completed_successfully`, and depends_on
	# cannot cross compose projects. Without the wait, `up` returns once
	# rustfs-init has STARTED and the backend races the bucket/IAM bootstrap —
	# storage.New does a single BucketExists at boot with no retry, so losing
	# that race disables screenshots and unmounts /api/backup/* for the whole
	# process lifetime, on a stack that otherwise looks healthy.
	$(COMPOSE_SVC) up -d --wait rustfs
	$(COMPOSE_SVC) up -d --wait --wait-timeout 120 rustfs-init

storage-down: ## Stop RustFS (keep volume)
	# `stop`, not `down`: docker-compose.services.yml also declares `db`, and a
	# `down` on that file would take Postgres with it — which db-down owns.
	$(COMPOSE_SVC) stop rustfs rustfs-init

storage-logs: ## Tail RustFS logs
	$(COMPOSE_SVC) logs -f rustfs

up: env network ## Start the full stack from Docker Hub (Postgres only when POSTGRES_HOST=db)
ifeq ($(NEED_FOLDEX_DB),yes)
	@$(MAKE) db-up
else
	@echo "POSTGRES_HOST=$(POSTGRES_HOST) → skipping foldex-db (using your existing Postgres)"
endif
ifeq ($(NEED_FOLDEX_STORAGE),yes)
	@$(MAKE) storage-up
else
	@echo "RUSTFS_ENDPOINT=$(RUSTFS_ENDPOINT) → skipping foldex-rustfs (using your existing object store)"
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
ifeq ($(NEED_FOLDEX_STORAGE),yes)
	@$(MAKE) storage-up
else
	@echo "RUSTFS_ENDPOINT=$(RUSTFS_ENDPOINT) → skipping foldex-rustfs (using your existing object store)"
endif
	$(COMPOSE_APP) up -d --build

apps-up-build: env network ## Build apps locally and start them (dev mode, assumes Postgres running)
	$(COMPOSE_APP) up -d --build

pull: ## Refresh backend + web images from Docker Hub (does not restart)
	$(COMPOSE_APP) pull

down: ## Stop apps (Postgres keeps running — use db-down for that)
	$(COMPOSE_APP) down

up-mail: network ## Start Mailpit (local SMTP sink + inbox at :8025) for e-mail flows
	docker compose -f docker-compose.mail.yml up -d
	@echo "Mailpit inbox: http://localhost:$${MAILPIT_WEB_PORT:-8025}"
	@echo "Point .env at it: MAIL_DRIVER=smtp MAIL_HOST=mailpit MAIL_PORT=1025 MAIL_STARTTLS=0"

down-mail: ## Stop Mailpit
	docker compose -f docker-compose.mail.yml down

stop-all: down down-mail db-down storage-down ## Stop everything (apps + Mailpit + Postgres + RustFS)

nuke: ## Stop everything and drop the Postgres volume (destructive)
	$(COMPOSE_APP) down
	$(COMPOSE_DB) down -v
	# The object store is STOPPED, not wiped. `down -v` here would also drop
	# foldex_rustfs_data — every screenshot, note image and backup object — and
	# this target only advertises the Postgres volume.
	@$(MAKE) storage-down

logs: ## Tail logs from backend + web
	$(COMPOSE_APP) logs -f

ps: ## Show all foldex container status (apps + Postgres + RustFS)
	@$(COMPOSE_APP) ps
	@echo
	@$(COMPOSE_DB) ps
	@echo
	@$(COMPOSE_SVC) ps rustfs rustfs-init

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
# Bumps web/package.json + extension/manifest.json and commits. After pushing
# main, dispatch release.yml with vX.Y.Z; the validated workflow creates the
# tag and publishes Docker images without a tag-push trigger.
release-patch: ## Bump patch (1.0.8 → 1.0.9) and commit locally
	@./scripts/release.sh patch
release-minor: ## Bump minor (1.0.8 → 1.1.0) and commit locally
	@./scripts/release.sh minor
release-major: ## Bump major (1.0.8 → 2.0.0) and commit locally
	@./scripts/release.sh major

.PHONY: help env up apps-up down stop-all nuke logs ps up-mail down-mail \
        db-up db-down db-nuke db-logs storage-up storage-down storage-logs \
        restart-backend restart-web migrate-up migrate-down seed psql healthz \
        test-backend test-integration coverage-backend test-web coverage-web test-all coverage-all \
        release-patch release-minor release-major
