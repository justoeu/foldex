# AGENTS.md — Foldex project invariants

> Instructions for Codex (and any coding assistant). These rules apply to **every** change in this repo. They are **non-negotiable**.

## 1. Always run on the latest stable versions

When upgrading or scaffolding, check the **actual latest stable** version of each dependency before pinning. Sources of truth:

- **Go:** https://go.dev/dl/ (the version on top is "current"). Match `go.mod`'s `go` directive and the Docker base image (`golang:X.Y-alpine`).
- **Node:** the LTS on https://nodejs.org/ (used in `web/Dockerfile` and `extension/` if applicable).
- **npm packages:** `npm view <pkg> version` against `https://registry.npmjs.org/`. Always query the public registry for version checks — never a local mirror.

Current pinned versions (re-verify on every upgrade — `bun pm ls` to confirm):

| Stack            | Version pinned    | Notas                                              |
|------------------|-------------------|----------------------------------------------------|
| Go               | `1.26.x`          | `go.mod` directive `go 1.26`                       |
| Node             | `24.x` (LTS)      | usado só pelo `web/Dockerfile`                     |
| Postgres         | `16-alpine`       |                                                    |
| Chi              | `v5.2.x`          |                                                    |
| pgx              | `v5.9.x`          |                                                    |
| testcontainers   | `v0.42.x`         |                                                    |
| Vite             | `^8.0`            |                                                    |
| React            | `^19.2`           |                                                    |
| **MUI**          | **`^7.3`** ⚠️     | **NÃO** subir pra v8/v9 sem migrar `Stack`/`Typography` (passa a exigir `component=` em vários nodes). Avaliação caso a caso. |
| TanStack Query   | `^5.100`          |                                                    |
| TypeScript       | `^6.0`            |                                                    |
| Vitest           | `^4.1`            |                                                    |
| jsdom            | `^29`             |                                                    |
| `@testing-library/react` | latest    |                                                    |
| Package manager  | **`bun`** ≥ 1.3   | Bun resolve pacotes platform-specific (`@rollup/rollup-darwin-arm64` etc.) mesmo quando um mirror npm privado está malconfigurado. CI usa `npm ci` no Linux normalmente. |

**Bump policy:** whenever you touch a file in `backend/go.mod` or `web/package.json`, re-check that all listed deps are still on their **latest stable** (no betas, no RCs). If a major version was released, evaluate breaking changes; if minor/patch, bump and re-run tests.

## 2. Always write tests — coverage gate is 85%

For **every** new function, handler, component, or hook you add: **write the test in the same change**. No exceptions.

- **Backend (Go):** unit tests with `testify` + integration tests with `testcontainers-go` behind `//go:build integration`.
- **Frontend (Vite/React):** unit + integration tests with `Vitest` + `@testing-library/react`. Mock the API via `src/test/server.ts` (in-memory axios mock — keep it in sync with backend changes).
- **Browser extension:** the manifest is data, but any JS that touches state (`popup.js`, `options.js`) gets a unit test if it grows beyond ~30 lines. Pre-30 lines, smoke check by sideloading.

### Coverage thresholds (enforced in CI / Makefile)

- **Total:** ≥ **85%** of statements, both backend and frontend.
- **Branches (frontend):** ≥ 80% (looser because UI branches like "loading vs empty vs list" are expensive to drive).
- Excluded from measurement: `cmd/server/main.go`, `internal/db/db.go` (only callable with real DB), `internal/testdb/` (test helper), `src/main.tsx`, `src/theme/**`, `src/test/**`, `src/api/client.ts`.

### Running tests + coverage

```bash
# backend
make test-backend              # unit only, must pass
make test-integration          # unit + integration (needs Docker)
make coverage-backend          # writes coverage.out + prints % (fails if < 85)

# frontend
cd web && npm test             # unit + component
cd web && npm run coverage     # writes coverage/ + prints % (fails if < 85)
```

