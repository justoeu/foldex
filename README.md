# Foldex

<p align="center">
  <img src="docs/assets/hero.svg" alt="foldex — self-hosted bookmark manager" width="100%"/>
</p>

> Self-hosted bookmark manager with rich tagging, nestable folders, click tracking, visual URL previews, full backup, and a browser extension.

Foldex is a personal "smart bookmarks bar" — it stores links organized by **nestable folders + M:N tags**, shows **what you actually click** (telemetry via `/go/:id`), captures every URL visually (OG image / favicon / screenshot fallback), and runs **entirely on your own machine** (Postgres + MinIO + Go + React in containers).

> Stack: **Go 1.26 · PostgreSQL 16 · MinIO · Vite 8 + React 19 + bun · Vitest 4**. Versioning policy + invariants in [`CLAUDE.md`](CLAUDE.md).

---

## Why foldex instead of the browser's built-in bookmarks?

Native bookmarks are fine for "save a page quickly and forget it". Once you pass 50 links, the friction starts to hurt. Foldex addresses each pain point:

| Native-bookmark pain                                                            | How foldex solves it                                                                                                            |
| ------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| **Locked to one browser.** Chrome ↔ Safari ↔ Firefox = 3 silos. Sync requires a vendor account. | Your own server. Reach it from any browser, on any machine on your network. Data lives in a Postgres **you** control. |
| **Tree-only.** A bookmark lives in ONE folder. Want "work + ai + notebookLLM"? You triplicate. | **M:N tags** (a link can carry N labels) **+ 1:N nestable folders** (iPhone-style containment). The two systems coexist. |
| **Zero telemetry.** You "favorite" 200 links and use 8. You don't know which.   | Every navigation goes through `/go/:id` which inserts into `click_log`. Stats page shows clicks per day, top hosts, top links (last 30d), tag distribution. |
| **Preview = 16×16 favicon.** A gray list with tiny icons.                       | Visual card with OG image. If the page has none, foldex **captures a screenshot** automatically (headless Chromium → MinIO). You can also upload any image manually. |
| **Weak search.** Title/URL match only.                                          | Full-text search via Postgres `pg_trgm` over title + URL + description. Composes with tag filter (AND-multi-tag) and folder scope. |
| **Backup = opaque Netscape file.** Images? Clicks? Hierarchy? All lost.         | Single backup ZIP with `manifest.json` + `database.json` (5 tables) + **every MinIO image**. Lossless round-trip, SHA-256 checksum verification, 3 conflict modes (wipe/skip/duplicate). |
| **Fixed shortcuts.** Cmd+D opens the browser's native dialog.                   | MV3 extension + Alt-K (palette), Alt-N (new link), Alt-F (new folder). iPhone-style drag-and-drop between cards/folders. |
| **Vendor lock-in.** Leaving Chrome = export HTML + lose metadata.               | Export to **Netscape HTML** (universal compat) **OR** JSON v2 (with folders + click_count) **OR** full backup ZIP. Importer accepts all three. |
| **Pinned/favorites = a tiny separate folder.** Visual only.                     | `pinned` is a real column on the table. `ORDER BY pinned DESC, …` applies in every sort mode. Gradient badge always visible. |
| **Data embedded in the browser.** Switched machines? Reinstalled Chrome? Pray. | Postgres + MinIO in containers. `make up` on a new machine and your backup ZIP restores everything (DB + images) in ~minutes. |

### Real scenarios that flipped the switch (native bookmarks → foldex)

- **"Which dashboards am I actually using?"** → the stats page surfaces top hosts and top links over 30 days. Drop the ones at 0 clicks.
- **"I want to share `localhost:9089/go/42` with the team."** → every URL gets a stable alias `/go/:id` that redirects + logs the click.
- **"Switch machines without losing anything."** → 1 button in the UI generates the full backup ZIP. Another button on the new machine restores with `mode=wipe`.
- **"The same link lives in 3 contexts (work + ai + architecture)."** → 3 tags. It shows up in all 3 filters.
- **"I want to know visually which link is which before clicking."** → every card shows an OG/screenshot/upload preview at 150px.

### When foldex is overkill

If you have fewer than 30 links saved and use **a single browser on a single machine**, native bookmarks are simpler. Foldex starts paying off once you need cross-browser access, telemetry, or real organization across more than one dimension.

