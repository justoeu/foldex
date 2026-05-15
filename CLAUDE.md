# CLAUDE.md — Foldex project invariants

> Instructions for Claude (and any coding assistant). These are the defaults for every change in this repo — follow them unless the task explicitly says otherwise. Changing a default is fine; explain why in the PR description.

## 0. What is Foldex

A **self-hosted personal bookmark manager** for a single user. Intended use:
- Save links from the browser via UI, ⌘K palette, or MV3 extension
- Tag them (M:N, free-form, color + emoji)
- Track click counts (every `/go/:id` is logged) — feeds a stats dashboard
- Pin favorites to the top
- Import/export to Netscape HTML + a versioned JSON

Threat model: **single-user, local network, no public exposure**. Not multi-tenant. No PII beyond the user's own bookmarks. Postgres is reachable on `host.docker.internal:5432` by default; the backend listens on `127.0.0.1` only.

## 1. Always run on the latest stable versions

When upgrading or scaffolding, check the **actual latest stable** version of each dependency before pinning. Sources of truth:

- **Go:** https://go.dev/dl/ — match `go.mod`'s `go` directive and the Docker base image (`golang:X.Y-alpine`).
- **Node:** the LTS on https://nodejs.org/ — used only inside `web/Dockerfile` (bun image actually runs).
- **Bun image:** https://hub.docker.com/r/oven/bun/tags — `web/Dockerfile`.
- **npm packages:** `npm view <pkg> version --registry=https://registry.npmjs.org/`. Always check the public registry — never trust a local mirror for version lookups.

Current pinned versions (re-verify on every upgrade — `bun pm ls` to confirm):

| Stack            | Version pinned    | Notes                                              |
|------------------|-------------------|----------------------------------------------------|
| Go               | `1.26.x`          | `go.mod` directive `go 1.26`                       |
| bun (Docker)     | `oven/bun:1.3-alpine` | base image of `web/Dockerfile`                 |
| Postgres         | `16-alpine` (`docker-compose.db.yml`); a Postgres ≥16 already running on the host works too | only `click_log` table is new schema; everything else is pg-16-compatible |
| Chi              | `v5.2.x`          |                                                    |
| pgx              | `v5.9.x`          |                                                    |
| testcontainers   | `v0.42.x`         |                                                    |
| golang-migrate   | `v4.17.x`         | inside `migrate/migrate` Docker image              |
| Vite             | `^8.0`            |                                                    |
| React            | `^19.2`           |                                                    |
| **MUI**          | **`^9.0`**        | Used only for `createTheme` (`web/src/theme/theme.ts`) + `<ThemeProvider>` (in tests). No `<Stack>`/`<Typography>`/etc. in render — visual lives in `web/src/styles/foldex.css`. Earlier v8/v9 warning is moot for that reason; do re-evaluate if you ever start rendering MUI components. |
| **react-i18next**| **`^17.0`**       | Wraps `i18next ^26`. Locales: `en` (source-of-truth), `pt`, `es`. Picker in topbar persists choice to `localStorage["foldex.locale"]`. New visible strings MUST go through `t('namespace.key')` and ship in all 3 locale files (`web/src/i18n/locales/{en,pt,es}.json`). Plurals use the modern `_one`/`_other` suffix (NOT the legacy `_plural`). |
| TanStack Query   | `^5.100`          |                                                    |
| TypeScript       | `^6.0`            |                                                    |
| Vitest           | `^4.1`            |                                                    |
| jsdom            | `^29`             |                                                    |
| `@testing-library/react` | `^16.3`   |                                                    |
| Package manager  | **`bun`** ≥ 1.3   | Bun's resolver handles platform-specific packages (`@rollup/rollup-darwin-arm64` etc.) more robustly than `npm` when a private mirror is misconfigured. Docker and CI use `bun install`. |

**Bump policy:** whenever you touch `backend/go.mod` or `web/package.json`, re-check that all listed deps are still on their **latest stable** (no betas, no RCs). If a major version was released, evaluate breaking changes; if minor/patch, bump and re-run tests.

## 2. Always write tests — coverage gate is 85%

For **every** new function, handler, component, or hook you add: **write the test in the same change**. No exceptions.

