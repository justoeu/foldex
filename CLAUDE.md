# CLAUDE.md — Foldex project invariants

> Defaults for every change in this repo. Override only with a one-line note in the PR description. **WHAT** must hold lives here; **WHY** in long form lives in `docs/ARCHITECTURE.md` ADRs.

## 0. What is Foldex

A **self-hosted personal bookmark manager** for a single user — save links from the browser (UI, ⌘K palette, MV3 extension), tag them (M:N), organize in nestable folders (1:N), track clicks via `/go/{id-or-slug}`, pin favorites, monitor opt-in pages for changes via Web Push, import/export to Netscape HTML + versioned JSON, and full DB+MinIO backup ZIPs.

Threat model: single-user, local network, no public exposure. Backend listens on `127.0.0.1` only by default.

## 1. Always run on the latest stable versions

When upgrading or scaffolding, check the actual latest stable before pinning — `go.dev/dl`, `nodejs.org`, `hub.docker.com/r/oven/bun/tags`, and `npm view <pkg> version --registry=https://registry.npmjs.org/` (always public registry, never a local mirror).

Currently pinned (re-verify on every upgrade with `bun pm ls` / `go list -m all`):

| Stack | Version | Notes |
|---|---|---|
| Go | `1.26.x` | `go.mod` + `golang:X.Y-alpine` base image |
| bun (Docker) | `oven/bun:1.3-alpine` | `web/Dockerfile` |
| Postgres | `18.2-alpine` | pinned in THREE places that MUST stay in lockstep: `docker-compose.db.yml`, `docker-compose.services.yml`, and `internal/testdb` testcontainers — so tests mirror prod (a version-specific planner/default change can't hide behind an older test engine); host's Postgres ≥16 also works |
| Chi / pgx / testcontainers / golang-migrate | `v5.2 / v5.9 / v0.42 / v4.17` | |
| webpush-go | `v1.4` | Web Push (RFC 8030) |
| Vite / React / TS / Vitest / jsdom | `^8 / ^19.2 / ^6 / ^4.1 / ^29` | |
| MUI | `^9.0` | **only** `createTheme` + `ThemeProvider`. Visual lives in `web/src/styles/foldex.css`. |
| react-i18next | `^17` (wraps i18next `^26`) | en (source-of-truth) / pt / es. New visible strings MUST go through `t()` and ship in all 3 locales. Plurals use `_one`/`_other` (not legacy `_plural`). |
| TanStack Query | `^5.100` | |
| Testing Library / vite-plugin-pwa | `^16.3 / ^1.3` | |
| Package manager | **bun ≥ 1.3** | bun's resolver handles platform-specific packages more robustly than npm against a misconfigured mirror. |

Whenever you touch `backend/go.mod` or `web/package.json`, re-check that all listed deps are still on latest stable; bump if minor/patch, evaluate breaking changes if major.

## 2. Always write tests — coverage gate is 85%

For every new function/handler/component/hook: write the test in the same change. Backend = `testify` + `testcontainers-go` (build-tag `integration`). Frontend = `Vitest` + `@testing-library/react` with axios mocked via `src/test/server.ts` (keep in sync with backend changes).

**Coverage thresholds (enforced in CI/Makefile):** ≥85% statements, ≥80% branches (frontend). Excluded: `cmd/server/main.go`, `internal/db/db.go`, `internal/testdb/`, `src/main.tsx`, `src/theme/**`, `src/test/**`, `src/api/client.ts`.

```bash
make test-backend / test-integration / coverage-backend
cd web && bun run test / coverage
```

Two Makefile gotchas (both burned in): `-covermode=atomic` (default deflates under `-coverpkg`) and `-count=1` (without it, cached test profiles silently show old coverage).

## 3. Always update documentation when behavior changes

| Change to … | Update … |
|---|---|
| Feature scope, goals, MVP boundary | `docs/VISION.md` |
| API surface, data model, stack, ADRs | `docs/ARCHITECTURE.md` |
| Task done / lessons learned / followups | `docs/TASKS.md` (append to "Log de conclusão") |
| Stack version bump | `docs/ARCHITECTURE.md` + this `CLAUDE.md` §1 table + `README.md` stack line |
| **Any user-visible feature, flow, quickstart, smoke test, shortcut, stack version, or screenshot** | **`README.md` (source) AND `README.pt-BR.md` (mirror — keep in parity)** |
| Browser extension behavior | `extension/README.md` |
| Database schema (migration) | `docs/ARCHITECTURE.md` (data model) + comment block at top of `.up.sql` |

**The README is NOT optional.** Treat `README.md` as part of the product surface: if a change alters what a user sees or does, or what the stack is, the README MUST be updated in the same change — and the `README.pt-BR.md` mirror kept in sync. Before declaring done, re-read the README sections your change touches and confirm none went stale (versions, feature table, shortcuts, smoke test, screenshots). A change that ships code but skips doc updates — README included — is **incomplete**.

## 4. Data invariants — what must always hold

Schema + behavior contracts. Tests lock these.

- **`tag.name` is unique** (DB + `tag_name_taken` 409 on conflict).
- **`tag.color` / `folder.color` are CSS strings** validated by `internal/pkg/cssvalid` — only hex (`#abc`/`#abcd`/`#aabbcc`/`#aabbccdd`) or `linear-gradient(135deg, #hex, #hex)`. Frontend `web/src/lib/tagColor.ts` is the SINGLE parser (use `primaryColor(c)` for `color:`/`color-mix(…)` since those don't accept gradients). Without `cssvalid`, `red url("https://evil/exfil")` turns every chip render into a tracking pixel.
- **`link.url` is unique** (mig 000002). UNIQUE violations are **409 `url_taken`, never 500** — `internal/links/repository.go` uses `errors.As(*pgconn.PgError)` + `ConstraintName` match (string-match on wrapped messages would silently break behind any layer that drops `Unwrap`).
- **`link.slug` is NOT NULL UNIQUE, lowercase + hyphenated** (mig 000009) with CHECK `^[a-z0-9]+(-[a-z0-9]+)*$ AND NOT all-numeric` so `/go/42` always resolves to id 42. Auto-derived from `title` via `Slugify`; user can override in `LinkDialog`. Resolution in `redirect.handler`: int parse → ID lookup → slug lookup → 404. Backup/import/export propagate slug end-to-end (`uniqueLinkSlug`, `nextAvailableSlug` add `-2`/`-3` on conflict).
- **`click_log` is the single source of truth for clicks.** `link.click_count`/`last_clicked_at` columns no longer exist (mig 000006); both are derived via `LEFT JOIN LATERAL`. `/go/{id-or-slug}` is the **only** path that INSERTs into `click_log`, inside a tx that also verifies the link exists (404 otherwise) — never an UPDATE on `link`.
- **`link_tag` is the only place link↔tag lives.** No denormalization. M:N is mutated only through `links` handlers (Create/Update with `tag_ids`). Tag deletion cascades to `link_tag` (FK `ON DELETE CASCADE`); links survive.
- **`link_tag` and `click_log` are polymorphic (mig 000014) — shared by `link` and `note` via an `entity_kind ∈ {'link','note'}` discriminator on `entity_id`.** Every query against either table **MUST filter `entity_kind`**: link ids and note ids occupy the same numeric space, so an unscoped join can silently attach a note's tag/click to an unrelated link (or vice versa) that happens to share the same id — `TestCrossContamination_LinkAndNoteRowsDoNotLeak` (`internal/notes`) locks this. The FK from these tables to `link(id)` was dropped when they were polymorphized (a polymorphic column can't reference two tables), so cascade-on-delete is **app-level**: `links.Repository.Delete` and `notes.Repository.Delete` each delete their own `link_tag`/`click_log` rows inside the same tx as the entity row delete — never rely on `ON DELETE CASCADE` for either table again. Detail: ADR-27.
- **`preview_status ∈ {pending, ok, failed}`.** Preview worker is the only writer (`internal/preview`). Handlers never set it directly. **Manual upload short-circuits** the worker: `UpdateOGImage` sets `og_image_url`, `preview_status='ok'`, `preview_error=NULL` atomically; worker's `process()` checks at the top and skips both HTML fetch and screenshot fallback if `og_image_url` is non-empty.
- **Folders are 1:N exclusive AND nestable.** A link belongs to at most one folder via `link.folder_id`. Folders nest via `folder.parent_id` (self-FK). Both FKs `ON DELETE SET NULL` — deleting a folder promotes children to root. `?cascade=1` recurses via CTE through the whole subtree (existing `ON DELETE CASCADE` on `link_tag`/`click_log` cleans up). `folder.name` is NOT unique. Detail: ADR-19.
- **Home view excludes links inside folders.** `GET /api/links?ungrouped=1` returns `folder_id IS NULL` only. A link never appears in two places.
- **Tag filter and folder scope compose via AND.** Inside a folder, selecting a tag narrows that folder's content (`folder_id = X AND tag_id IN (...)`). Sidebar stays interactive — backend already supports the composition.
- **Internal IDs never appear in the URL.** Folder navigation lives in component state only — no `?folder=N`, no `/folder/:id`. Same for tooltips: `/go/{id-or-slug}` is implementation detail, UI label is just "Acessar". The slug IS exposed in `LinkDialog` (the user owns it as the share-friendly path).
- **Folders come BEFORE links in the grid except in alpha sort.** Default (Novos / Top / Recentes) renders `folders.map(...)` first, then `links.map(...)`. `alpha`/`alpha_desc` interleave by name so alphabetical order is honest.
- **viewMode + foldersCompact are per-context.** Persisted under `foldex.viewMode.map` and `foldex.foldersCompact.map` keyed by `home` or `folder.<id>`. Default `cards` / `false`. Home `useEffect` prunes orphan `folder.<id>` keys from BOTH maps on the same pass — never let them drift.
- **FolderCard `compact` mode + RapidView popover.** Compact hides the 2×2 preview and shows a thin strip. Hover/focus on the title mounts `FolderRapidView` (portal) listing `preview_folders` then `preview_links` from the existing `useFolders` payload — **no extra API call**. Cap 10 items + `+N more` footer (`link_count + folder_count − rows.length`). Empty folder = no popover. Show delay 220 ms.
- **Drag-and-drop wiring.** `LinkCard` is the only `draggable`; payload `application/x-foldex-link` carries the link id. `FolderCard` accepts → `onDropLink(linkId, folderId)`. `LinkCard` accepts → `onMergeWith(sourceId, targetId)`. Mutations live in `App.tsx`; cards stay UI-only. Same-card drops are no-ops.
- **Imports are idempotent by URL.** Re-importing the same `bookmarks.html` produces `skipped` matches. When JSON export carries `click_count`, importer materializes that many `click_log` rows stamped at the link's `created_at` — only on fresh insert.
- **Image input has a 50 MP decode cap.** `imageopt.Optimize` calls `image.DecodeConfig` before `Decode` and refuses with `ErrTooLarge` if `width × height > 50_000_000`. Without this, a ~30 KB PNG declaring 60000×60000 allocates ~14 GB. Upload entry point also caps body at 5 MiB — both caps must stay.
- **Uploads and screenshots are always re-encoded via `internal/imageopt`** (decode → downscale Catmull-Rom ≤1024 px → composite over white → JPEG q82). Exception: source already JPEG AND no resize AND re-encode came out larger → keep original (no-regression). PNG/GIF/WebP **always** re-encoded; canonical key for new files is `{prefix}/{id}.jpg`. Animated GIFs collapse to first frame.
- **Old extensions are purged on re-upload.** When a new upload writes `{prefix}/{id}.jpg`, handlers fire idempotent `DeleteObject` for sibling extensions (`.png`/`.gif`/`.webp`). Pre-deploy files in MinIO are NOT backfilled — `ProxyFile` keeps serving legacy formats. `Uploader` interface (both `internal/links` and `internal/preview`) **requires `DeleteObject`** — don't drop it without also removing purge calls.
- **IMDS (`169.254.169.254`) is always blocked** by the preview fetcher (no env opt-out). `PREVIEW_STRICT_SSRF=1` *additionally* blocks loopback, RFC1918, link-local, IPv6 ULA. Default permissive because intranet is foldex's primary use case (ADR-12).
- **SSRF dialer is checked twice.** `preview.safeDialer.DialContext` runs `LookupIP` + IMDS/private guard pre-dial AND `conn.RemoteAddr().(*net.TCPAddr)` post-dial. The pre-dial leg is fast-fail; the post-dial leg defeats DNS rebinding. Post-dial type-assert is fail-closed.
- **Screenshot is a FALLBACK, never default.** `preview.Worker.maybeScreenshot` runs only when **all** of: (a) HTML fetch returned empty `og:image`, (b) link still has no `og_image_url`, (c) `preview.IsPublicURL(url)` is true, (d) worker was wired with `WithScreenshotFallback(sc, up)`. Decode-bomb errors abort the fallback (don't write raw PNG to MinIO). Detail: ADR-16.
- **Manual screenshot endpoint applies the same SSRF gate.** `internal/links/screenshot_handler.go:CaptureAndStore` takes a `links.URLPolicy` (wired to `preview.IsPublicURL`). Rejects non-http(s) → 400 `invalid_scheme`; private/IMDS → 400 `private_target`. **Nil policy is a config error, not SSRF rejection**: 500 `policy_unconfigured`. Router additionally panics at boot if `Screenshotter != nil && ScreenshotURL == nil` — surface wiring errors at deploy, not per-request.
- **`GET /api/links/url-metadata` reuses the preview `Fetcher` — same SSRF posture, no duplicate HTTP client.** `internal/links/metadata_handler.go` injects `links.MetadataFetcher` (adapter over `*preview.Fetcher` constructed in `main.go`). Endpoint rejects non-http(s) → 400 `invalid_scheme`, URL > 2 KiB → 400 `invalid_url`; every fetch failure (DNS, SSRF, TLS, 4xx, timeout) collapses to 502 `fetch_failed` with no internal text. Returned fields are truncated via UTF-8-aware `truncateRunes`: **title at `links.MaxTitleBytes`** (single source of truth — same constant the Create/Update DTOs enforce, so a pre-filled title always passes Save), description at 4 KiB, favicon/og_image URLs at 2 KiB.
- **oEmbed enrichment reuses the SAME `preview.Fetcher` client — never a second HTTP stack.** `internal/preview/oembed.go:fetchOEmbed`. The discovery `OEmbedURL` (from `<link rel="alternate" type="application/json+oembed">`) is attacker-controlled HTML, so `fetchOEmbed` enforces `scheme ∈ {http, https}` BEFORE building the request (Go's default transport would read `file:///…` since the IP-level dialer never fires for non-http(s) schemes). Merge contract: HTML always wins; oEmbed only fills empty fields. Detail: ADR-25.
- **JSON request bodies are capped at 64 KiB.** Every POST/PATCH handler in `links`/`folders`/`tags` wraps `r.Body` with `http.MaxBytesReader` before `Decode`. Realistic payloads are well under 4 KiB; surface is hostile.
- **Stats handler clamps every numeric knob via `clampInt`.** `?days` ∈ [1,365], `?limit` ∈ [1,100]. Without the cap, `?days=2147483647` lands in a `generate_series(...)` and the planner attempts it.
- **Boot refuses the insecure-by-default combo.** `config.validateSecureDefaults` errors if `BACKEND_BIND` is non-loopback AND `SHARED_SECRET == ""` AND `CORS_ORIGINS` includes `*`. Defaults are fine for single-user/localhost; flipping any one forces the operator to set at least one knob.
- **nginx ships defense-in-depth headers** (all with `always` for 4xx/5xx): HSTS, X-Frame-Options DENY, X-Content-Type-Options nosniff, Referrer-Policy no-referrer, Permissions-Policy, strict CSP. CSP allows `'unsafe-inline'` ONLY for style-src (emotion runtime); script-src stays strict.
- **All CI actions are SHA-pinned, not tag-pinned.** Each `uses:` line carries a 40-char commit SHA + `# vX.Y.Z` comment for Dependabot. Major tags are mutable; a compromised upstream could swap silently. `govulncheck` + `bun audit` run as informational steps (`continue-on-error: true`) — surface CVEs without gating mid-flight releases.
- **`SHARED_SECRET`, when set, gates all `/api/*` (including `/api/push/vapid-key`).** `/healthz` and `/go/{id-or-slug}` stay public — operational endpoints.
- **`.env` is never committed.** `.env.example` is the only canonical source.
- **Postgres credentials live in `POSTGRES_*` only — `DB_URL` is derived.** `docker-compose.yml` + `backend/Makefile` build the DSN. Override `DB_URL` only for external DBs (TLS, schema). If you change `POSTGRES_USER`/`PASSWORD` in `.env`, **delete any `DB_URL=` line**. `POSTGRES_HOST` accepts `db`/`localhost`/`host.docker.internal`/external — backend container's `extra_hosts` aliases `localhost` + `host.docker.internal` to host gateway.
- **Backup is a complete DB + MinIO snapshot ZIP** (`manifest.json` `Store`-compressed for client-side count read; `database.json` + `files/`; SHA-256 checksums; counts). Export runs under `REPEATABLE READ`. Detail: ADR-20 + `docs/SDD-BACKUP-RESTORE.md`.
- **Backup export streams; restore streams to a temp file.** `Export(ctx, w, onCountsReady)` fires the callback (handler flushes `X-Foldex-Backup-*` headers) before streaming the ZIP to `w`; `readZipFromRequest` `io.Copy`s the upload to `os.CreateTemp` and opens via `zip.NewReader`, cleaning up on success and failure. **Never reintroduce `bytes.Buffer` in either path** — a 2 GiB restore would peg the heap.
- **Backup restore is idempotent by default, never atomic across DB+MinIO.** Three modes: `wipe` (TRUNCATE + DELETE prefix + restore with original IDs + bump sequences), `skip` (`ON CONFLICT DO NOTHING`; old→new id mapping for link_tags/click_logs re-key), `duplicate` (tags renamed `nome (N)`; folders always new; links with URL collision fall back to skip + warning — URL is UNIQUE so true duplication would violate the invariant). DB phase is single tx; files post-commit. Crash between the two = re-run with same zip converges.
- **Backup endpoints require MinIO.** `POST /api/backup/*` are mounted only when storage client came up. Without MinIO the backup would be silently incomplete; routes don't exist at all (404, not partial 200).
- **`preview.Worker.Enqueue` returns an error** (`ErrQueueFull` / `ErrStopped`). HTTP handlers and importer treat enqueue as fire-and-forget with `_ = w.Enqueue(id)` — the link row already exists and `requeuePending` picks stragglers up on next boot. Stop ordering: set `stopped atomic.Bool` first, then cancel, then `wg.Wait` — never close the jobs channel.
- **The `foldex-web` image NEVER ships a private TLS key.** `entrypoint-certs.sh` uses a volume-mounted pair at `/etc/nginx/certs/{cert,key}.pem` OR generates a self-signed ephemeral pair on boot. Baking a key into a public image is HIGH-severity (Trivy/Scout flag) — operators pulling it would share the same private key. Local dev: `make up` bind-mounts `./web/certs` from the gitignored host directory.
- **The change-check worker reuses the preview `Fetcher` — never duplicate SSRF guards.** `internal/changecheck.New` accepts a `Fetcher` interface; `main.go` injects `preview.NewFetcher`. Adding a second HTTP client would silently fork the SSRF posture. Fingerprinter's feed fetch goes through the same `GetRaw`. Detail: ADR-23.
- **`link.last_fingerprint` is prefixed `feed:<hex>` or `content:<hex>`.** The prefix is the **strategy discriminator**: kind switch `content:` → `feed:` is treated as new baseline, NOT change. `worker.process` only fires push when `prevKind == newKind && prevHash != newHash`. Without it, the first feed-augmented scan would always fire a false-positive push.
- **First observation never counts as a change.** `last_fingerprint IS NULL` → grave the new fingerprint without bumping `last_change_detected_at`. Without this, every newly opted-in link sends a "this page changed" push on its first scan.
- **Opt-out clears the full change-check column group.** When `LinkUpdate.CheckInterval` is `null` (tri-state), the repository writes `check_interval = NULL` AND `last_checked_at = NULL`, `last_fingerprint = NULL`, `last_change_detected_at = NULL`, `change_seen_at = NULL` in the same statement. Re-opting-in would otherwise replay a stale badge.
- **Manual `/api/links/{id}/seen-change` is a no-op when `last_change_detected_at IS NULL`** (404). Prevents out-of-band POSTs from permanently suppressing the next genuine detection.
- **`push_subscription.endpoint` is UNIQUE; upsert is the only INSERT path.** `INSERT … ON CONFLICT (endpoint) DO UPDATE SET p256dh, auth` — browser may renew with rotated keys; track those rather than accumulate dead duplicates. Single-user: no `user_id` (revisit when multi-user lands).
- **404/410 from the push service removes the subscription row** (RFC 8030 §7.3). Other non-2xx are logged, row stays. Transport errors NEVER delete — a transient network blip would wipe live subscriptions.
- **VAPID private key is `0o600` and never baked into the image.** `internal/push.LoadOrGenerate` writes to `VAPID_STATE_PATH` (default `/data/vapid.json`) with explicit `os.WriteFile(..., 0o600)` — umask not trusted. Volume `foldex-data` persists; pin `VAPID_*` in `.env` for stable subscriptions across recreations.
- **Web Push send is fire-and-forget from the change-check worker.** `worker.process` launches `sender.Notify` in a goroutine with fresh `context.Background()` + 15s timeout — slow push service can't rollback the durable `RecordCheckResult`. Detail: ADR-24.
- **Service Worker is hand-rolled — no `workbox-*` runtime deps.** `web/src/sw.ts` uses Cache API + raw fetch directly. `vite-plugin-pwa` with `strategies: 'injectManifest'` injects `__WB_MANIFEST` at build; runtime caching + Web Push event listeners (`push`, `notificationclick`) are hand-written. Adding workbox imports later requires bumping `bun.lock`.

## 5. UI/UX invariants — interaction contracts

Part of the product contract, not nice-to-haves.

- **Every dialog closes on `Esc`** via `useEscape(onClose, open)`. **No `window.confirm()`** — always `useConfirm({ title, message, destructive })`. Focus trap via `useFocusTrap(ref, open)` on every dialog (Tab/Shift+Tab cycle inside, focus restored on close).
- **Destructive actions** render with `fx-confirm-btn-danger` + trash icon + monospace `⚠ AÇÃO DESTRUTIVA` kicker. Cancel = ghost.
- **Tag creation inside New Link dialog is deferred until save.** Pending tags use `id: 0`. The link's submit handler creates real tags first, then saves. Pending chips let the user cycle colors by clicking the dot (palette in `LinkDialog.tsx:INLINE_PALETTE`, Tailwind 500-weight to minimize collisions).
- **LinkDialog auto-fills Title/Description from the URL after a 500 ms debounce** — only on **create** (edit mode skips entirely; the link already has its own copy), only when the field is **empty** (`setTitle((cur) => cur.trim() ? cur : data.title)` — user input always wins), and only when `looksLikeUrl(url)` passes. Effect uses `AbortController` so a fresh keystroke cancels the previous in-flight fetch AND unmounting the dialog aborts cleanly (no setState on dead component). Failure is silent (no toast, no submit block). Image stays async via the preview worker.
- **Tooltips are CSS-only via `data-tooltip` (+ optional `data-tooltip-side`)** rendered through `<TooltipPortal>` (portal to `document.body`, viewport-clamped). Never use native `title` on visible UI. Keep `aria-label` for a11y.
- **Sidebar stays clean** — no per-row edit/delete. Editing goes through `TagManagerDialog` (opened by "Gerenciar tags" footer button). Collapsed sidebar = 44 px rail with expand chevron; state in `localStorage["foldex.sidebar.collapsed"]`.
- **Pinned links always come first.** `ORDER BY l.pinned DESC, ...` applies in every sort mode. Card shows gradient pin badge (always visible when pinned, on-hover when not).
- **Grid is row-major and density is user-controlled.** `.fx-grid` / `.fx-pingrid` use CSS Grid (never `column-count`). Density picker (3/5/8) lives in Topbar's `fx-viewseg`, visible only when `viewMode === 'cards'`, persisted in `localStorage["foldex.grid.cols"]` (default 5). Mobile breakpoints (≤980px / ≤640px) set a **lower cap** only.
- **Card preview area has a fixed height** (`.fx-preview { height: 150px; min/max-height: 150px }`). Images use `object-fit: scale-down` so large shrink to fit, small render natural size.
- **"preview falhou" hides when an image is already present.** Gated by `link.preview_status === 'failed' && !link.og_image_url`. With a working image, flagging "failed" alongside it is noise.
- **`localStorage` is the persistence layer for UI prefs** under `foldex.*` namespace, with SSR-safe `typeof localStorage !== 'undefined'` guard in the initializer.
- **`/go/{id-or-slug}` button label says "Acessar"** — never the implementation path.
- **All keyboard shortcuts are Alt-based.** `⌥K` palette, `⌥N` new link, `⌥F` new folder. Browsers swallow most `⌘`-modifier combos (⌘K, ⌘N, ⌘P). Any new SPA shortcut MUST use `alt+<key>`.
- **Pasting a URL anywhere opens the New Link dialog with it pre-filled.** Document-level `paste` listener (`web/src/hooks/usePasteUrl.ts`) uses `web/src/lib/url.ts:looksLikeUrl` (accepts `http(s)?://`, `ftp://`, `file://`, bare `example.com/x`; rejects words, numbers, multi-word, `mailto:`/`tel:`/`javascript:`). No-op when target is editable (INPUT/TEXTAREA/SELECT/contentEditable) or any `.fx-overlay` is mounted. `pastedUrl` MUST be cleared on close so subsequent `+ New link` clicks start empty.
- **Dark mode is neutral charcoal/slate, not purple.** Only the accent (`--fx-accent` indigo `#8B85FF`) carries hue.
- **Backup mode picker uses dual visual encoding** for `wipe`: red border + red background on the option AND `fx-confirm-btn-danger` on submit AND literal `⚠` prefix on the label. `skip` and `duplicate` use indigo accent. The submit gradient is what makes destructive intent unmissable.
- **Backup history persists in `localStorage["foldex.backups"]`** (array of `{id, created_at, duration_ms, size_bytes, counts}`, capped at 10). New entries prepend; other tabs sync via `storage` event.
- **Locale picker lives in the topbar.** Persists to `localStorage["foldex.locale"]`. Default detection: `navigator.language` falling back to `en`. Adding a new locale = drop JSON in `web/src/i18n/locales/`, list in `SUPPORTED_LOCALES`, populate every key from `en.json` (source of truth).
- **Monitored / unseen-change UI.** Cards with `check_interval IS NOT NULL` always render a "Monitored" chip. Cards with unseen `last_change_detected_at` render an amber badge (`fx-card-update-alert` + bell icon); clicking it calls `useMarkChangeSeen` optimistically. Sidebar's "Recent updates" section refetches every 60 s, cap 10 items.
- **Push subscription UI is a bell in the Topbar.** Four states: unsupported / denied / off / on. Hooks `useWebPush`/`useSubscribePush`/`useUnsubscribePush` wrap the `PushManager` plumbing — components never touch the API directly.
- **Mobile responsiveness** (3 breakpoints in `web/src/styles/foldex.css`): ≤980px / ≤640px = grid caps to 2/1 cols; ≤768px = topbar single-row, sidebar off-canvas, FAB for new-link; ≤600px = dialogs full-screen, inputs min-height 44px, safe-area inset bottom. **Gotcha**: `overrides.css` loads after `foldex.css` — every desktop-only rule there MUST be wrapped in `@media (min-width: 769px)` or mobile breaks silently. Detail: ADR-22.

## 6. Definition of Done — every change must check all boxes

Before announcing "done", verify each. If any fails, the change is not done.

- [ ] Code compiles cleanly (`go build ./...`, `bun run build`).
- [ ] `go vet ./...` is silent.
- [ ] `bun run typecheck` is silent.
- [ ] Tests added for new code paths (success + at least one error path).
- [ ] Existing tests still pass (`make test-integration` for backend, `bun run test` for web).
- [ ] Coverage ≥ 85% (`make coverage-backend`, `bun run coverage`).
- [ ] Docs updated per §3 matrix.
- [ ] **`README.md` reviewed and updated** (and `README.pt-BR.md` mirror) — see §3. Any user-visible/stack/feature change MUST land in the README in the same change; re-read the touched sections to confirm nothing went stale.
- [ ] Versions still on latest stable per §1.
- [ ] Invariants in §4 and §5 not violated.
- [ ] If a migration was added: applied to the running Postgres and backend recompiled to use the new schema.
- [ ] User-visible UI changes manually validated in a real browser when behavior changes (not just type-check).
- [ ] **Post-implementation agent sweep run** — see §9. Mandatory for every implementation task. **5 agents** (Code Review, Code Quality, Test Quality, Performance, Security) in parallel — never serialize, never skip "because the change is small."
- [ ] **`graphify update .` run after any code change** — keeps `graphify-out/` in sync with the AST. Free (no API cost). Skipping means future codebase queries return stale results.
- [ ] **Semver bump shipped** — see §6.2. `:latest` is not a release; only a `vX.Y.Z` tag is.

### 6.1 Pre-push gate — MANDATORY before ANY commit / push / PR

Before `git commit` / `git push` / `gh pr create`, run the **exact** CI commands locally and confirm green. NEVER push relying on "the CI will catch it" — wastes minutes per round-trip AND consumes GitHub Actions billing.

If the change touches `.github/workflows/*.yml`, run the **new** commands locally (not what the workflow used to run). Typical failure mode: workflow swaps `bun run test` for `bun run coverage`, you forget to re-validate, push, threshold gate trips. Process bug, not CI bug.

```bash
# Backend
( cd backend && make fmt-check && go vet ./... && make coverage-run )
# Frontend
( cd web && bun run typecheck && bun run coverage:nogate )
```

If the workflow file itself changed, also `grep -E '^\s+run:' .github/workflows/ci.yml` and execute each `run:` line locally. Exception: secrets-gated or matrix-arm64-specific steps — document in PR description and ask the user to confirm CI is acceptable before merge.

### 6.2 Version bump — MANDATORY after every merge to main

Every merge ships code; every shipment gets a version. `:latest` keeps moving but **a moving tag is not a release** — operators can't pin to it, rollbacks have nothing to roll back to, regressions can't be bisected without `vX.Y.Z` tags.

| Merged work | Command | Example |
|---|---|---|
| feat (backwards-compat) | `make release-minor` | 1.0.8 → 1.1.0 |
| fix / chore / ci / docs | `make release-patch` | 1.0.8 → 1.0.9 |
| breaking API/schema | `make release-major` | 1.0.8 → 2.0.0 |
| mixed (feat + fix same window) | `make release-minor` (features dominate) | |

`make release-X` runs `scripts/release.sh` (refuses dirty tree / off-main). Bumps `web/package.json` + `extension/manifest.json`, commits, tags `vX.Y.Z`, prompts to push. Pushing the tag triggers `release.yml` (it watches `push: main` + `tags: ['v*']`) which publishes `:X.Y.Z`, `:X.Y`, `:X`, `:latest` for both images — **`docker/metadata-action` strips the leading `v`**, so the git tag is `vX.Y.Z` but the Docker image tags carry NO `v` (pin `FOLDEX_VERSION=X.Y.Z`). `ci.yml` is the PR gate (`on: pull_request` only) — it does NOT run on push to main/tags, so a merge never re-runs the test suite; the release trusts the gate that already passed on the PR.

After the bump, surface the new pin to the user: `FOLDEX_VERSION=1.2.0` in `.env` (no `v` — Docker image tags drop it even though the git tag is `v1.2.0`).

If the user explicitly opts out for the current session ("don't bump yet, batching the next 3 PRs"), record the deferral in the session log and resume the policy on the next merge. Default is bump-every-merge — silence is not opt-out.

## 7. Style choices — the project's defaults

- **Backend:** Chi router, pgx + pgxpool, slog. No ORMs, no global state, no service locators.
- **Frontend:** Plain React (no MUI in render). TanStack Query for server state, no Redux. axios as HTTP client. `react-hotkeys-hook` for shortcuts. **i18n via `react-i18next`** — every visible string through `t('key')`, mirrored across `en/pt/es`.
- **Migrations:** `golang-migrate`, `000NNN_*.up/down.sql` only. Each migration reversible (real `.down.sql` or explicit `SELECT 1;` with comment).
- **Errors:** uniform JSON envelope `{ "error": { "code", "message" } }`. Backend handlers go through `httperr.Write`. Never leak `pgx` errors to clients.
- **Logs:** structured (slog JSON). No `fmt.Println`.
- **Comments:** only when *why* is non-obvious. No "what" comments, no task references, no commit ids.

## 8. Architecture in one paragraph

Two docker-compose projects: **`docker-compose.db.yml`** brings up Postgres on the shared `foldex` Docker network. **`docker-compose.yml`** brings up the backend (Go + Chi on `:9089`), the web (nginx serving Vite build on `:9088`/`:9444` for HTTPS), and the `foldex-data` volume for VAPID + future stateful goodies. Backend talks to db; web proxies `/api` and `/go` through nginx. The preview worker + change-check worker + push sender all run in-process inside the backend as goroutine pools. Schema: `tag`, `link` (with slug, pinned, change-check columns), `note` (slug/title/folder/pinned shape like `link`, plus `body_html`/`body_text`), `link_tag` (M:N, polymorphic via `entity_kind` — shared by both `link` and `note`), `folder` (nested), `click_log` (single source of truth for clicks/views, polymorphic via `entity_kind`), `push_subscription`. `internal/entries` is a read-only `UNION ALL` projection over `link`+`note` (`GET /api/entries`) that feeds the interleaved home/folder grid — see ADR-27.

## 9. Post-implementation agent sweep — MANDATORY for every change

Before declaring any implementation task done (and before opening a PR), spawn the **five agents** below **in parallel** via the `Agent` tool and surface every HIGH finding inline. Skipping the sweep is not allowed — it is part of the Definition of Done in §6.

**The five agents** (always all five, parallel, single tool-use block), split by concern — Code Review owns *"correct & coherent?"*, Code Quality owns *"clean & maintainable?"*; never merge or skip one:

1. **Code Review** — architectural coherence vs §4/§5, React/backend idiomaticity, CI correctness.
2. **Code Quality** — dirty code, duplication, comment hygiene (§7), cyclomatic/cognitive complexity, clean-architecture/layering.
3. **Test Quality** — new paths tested (positive/negative/edge), missing cases, antipatterns, coverage gap.
4. **Performance** — re-render storms, memoization, debounce, network waste, bundle, SQL N+1 / missing index.
5. **Security** — XSS / DoS / secret-leak / injection / supply-chain, runtime AND CI.

**Canonical prompts (with each agent's exact "does NOT review" scope) live in [`AGENTS.md`](./AGENTS.md)** — copy verbatim, substitute only the session-scope placeholder.

**Workflow:**

1. After typecheck + tests + coverage pass, call `Agent(...)` five times in one tool-use block — one per agent — with `run_in_background: true` and the session scope filled in.
2. Continue with docs / commit prep / `graphify update .` while they run; harness notifies on completion. Do NOT sleep or poll.
3. When each agent reports back, surface findings to the user. **Treat every HIGH as a blocker** — fix in this session, then re-run the relevant agent against the patched diff. MEDIUM and LOW go to the PR description (or get fixed if cheap).
4. Only declare done after the five reports are visible AND every HIGH is resolved AND `graphify update .` completed.

The sweep is the safety net for changes that *look* small — that's exactly when it's skipped and exactly when it shouldn't be.

---

> Whenever this file conflicts with another instruction in the project (README, ARCHITECTURE), this file wins — update the other doc.