---

## Quickstart

```bash
cp .env.example .env
make up                 # builds Postgres (separate compose) + backend + web on 127.0.0.1
make migrate-up         # applies SQL migrations
make seed               # optional: sample tags + links

open http://localhost:9088
```

### HTTPS (local dev) via mkcert

Nginx serves the web container over HTTPS on `:443` inside the container,
exposed on the host at `WEB_HTTPS_PORT` (default **9444**). The cert is signed
by a local CA — to make the browser trust it without warnings, install
[`mkcert`](https://github.com/FiloSottile/mkcert) once on the host and emit
the pair into `web/certs/`:

```bash
brew install mkcert nss      # nss is only needed for Firefox
mkcert -install              # installs the local CA into the system trust store
                             # (prompts for your sudo password + a Keychain
                             # confirmation dialog on macOS)

mkdir -p web/certs
mkcert -cert-file web/certs/cert.pem \
       -key-file  web/certs/key.pem \
       localhost 127.0.0.1 ::1 host.docker.internal

make up                       # rebuilds the web image with the new certs baked in
open https://localhost:9444   # 9444 = WEB_HTTPS_PORT; 9088 (WEB_PORT) is HTTP→HTTPS redirect
```

The `cert.pem` and `key.pem` files are **gitignored** — generate them locally,
never commit. Re-run `mkcert ...` (and `make up`) when you add a new hostname
(e.g. a `*.foldex.test` you point at `127.0.0.1`) or after re-installing the
local CA (`mkcert -install`) on a new machine.

> **Still seeing "Not Secure" in the browser?** It means the mkcert root CA
> is not in this machine's trust store (or it is, but the cert was signed by
> a different CA — common when you move the project between machines).
> Run `mkcert -install` and re-emit the pem files using the block above; then
> `make up` to rebuild the nginx image with the fresh certs baked in.

> **Reuse a Postgres you already run on your host.** Set `POSTGRES_HOST=host.docker.internal` in `.env` (and matching `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB`), skip `make db-up`, and run `make apps-up` directly. Migrations need to be applied against that DB by hand (or `make migrate-up` if the user/db exist).

## Stack layout

Postgres lives in `docker-compose.db.yml` (its own compose project). Backend + web live in `docker-compose.yml` and attach to the shared `foldex` Docker network so they reach Postgres by the name `db`. Useful targets (`make help`):

| Target | What |
|---|---|
| `make db-up` / `db-down` / `db-nuke` | manage Postgres only |
| `make apps-up` / `down` | manage backend + web only |
| `make up` / `stop-all` | full stack (Postgres + apps) |
| `make migrate-up` / `migrate-down` | apply / revert SQL migrations |
| `make psql` | shell into Postgres |
| `make logs` / `db-logs` | follow logs |

## Tests + coverage (gate: ≥ 85%)

```bash
make test-backend       # unit only (no Docker)
make test-integration   # unit + integration (Docker required)
make coverage-backend   # enforces 85% on backend
make coverage-web       # enforces 85% on frontend (Vitest)
make coverage-all       # both
```

Coverage rules and exclusions live in [`CLAUDE.md`](CLAUDE.md). Read it before opening a PR.

Other targets: `make logs`, `make psql`, `make healthz`, `make down`. See `make help`.

## Smoke test (sanity check after `make up`)

```bash
# 1. Backend up?
curl -s localhost:9089/healthz | jq .

# 2. Create a tag.
curl -s -X POST localhost:9089/api/tags \
  -H 'Content-Type: application/json' \
  -d '{"name":"jira","color":"#1f6feb","icon":"🪲"}' | jq .

# 3. Create a link tied to that tag (preview is enqueued async).
curl -s -X POST localhost:9089/api/links \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://news.ycombinator.com","title":"HN","tag_ids":[1]}' | jq .

# 4. Wait ~2s for the worker; then fetch — `preview_status` should be "ok".
sleep 3 && curl -s localhost:9089/api/links/1 | jq '.preview_status, .og_image_url'

# 5. Resolve the short link (302 + counter bump).
curl -sI localhost:9089/go/1 | head -3

# 6. Open the SPA and try ⌘K (search) / ⌘N (new link).
open http://localhost:9088
```