- **Backend (Go):** unit tests with `testify` + integration tests with `testcontainers-go` behind `//go:build integration`.
- **Frontend (Vite/React):** unit + integration tests with `Vitest` + `@testing-library/react`. Mock the API via `src/test/server.ts` (in-memory axios mock — keep it in sync with backend changes).
- **Browser extension:** the manifest is data, but any JS that touches state (`popup.js`, `options.js`) gets a unit test if it grows beyond ~30 lines. Pre-30 lines, smoke check by sideloading.

### Coverage thresholds (enforced in CI / Makefile)

- **Total:** ≥ **85%** of statements, both backend and frontend.
- **Branches (frontend):** ≥ 80% (UI branches like "loading vs empty vs list" are expensive to drive).
- Excluded from measurement: `cmd/server/main.go`, `internal/db/db.go` (only callable with real DB), `internal/testdb/` (test helper), `src/main.tsx`, `src/theme/**`, `src/test/**`, `src/api/client.ts`.

### Running tests + coverage

```bash
# backend
make test-backend              # unit only, must pass
make test-integration          # unit + integration (needs Docker)
make coverage-backend          # writes coverage.out, fails if < 85%

# frontend
cd web && bun run test         # unit + component
cd web && bun run coverage     # writes coverage/, fails if < 85%
```

**Two things the Makefile's `coverage` target HAS to do** (both burned in by experience):
- `-covermode=atomic` — default `set`/`count` deflates the total when running multi-package with `-coverpkg=...`.
- `-count=1` — disables the test cache. Without it, cached passes from a previous run reuse the OLD coverage profile and silently show the wrong total.

CI must run both `coverage-backend` and `bun run coverage`. PRs that drop coverage below 85% are blocked.

## 3. Always update documentation when behavior changes

When **anything user-visible or API-shaped** changes, the SDD docs **must** be updated in the same change:

| Change to ...                            | Update ...                              |
|------------------------------------------|-----------------------------------------|
| Feature scope, goals, MVP boundary       | `docs/VISION.md`                        |
| API surface, data model, stack, ADRs     | `docs/ARCHITECTURE.md`                  |
| Task done / lessons learned / followups  | `docs/TASKS.md` (append to "Log de conclusão") |
| Stack version bump                       | `docs/ARCHITECTURE.md` + this `CLAUDE.md` table |
| README quickstart, smoke test, shortcuts | `README.md`                             |
| Browser extension behavior               | `extension/README.md`                   |
| Database schema (migrations)             | `docs/ARCHITECTURE.md` (data model section) and add comment block at the top of the `.up.sql` |

A change that ships code but skips the doc update is considered **incomplete**.

## 4. Data invariants — what must always hold

These are checked by tests and enforced by the schema / handlers:

- **`tag.name` is unique** (DB constraint + `tag_name_taken` error code on conflict).
- **`tag.color` is a plain CSS string** — either a hex (`#6366F1`) or a `linear-gradient(135deg, c1, c2)`. Backend treats it as opaque. Frontend `web/src/lib/tagColor.ts` is the SINGLE source of truth for parsing it; new render sites must use `primaryColor(c)` whenever the value flows into `color:` or `color-mix(…)` (those don't accept gradients).
- **Folders are 1:N exclusive AND nestable.** A link belongs to at most one folder via `link.folder_id`. Folders themselves nest via `folder.parent_id` (self-FK). Both FKs are `ON DELETE SET NULL` — deleting a folder promotes children to root and ungroups its links. Cascade delete (`?cascade=1`) recurses via CTE through the whole subtree. Folders coexist with tags but are NOT a synonym — folders = containment, tags = labels. `folder.name` is NOT unique.
- **Folder deletion is non-destructive BY DEFAULT** (`ON DELETE SET NULL` on `link.folder_id`). `DELETE /api/folders/{id}` keeps every contained link; they become ungrouped. The destructive variant — `DELETE /api/folders/{id}?cascade=1` — deletes the folder AND its links inside a single transaction (existing `ON DELETE CASCADE` on `link_tag`/`click_log` cleans up the rest; tags themselves survive). The frontend `useDeleteFolder({ cascade: true })` is the only place that should send `cascade=1`, and only after a `ConfirmDialog destructive` prompt naming the link count.
- **Home view excludes links inside folders.** `GET /api/links?ungrouped=1` is the canonical home query and returns `folder_id IS NULL` only. A link never appears in two places (home grid AND folder card preview).
- **Tag filter and folder scope compose via AND.** Inside a folder, selecting a tag narrows that folder's content by tag (`folder_id = X AND tag_id IN (...)`). The sidebar stays interactive in both contexts — server-side already supports the composition, no UI guard needed.
- **Internal IDs never appear in the URL.** Folder navigation, link IDs, etc. live in component state only — no `?folder=N`, no `/folder/:id` route. The address bar shows only the bare app URL. Same rule for tooltips: `/go/{id-or-slug}` is an implementation detail, the UI label is just "Acessar". The slug IS exposed in `LinkDialog` (the user owns it as the share-friendly path) but never as part of the in-app navigation URL.
- **Folders come BEFORE links in the cards grid** — except in alphabetical sort. Default (Novos / Top / Recentes) renders `folders.map(...)` first, then `links.map(...)`. The two alpha sorts (`alpha` = A→Z, `alpha_desc` = Z→A) deliberately interleave folders and links by name/title so the alphabetical order is honest. Removing the alpha sort restores folders-first.
- **viewMode is per-context.** Home and each folder remember their own choice (cards / compact / list) under `foldex.viewMode.map` in localStorage, keyed by `home` or `folder.<id>`. Default `cards` for any unsaved context. Switching contexts surfaces the saved choice — never the previous context's.
- **Drag-and-drop wiring.** `LinkCard` is the only `draggable` element; the payload is `application/x-foldex-link` containing the link id as a string. `FolderCard` accepts the payload and fires `onDropLink(linkId, folderId)`. `LinkCard` also accepts the payload (link→link gesture) and fires `onMergeWith(sourceId, targetId)`. The actual mutations (PATCH folder_id / POST folder + 2× PATCH) live in `App.tsx` — cards stay UI-only. Same-card drops are no-ops.
- **`link.url` is unique** (added in 000002). Used by importer's `ON CONFLICT DO NOTHING`.
- **`click_log` is the single source of truth for clicks.** `link.click_count` and `link.last_clicked_at` columns no longer exist — both are derived via a `LEFT JOIN LATERAL` on every SELECT. Migrations: 000003 created the table, 000004 backfilled pre-existing counts, 000006 dropped the cache columns.
- **`link.slug` is `NOT NULL UNIQUE`, lowercase + hyphenated** (migration 000009). Auto-derived from `title` via `Slugify` on create; user can override in `LinkDialog`. CHECK constraint `^[a-z0-9]+(-[a-z0-9]+)*$ AND NOT ^[0-9]+$` forbids pure-numeric slugs so `/go/42` always resolves to link id 42 (never to a slug). Resolution priority in `redirect.handler`: try int parse → ID lookup; fall back to slug lookup; 404 otherwise. Backup/import/export must propagate slug end-to-end; helpers `uniqueLinkSlug` (backup restore) and `nextAvailableSlug` (importer) handle conflicts with `-2`, `-3`, … suffix.
- **`/go/:id` is the only path that inserts into `click_log`.** Single INSERT inside a tx that also verifies the link exists (404 otherwise) — never an UPDATE on `link`.
- **`link_tag` is the only place a link↔tag association lives.** No denormalization. M:N is mutated only through `links` handlers (Create/Update with `tag_ids`).
- **Tag deletion cascades** to `link_tag` (FK `ON DELETE CASCADE`). Links survive; their tag set shrinks.
- **`preview_status ∈ {pending, ok, failed}`.** Worker is the only writer (`internal/preview`). Handlers never set it directly.
- **Imports are idempotent by URL.** Re-importing the same `bookmarks.html` produces `skipped` matches, never duplicates. When the JSON export carries a `click_count`, the importer materializes that many rows in `click_log` (stamped at the link's `created_at`) — only on fresh insert.
- **IMDS (`169.254.169.254`) is always blocked** by the preview fetcher. No env opt-out. The `PREVIEW_STRICT_SSRF=1` flag *additionally* blocks loopback, RFC1918, link-local, IPv6 ULA; default is permissive because intranet links are foldex's primary use case.
- **Screenshot is a FALLBACK, never a default action.** `preview.Worker.maybeScreenshot` runs only when **all** of: (a) the HTML fetch returned an empty `og:image`, (b) the link still has no `og_image_url` (no user upload), (c) `preview.IsPublicURL(url)` is true (host resolves to non-private IPs), and (d) the worker was wired with `WithScreenshotFallback(sc, up)` (MinIO present). Removing any of these conditions silently disables the screenshot — never make it unconditional.
- **Manual upload short-circuits the whole preview pipeline.** `UploadImage` sets `og_image_url` AND flips `preview_status='ok'` (clears `preview_error`) in the same UPDATE. The worker's `process()` re-checks at the head of the function: if `og_image_url` is non-empty, it skips the HTML fetch *and* the screenshot fallback. Effect: zero unnecessary work, and the "capturando…" label disappears the instant the upload returns.
- **`SHARED_SECRET`, when set, gates all `/api/*`.** `/healthz` and `/go/:id` remain public — they're operational endpoints.
- **`.env` is never committed.** `.env.example` is the only source of canonical config in the repo.
- **Postgres credentials live in `POSTGRES_*` only — `DB_URL` is derived.** Never duplicate user/password/db across vars. `docker-compose.yml` and `backend/Makefile` build the DSN from `${POSTGRES_USER}:${POSTGRES_PASSWORD}@${POSTGRES_HOST}:${POSTGRES_PORT}/${POSTGRES_DB}?sslmode=${POSTGRES_SSLMODE}`. Override `DB_URL` only for external DBs (TLS, schema, etc.). If you ever change the Postgres user/pass in `.env`, **delete** any `DB_URL=` line.
- **`POSTGRES_HOST` accepts `db` / `localhost` / `host.docker.internal` / any reachable host.** Inside the apps container, `extra_hosts` aliases both `localhost` and `host.docker.internal` to the host gateway so the user's mental model "my Postgres is on localhost" actually works.
- **Backup is a complete DB + MinIO snapshot ZIP.** `POST /api/backup` produces a ZIP with `manifest.json` (kind=`foldex.backup`, schema_version=8, SHA-256 checksums, counts), `database.json` (all 5 tables incl. `link_tags` and `click_logs`) and `files/` mirroring the bucket prefixes (`screenshots/`, `images/`). `manifest.json` is stored uncompressed (`zip.Store`) so the frontend can read counts without inflate; everything else uses `zip.Deflate`. Export runs inside `REPEATABLE READ` so the 5 SELECTs + the bucket listing all see the same snapshot. `POST /api/backup/{validate,restore}` accepts the zip as `multipart/form-data` (2 GiB cap via streaming `MultipartReader`).
- **Backup restore is idempotent by default and never atomic across DB+MinIO.** Three conflict modes: `wipe` (TRUNCATE 5 tables + DELETE bucket prefixes + restore with original IDs preserved + bump sequences), `skip` (`ON CONFLICT DO NOTHING` on tag.name and link.url; old→new id mapping for link_tags/click_logs re-key; bucket files skipped if key exists), `duplicate` (tags renamed to `nome (N)`, folders always new, links with URL collision **fall back to skip + warning** — URL is UNIQUE so true duplication would violate the invariant). The DB phase is a single transaction; files are written post-commit. Crash between the two = re-run with the same zip converges.
- **Backup endpoints require MinIO.** `POST /api/backup/*` are mounted only when the storage client came up. Without MinIO the backup would be silently incomplete (no files), so the routes don't exist at all — `404` rather than `200 OK` with partial data.
- **The `foldex-web` image NEVER ships a private TLS key.** `web/Dockerfile` does NOT `COPY` certs. At container start, `entrypoint-certs.sh` either uses a volume-mounted pair at `/etc/nginx/certs/{cert,key}.pem` (production / mkcert dev) OR generates a self-signed ephemeral pair on the fly. Baking a key into a public image is a HIGH-severity finding (Trivy/Scout flag it) AND a real risk (every operator pulling it would share the same private key). Do not add `COPY certs/...` back to the Dockerfile. Local dev: `make up` bind-mounts `./web/certs` from the gitignored host directory.

## 5. UI/UX invariants — interaction contracts

These are not "nice to have" — they are part of the product contract:

- **Every dialog/modal closes on `Esc`.** Use the `useEscape(onClose, open)` hook in every overlay component. No native `confirm()` allowed — always go through the `ConfirmDialog` provider (`useConfirm({ title, message, destructive })`).
- **Destructive actions render with the danger gradient** (`fx-confirm-btn-danger`) and the trash icon. The kicker is `⚠ AÇÃO DESTRUTIVA` in monospace. The cancel button is the default ghost.
- **Tag creation inside the New Link dialog is deferred until save.** Pending tags live with `id: 0` in selected state; pressing Enter just queues. The link's submit handler creates real tags first, then saves. Cancel = nothing was persisted.
- **Pending tag chips let the user cycle colors** by clicking the colored dot. Palette is the 20-color hue-wheel set in `LinkDialog.tsx` (`INLINE_PALETTE`), spread at Tailwind 500-weight to minimize collisions with existing tags. Hint copy under the picker explains this.
- **Tooltips are CSS-only via `data-tooltip` + optional `data-tooltip-side`.** Never use the native `title` attribute on visible UI — it produces the slow, ugly browser tooltip. Keep `aria-label` for accessibility.
- **The sidebar tag list stays clean** — no per-row edit/delete buttons. Editing/deleting goes through `TagManagerDialog` (opened by the "Gerenciar tags" button at the sidebar footer). Per-row clutter was rejected.
- **Sidebar collapse is a full rail.** When collapsed, only an expand chevron is rendered (44px column). State persisted in `localStorage` as `foldex.sidebar.collapsed`.
- **Pinned links always come first.** SQL `ORDER BY l.pinned DESC, ...` applies in every sort mode. The card shows a gradient pin badge in the top-right (always visible when pinned, on-hover when not).
- **Grid is row-major and density is user-controlled.** `.fx-grid` / `.fx-pingrid` use CSS Grid (never `column-count`) so cards always flow left → right. The density picker (3/5/8) lives in the Topbar's `fx-viewseg`, visible only when `viewMode === 'cards'`, and persists to `localStorage` under `foldex.grid.cols`. Default is 5. Mobile breakpoints (≤980px / ≤640px) only set a **lower cap** — they never override an explicit user choice upward.
- **Card preview area has a fixed height** (`.fx-preview { height: 150px; min/max-height: 150px }`), not an `aspect-ratio`. Images use `object-fit: scale-down` so large images shrink to fit (no crop) and small images render at natural size (no upscale stretch).
- **"preview falhou" hides when an image is already present.** The label is gated by `link.preview_status === 'failed' && !link.og_image_url`. If we have a screenshot or upload, the user already sees a working preview — flagging "failed" alongside it is noise.
- **`localStorage` is the persistence layer for UI preferences.** Today: `foldex.sidebar.collapsed` and `foldex.grid.cols`. Any new toggle that survives reloads goes in `localStorage` under a `foldex.*` namespace, with a SSR-safe `typeof localStorage !== 'undefined'` guard in the initializer.
- **`/go/:id` button label says "Acessar"** — never the implementation path. The full `/go/N` lives in the `data-tooltip` only.
- **Keyboard shortcuts are ALL Alt-based.** `⌥K` = palette, `⌥N` = new link, `⌥F` = new folder. `⌘K` competes with browser URL-bar focus; `⌘N`/`⌘P` are hard-claimed by the browser ("New window" / "Print"). Don't try to reclaim Cmd combos — they're unreliable. Any new SPA shortcut must use `alt+<key>`.
- **Pasting a URL anywhere opens the New Link dialog with it pre-filled.** A document-level `paste` listener (`web/src/hooks/usePasteUrl.ts`) sniffs the clipboard for a URL-shaped payload (`web/src/lib/url.ts:looksLikeUrl` — accepts `http(s)?://`, `ftp://`, `file://`, or bare `example.com/x`; rejects words, plain numbers, multi-word text, and non-web schemes like `mailto:`/`tel:`/`javascript:`). The listener is a no-op when (a) `e.target` is editable (INPUT/TEXTAREA/SELECT/contentEditable) or (b) any `.fx-overlay` is already mounted — so paste inside the search bar, inside a modal, or while the palette is open behaves like a regular paste. On a match it calls `e.preventDefault()` and opens `LinkDialog` with `initialUrl=<pasted>`. `pastedUrl` MUST be cleared on close (manual or save) so subsequent `+ New link` clicks start with an empty URL.
- **Dark mode is neutral charcoal/slate**, not purple. Only the accent (`--fx-accent` indigo `#8B85FF`) carries hue. Backgrounds/surfaces/ink are all neutral gray.
- **Backup mode picker uses dual visual encoding** for `wipe`: red border + red background on the option AND `fx-confirm-btn-danger` on the submit button (red gradient) AND the literal `⚠` prefix in the label. `skip` and `duplicate` use the indigo accent. The submit button's gradient is what makes the destructive intent unmissable; the radio styling alone isn't enough.
- **Backup history persists in `localStorage` under `foldex.backups`** (array of `{id, created_at, duration_ms, size_bytes, counts}`, capped at 10). New entries prepend. Other tabs sync via `storage` event listener.
- **Locale picker lives in the topbar**, between the view-mode segment and the theme toggle. Persists to `localStorage["foldex.locale"]`. Default detection: `navigator.language` falls back to `en`. Adding a new locale = drop a JSON in `web/src/i18n/locales/`, list it in `SUPPORTED_LOCALES` in `web/src/i18n/index.ts`, populate every key from `en.json` (source of truth).

## 6. Definition of Done — every change must check all boxes

Before you announce "done," verify each item below. If any fails, the change is not done.

- [ ] Code compiles cleanly (`go build ./...`, `bun run build`).
- [ ] `go vet ./...` is silent.
- [ ] `bun run typecheck` is silent.
- [ ] Tests added for new code paths (success + at least one error path).
- [ ] Existing tests still pass (`make test-integration` for backend, `bun run test` for web).
- [ ] Coverage ≥ 85% (`make coverage-backend`, `bun run coverage`).
- [ ] Docs updated per §3 matrix.
- [ ] Versions still on latest stable per §1.
- [ ] Invariants in §4 and §5 not violated.
- [ ] If a migration was added: applied to the running Postgres (`docker run migrate/migrate ... up`) and the backend recompiled to use the new schema.
- [ ] User-visible UI changes manually validated in a real browser when behavior changes (not just type-check).

## 7. Style choices — the project's defaults

- **Backend:** Chi router, pgx + pgxpool, slog. No ORMs, no global state, no service locators.
- **Frontend:** Plain React (no MUI in render — MUI is only used for `createTheme`/`ThemeProvider`; visual lives in `web/src/styles/foldex.css` from the handoff + `overrides.css` on top). TanStack Query for server state, no Redux. axios as the HTTP client. `react-hotkeys-hook` for keyboard shortcuts. **i18n via `react-i18next`** — every visible string goes through `t('key')` and is mirrored across `en/pt/es` JSON locale files.
- **Migrations:** `golang-migrate`, `000NNN_*.up.sql` / `.down.sql` only. Each migration must be reversible (a real `.down.sql` or an explicit `SELECT 1;` no-op with a comment explaining why).
- **Errors:** uniform JSON envelope `{ "error": { "code", "message" } }`. Backend handlers go through `httperr.Write`. Never leak `pgx` errors to clients.
- **Logs:** structured (slog JSON). No `fmt.Println`.
- **Comments:** only when *why* is non-obvious. No "what" comments, no task references, no commit ids.

## 8. Architecture in one paragraph

Two docker-compose projects: **`docker-compose.db.yml`** brings up Postgres on the shared `foldex` Docker network (default-off host port binding, exposed only inside the network). **`docker-compose.yml`** brings up the backend (Go + Chi on `:9089`) and web (nginx serving the Vite build on `:9088`). The user can also point at an existing Postgres already running on the host by setting `POSTGRES_HOST=localhost` in `.env` — the backend container resolves `localhost` to the host gateway via `extra_hosts`. Backend talks to db, web proxies `/api` and `/go` to the backend through nginx. The preview worker runs in-process inside the backend as a goroutine pool. Schema: `tag`, `link`, `link_tag` (M:N), `click_log` (single source of truth for clicks; `link.click_count` and `link.last_clicked_at` are derived at read time via LATERAL join).

---

> Whenever this file conflicts with another instruction inside the project (README, ARCHITECTURE), this file wins — update the other doc.