CI (when added) must run both `coverage-backend` and `npm run coverage`. PRs that drop coverage below 85% are blocked.

## 3. Always update documentation when behavior changes

When **anything user-visible or API-shaped** changes, the SDD docs **must** be updated in the same change:

| Change to ...                            | Update ...                              |
|------------------------------------------|-----------------------------------------|
| Feature scope, goals, MVP boundary       | `docs/VISION.md`                        |
| API surface, data model, stack, ADRs     | `docs/ARCHITECTURE.md`                  |
| Task done / lessons learned / followups  | `docs/TASKS.md` (append to "Log de conclusão") |
| Stack version bump                       | `docs/ARCHITECTURE.md` + this `AGENTS.md` table |
| README quickstart, smoke test, shortcuts | `README.md`                             |
| Browser extension behavior               | `extension/README.md`                   |

A change that ships code but skips the doc update is considered **incomplete**.

## 4. Data invariants — what must always hold

These are checked by tests and enforced by the schema / handlers:

- **`tag.name` is unique** (DB constraint + `tag_name_taken` error code on conflict).
- **`link.click_count` is monotonic-increasing.** Only `/go/:id` bumps it, atomically inside `UPDATE … RETURNING url`. Tests must assert it never decreases.
- **`link_tag` is the only place a link↔tag association lives.** No denormalization. M:N is mutated only through `links` handlers (Create/Update with `tag_ids`).
- **Tag deletion cascades** to `link_tag` (FK `ON DELETE CASCADE`). Links survive; their tag set shrinks.
- **`preview_status ∈ {pending, ok, failed}`.** Worker is the only writer. Handlers never set it directly.
- **Imports are idempotent by URL.** Re-importing the same `bookmarks.html` produces `skipped` matches, never duplicates.
- **SSRF guard is on by default.** The `FOLDEX_PREVIEW_ALLOW_PRIVATE=1` escape hatch exists **only** for integration tests. Never default-on in production.
- **`SHARED_SECRET`, when set, gates all `/api/*`.** `/healthz` and `/go/:id` remain public — they're operational endpoints.
- **`.env` is never committed.** `.env.example` is the only source of canonical config in the repo.

## 5. Definition of Done — every change must check all boxes

Before you announce "done," verify each item below. If any fails, the change is not done.

- [ ] Code compiles cleanly (`go build ./...`, `npm run build`).
- [ ] `go vet ./...` is silent.
- [ ] `npm run typecheck` is silent.
- [ ] Tests added for new code paths (success + at least one error path).
- [ ] Existing tests still pass (`make test-integration` for backend, `npm test` for web).
- [ ] Coverage ≥ 85% (`make coverage-backend`, `npm run coverage`).
- [ ] Docs updated according to the matrix in §3.
- [ ] Versions still on latest stable per §1.
- [ ] Invariants in §4 not violated (where touched).
- [ ] User-visible UI changes manually validated in a real browser when behavior changes (not just type-check).

## 6. Style choices that are NOT negotiable

- **Backend:** Chi router, pgx + pgxpool, slog. No ORMs, no global state, no service locators.
- **Frontend:** MUI as the only UI library. TanStack Query for server state, no Redux. axios as the HTTP client. `react-hotkeys-hook` for keyboard shortcuts.
- **Migrations:** `golang-migrate`, `000NNN_*.up.sql` / `.down.sql` only. Each migration must be reversible (real `.down.sql`).
- **Errors:** uniform JSON envelope `{ "error": { "code", "message" } }`. Backend handlers go through `httperr.Write`. Never leak `pgx` errors to clients.
- **Logs:** structured (slog JSON). No `fmt.Println`.
- **Comments:** only when *why* is non-obvious. No "what" comments, no task references, no commit ids.

---

> Whenever this file conflicts with another instruction inside the project (README, ARCHITECTURE), **this file wins**. Update the other doc.