## Keyboard shortcuts (SPA)

| Shortcut         | Action                          |
|------------------|---------------------------------|
| `⌥K` / `Alt+K`   | Command palette (fuzzy search). `⌘K` conflicts with browsers' URL-bar focus. |
| `⌥N` / `Alt+N`   | New link (⌘N is hard-claimed by browser for "New window") |
| `⌥F` / `Alt+F`   | New folder (⌥P collided with other handlers; "F" for Folder) |
| `Esc`            | Close any open modal / exit folder view |
| `⌘Enter` (popup) | Save (in the browser extension) |

> **Convention**: every foldex shortcut is Alt-based. Browsers swallow most `⌘`-modifier combos (⌘K = focus URL bar, ⌘N = new window, ⌘P = print), so Alt-prefixed shortcuts are the only ones that reach the SPA reliably.

## Browser extension

A vanilla Manifest V3 extension lives in `extension/`. Load it as **unpacked** from `chrome://extensions` → Developer mode → Load unpacked → pick the `extension/` folder. Then click its icon on any tab and hit Save. See `extension/README.md`.

## Screenshots

<p align="center">
  <img src="docs/assets/home-empty.png" alt="Foldex home view — empty state showing the tag sidebar, topbar, and the CTA card to add the first link" width="100%"/>
</p>

<p align="center"><sub><em>Home view (no links yet) — tag sidebar on the left, topbar with search + filters, New link / New folder CTAs on the right, empty-state card inviting the first import.</em></sub></p>

> *Other captures pending:*
>
> - Populated home grid (cards + 3/5/8-column density)
> - Command palette (`⌥K`)
> - New link dialog with tag autocomplete
> - Import page (drag-drop) + preview with the mode picker
> - Stats page (KPIs, top hosts, tag distribution)
> - Extension popup

## Layout

| Path           | What |
|----------------|------|
| `backend/`     | Go service (Chi + pgx + Postgres 16) — REST API, redirect, preview worker |
| `web/`         | Vite + React + TypeScript SPA. CSS handoff (`styles/foldex.css`) + local `overrides.css`. |
| `extension/`   | Manifest V3 browser extension to capture the current tab |
| `docs/`        | SDD docs: `VISION.md`, `ARCHITECTURE.md`, `TASKS.md` |
| `scripts/`     | Seed + backup helpers |

## Backup & Restore

Full snapshot of the DB **and** the MinIO bucket into a single ZIP. Three endpoints:

```bash
# Generate — streams a ZIP. Headers expose counts + duration.
curl -OJ http://localhost:9089/api/backup
unzip -l foldex-backup-*.zip
#   manifest.json
#   database.json
#   files/screenshots/{id}.png
#   files/images/{id}.{ext}

# Validate (without applying)
curl -X POST -F file=@foldex-backup-*.zip \
  http://localhost:9089/api/backup/validate | jq

# Restore — 3 conflict modes
curl -X POST -F file=@foldex-backup-*.zip \
  'http://localhost:9089/api/backup/restore?mode=skip' | jq
#   mode=wipe       — TRUNCATE everything + restore with original IDs (DESTRUCTIVE)
#   mode=skip       — preserve existing (ON CONFLICT DO NOTHING; default)
#   mode=duplicate  — rename conflicting tags to "nome (2)"; folders always new;
#                     links with URL collision fall back to skip + warning
```

Via UI: open the **Import / Export** page → the right column hosts the **💾 Full backup** card. Drag a `.zip` onto it to review the validation summary and pick a mode in `BackupRestoreDialog`. History (last 10 backups: date, duration, size, counts) persists in `localStorage`. *(Note: UI strings still ship in Portuguese — see the i18n roadmap below.)*

Full design rationale: [docs/SDD-BACKUP-RESTORE.md](docs/SDD-BACKUP-RESTORE.md).

## Docs

- [Vision](docs/VISION.md) — problem, goals, success criteria
- [Architecture](docs/ARCHITECTURE.md) — stack, data model, API, ADRs
- [Tasks](docs/TASKS.md) — phased implementation log
- [SDD: Backup & Restore](docs/SDD-BACKUP-RESTORE.md) — DB + MinIO snapshot ZIP, conflict modes, validation flow

## License

Personal project. No license granted.
