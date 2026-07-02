# Foldex

<p align="right"><sub><strong>🇺🇸 English</strong> · <a href="./README.pt-BR.md">🇧🇷 Português</a></sub></p>

<p align="center">
  <img src="docs/assets/home-empty.png" alt="foldex — self-hosted bookmark manager (home view with empty state, tag sidebar, topbar with search + sort + density controls, New folder / New link CTAs)" width="100%"/>
</p>

> Self-hosted bookmark manager with rich tagging, nestable folders, click tracking, visual URL previews, **pastebin-style rich-text notes**, **per-link change detection + Web Push**, full backup, and a browser extension.

Foldex is a personal "smart bookmarks bar" — it stores links organized by **nestable folders + M:N tags**, shows **what you actually click** (telemetry via `/go/:id`), captures every URL visually (OG image / favicon / screenshot fallback), lets you jot down **rich-text notes** (Tiptap editor with inline images) that live in the same grid/search/tags/folders as links, **watches the pages you care about** (RSS/Atom feed fingerprint with content-hash fallback) and pings you via Web Push when they change, and runs **entirely on your own machine** (Postgres + MinIO + Go + React in containers).

> Stack: **Go 1.26 (Chi · pgx) · PostgreSQL 18 · MinIO · Vite 8 + React 19 + TypeScript + bun · TanStack Query · Tiptap 3 · react-i18next (en/pt/es) · Vitest 4**. Versioning policy + invariants in [`CLAUDE.md`](CLAUDE.md).

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
| **Vendor lock-in.** Leaving Chrome = export HTML + lose metadata.               | Export to **Netscape HTML** (universal compat) **OR** JSON v2 (with folders + click_count) **OR** full backup ZIP. Importer accepts all three (idempotent by URL; `click_count` is bounded on import to keep a hostile file from ballooning the click log). |
| **English-only / no localization.**                                             | UI fully localized in **English / Português / Español** via `react-i18next`. Locale picker in the topbar; browser-language autodetect on first load; choice persists in `localStorage`. |
| **Pinned/favorites = a tiny separate folder.** Visual only.                     | `pinned` is a real column on the table. `ORDER BY pinned DESC, …` applies in every sort mode. Gradient badge always visible. |
| **Data embedded in the browser.** Switched machines? Reinstalled Chrome? Pray. | Postgres + MinIO in containers. `make up` on a new machine and your backup ZIP restores everything (DB + images) in ~minutes. |
| **No way to know when a page you bookmarked changes.** A board, a release notes page, a status page — you find out by opening it. | Per-link opt-in (hourly/daily/weekly). Backend runs a fingerprint worker (RSS/Atom feed if present, content-hash fallback) and fires a **Web Push notification** when content changes. Bell in the Topbar manages the subscription; amber badge on the card flags unseen changes; "Recent updates" section in the sidebar lists the last N. Works with the tab closed (Service Worker). |
| **Pastebin/notes app is a separate tool.** Snippets and links live in different places. | **Notes** (`⌥M`) are a first-class entity alongside links: rich-text editor (Tiptap) with a **formatting toolbar** — bold/italic/underline/strike, headings, bullet & numbered lists, text alignment, text color, font family, quotes/code, links and inline images, same tags/folders/pin/search as links, interleaved in the same grid with an emerald badge, shareable via a public `/n/{slug}` page. |
| **No way to keep a folder private** on a shared screen/machine without a whole second account. | **Folder passwords.** Set a bcrypt-hashed password on any folder — its links/notes stay hidden (and its preview thumbnails redacted, even on hover) until you unlock it for the session. Backend-enforced, not just a UI prompt: the API itself refuses a locked folder's contents without proof of the password. Add an optional **reminder hint** (shown on the unlock prompt; can't be the password itself), and set a **master password** in **Settings** (with a strength meter, confirm field, and its own reminder hint) to reset a folder's password if you ever forget it. |

### Real scenarios that flipped the switch (native bookmarks → foldex)

- **"Which dashboards am I actually using?"** → the stats page surfaces top hosts and top links over 30 days. Drop the ones at 0 clicks.
- **"I want to share `localhost:9089/go/42` with the team."** → every URL gets a stable alias `/go/:id` that redirects + logs the click.
- **"Switch machines without losing anything."** → 1 button in the UI generates the full backup ZIP. Another button on the new machine restores with `mode=wipe`.
- **"The same link lives in 3 contexts (work + ai + architecture)."** → 3 tags. It shows up in all 3 filters.
- **"I want to know visually which link is which before clicking."** → every card shows an OG/screenshot/upload preview at 150px.
- **"Tell me when the on-call rotation page or the release notes change."** → flip the link to `daily` in the dialog, allow Web Push once, walk away. Notification fires the next time the fingerprint diverges. The card grows a "Monitored" chip and an amber badge until you mark it seen.

### When foldex is overkill

If you have fewer than 30 links saved and use **a single browser on a single machine**, native bookmarks are simpler. Foldex starts paying off once you need cross-browser access, telemetry, or real organization across more than one dimension.

---

## Quickstart

```bash
cp .env.example .env
make up                 # pulls justoeu/foldex-{backend,web}:latest from Docker Hub
                        # + boots Postgres on 127.0.0.1 (no Go/bun toolchain needed)
make migrate-up         # applies SQL migrations
make seed               # optional: sample tags + links

open http://localhost:9088
```

### Choosing between pre-built images and local build

| Want to … | Run | Notes |
|---|---|---|
| Just run Foldex | `make up` | Pulls `justoeu/foldex-{backend,web}:${FOLDEX_VERSION}` from Docker Hub. Default tag is `latest`. |
| Pin to a specific build | set `FOLDEX_VERSION=sha-3f6cc06` (or `1.4.1` — image tags drop the `v`) in `.env` then `make up` | Image tags published per commit + per semver tag. |
| Refresh to the latest tag | `make pull && make up` | `pull` re-fetches without restarting; `up` notices the new image and rolls. |
| Develop / build from source | `make up-build` | Uses the same `Dockerfile`s but builds locally, ignoring the registry image. Needs Docker; does NOT need Go/bun on the host (they run inside the build stages). |
| Apply local code changes | `make restart-backend` / `make restart-web` | Same as `up-build` but only the named service. |

### HTTPS (local dev) via mkcert

Nginx serves the web container over HTTPS on `:8443` inside the container,
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

make up                       # restarts the web container; certs are bind-mounted from web/certs
open https://localhost:9444   # 9444 = WEB_HTTPS_PORT; 9088 (WEB_PORT) is HTTP→HTTPS redirect
```

#### Reaching the SPA from a phone on the same wifi

The containers bind to `127.0.0.1` by default (per the single-user threat
model). To open foldex on an iPhone/iPad/Android on the same LAN, two
things have to change:

1. Set `WEB_BIND_HOST=0.0.0.0` in `.env` so nginx listens on every
   interface. `BIND_HOST` (backend) can stay on `127.0.0.1` — nginx
   already proxies `/api/` and `/go/` for you.
2. Include the host's LAN IP in the mkcert SAN list, otherwise the
   phone's browser rejects the cert before nginx even sees the request:

   ```bash
   LAN_IP=$(ipconfig getifaddr en0)   # macOS; substitute for Linux/WSL
   cd web/certs && mkcert -cert-file cert.pem -key-file key.pem \
     localhost 127.0.0.1 ::1 host.docker.internal "$LAN_IP"
   cd - && make up                     # bind-mount picks up the new cert
   ```

Then open `https://<LAN_IP>:9444` on the phone. The cert will show as
untrusted unless you also install the mkcert root CA on the device
(AirDrop `$(mkcert -CAROOT)/rootCA.pem` → Settings → Profile → Trust
on iOS; varies on Android). Tap-through warnings work fine for casual
use; PWA install (Add to Home Screen) requires a trusted cert.

The `cert.pem` and `key.pem` files are **gitignored** — generate them locally,
never commit. The web container bind-mounts `./web/certs:/etc/nginx/certs:ro`
at boot, so you only need to `make restart-web` (or `make up`) after
re-emitting the pair — no rebuild required. The published Docker Hub image
ships **no** TLS material; if the volume is empty (e.g. plain
`docker pull && docker run` without a mount), the container generates an
ephemeral self-signed pair so the browser can still reach the SPA.

Re-run `mkcert ...` when you add a new hostname (e.g. a `*.foldex.test` you
point at `127.0.0.1`) or after re-installing the local CA (`mkcert -install`)
on a new machine.

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
( cd backend && make fmt-check )   # gofmt gate — part of the pre-push gate
```

Coverage rules, exclusions, and the full **pre-push gate** (gofmt + vet + coverage, run locally before every commit) live in [`CLAUDE.md`](CLAUDE.md) §6.1. Every implementation also runs a mandatory **5-agent review sweep** (Code Review · Code Quality · Test Quality · Performance · Security) before merge — see §9. Read it before opening a PR.

Other targets: `make logs`, `make psql`, `make healthz`, `make down`. See `make help`.

## Security scanning (CI)

Layered, defense-in-depth tooling — all **informational** today (they surface findings without blocking merges; promote to hard gates by removing the `|| true` / `continue-on-error` once a clean baseline lands):

| Layer | Tool(s) | Workflow | Trigger |
|---|---|---|---|
| **SAST** | CodeQL (`security-extended`, Go + JS/TS) | `.github/workflows/codeql.yml` | push · PR · weekly |
| **SAST** | Semgrep (OWASP/secrets/lang packs) + gosec | `.github/workflows/sast.yml` | push · PR · weekly |
| **DAST** | OWASP ZAP baseline (passive) vs a live stack | `.github/workflows/dast.yml` | **monthly** · manual dispatch |
| **SCA** | govulncheck + `bun audit` | `.github/workflows/ci.yml` | PR |
| **Deps** | Dependabot (gomod · docker ×2 · actions) | `.github/dependabot.yml` | weekly PRs |

SAST findings land in the repo **Security ▸ Code scanning** tab (SARIF upload). The DAST job builds the stack from source via `docker compose --build`, waits for `/healthz`, runs the ZAP baseline against nginx over the shared `foldex` network, and uploads the HTML/MD/JSON report as a 30-day artifact. Run it on demand from the **Actions** tab → *dast* → *Run workflow*.

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

# 6. Create a note (server-side sanitized rich HTML) and render its public page.
curl -s -X POST localhost:9089/api/notes \
  -H 'Content-Type: application/json' \
  -d '{"title":"Scratchpad","body_html":"<p>Hello <strong>world</strong></p>"}' | jq .
curl -s localhost:9089/n/scratchpad | grep -o '<h1>.*</h1>'

# 7. Create a password-protected folder, confirm its contents are gated
#    without the unlock token, then confirm they unlock with it.
curl -s -X POST localhost:9089/api/folders \
  -H 'Content-Type: application/json' \
  -d '{"name":"Private","password":"hunter22"}' | jq .
curl -s localhost:9089/api/entries?folder_id=1 | jq .              # 403 folder_locked
TOKEN=$(curl -s -X POST localhost:9089/api/folders/1/unlock \
  -H 'Content-Type: application/json' -d '{"password":"hunter22"}' | jq -r .unlock_token)
curl -s -H "X-Foldex-Folder-Unlock: $TOKEN" localhost:9089/api/entries?folder_id=1 | jq .   # 200

# 7b. Set a master password (Settings), then recover the forgotten folder.
curl -s -X PUT localhost:9089/api/settings/master-password \
  -H 'Content-Type: application/json' -d '{"password":"master-recover-1"}' | jq .
curl -s -X POST localhost:9089/api/folders/1/reset-password \
  -H 'Content-Type: application/json' -d '{"master_password":"master-recover-1"}' -o /dev/null -w '%{http_code}\n'  # 204
curl -s localhost:9089/api/entries?folder_id=1 | jq .              # 200 — folder is now unprotected

# 8. Open the SPA and try ⌥K (palette) / ⌥N (new link) / ⌥M (new note); Settings gear in the topbar.
open http://localhost:9088
```

## Keyboard shortcuts (SPA)

| Shortcut         | Action                          |
|------------------|---------------------------------|
| `⌥K` / `Alt+K`   | Command palette (fuzzy search). `⌘K` conflicts with browsers' URL-bar focus. |
| `⌥N` / `Alt+N`   | New link (⌘N is hard-claimed by browser for "New window") |
| `⌥F` / `Alt+F`   | New folder (⌥P collided with other handlers; "F" for Folder) |
| `⌥M` / `Alt+M`   | New note (⌘M is hard-claimed by macOS for "Minimize window") |
| `⌘V` / `Ctrl+V`  | Paste a URL anywhere on the page → New Link dialog opens with it pre-filled. No-ops when typing in a field or when any dialog is already open. |
| `Esc`            | Close any open modal / exit folder view |
| `⌘Enter` (popup) | Save (in the browser extension) |

> **Convention**: every foldex shortcut is Alt-based. Browsers swallow most `⌘`-modifier combos (⌘K = focus URL bar, ⌘N = new window, ⌘P = print), so Alt-prefixed shortcuts are the only ones that reach the SPA reliably. The paste-to-create gesture is the one exception — it uses the native clipboard event, so it works with whatever paste shortcut the OS provides (including the phone's "Paste" menu).

## Internationalization

The whole UI runs through `react-i18next`. **English is the source of truth**; **Português** and **Español** are kept in full parity (every key mirrored across all three).

- **Switch language**: locale picker in the topbar. Choice persists in `localStorage["foldex.locale"]`; first load autodetects from `navigator.language`, falling back to English.
- **Locale files**: `web/src/i18n/locales/{en,pt,es}.json`.
- **Add a locale**: drop a new `<lang>.json`, list it in `SUPPORTED_LOCALES`, and populate every key from `en.json`. Plurals use the `_one` / `_other` suffix convention.

Every user-visible string must go through `t('key')` and ship in all three locales — enforced as an invariant in `CLAUDE.md`.

## Browser extension

A vanilla Manifest V3 extension lives in `extension/`. Load it as **unpacked** from `chrome://extensions` → Developer mode → Load unpacked → pick the `extension/` folder. Then click its icon on any tab and hit Save. See `extension/README.md`.

## Screenshots

The empty-state hero up top is the Home view on a fresh install. More captures
to come as the project gets more populated content:

- Populated home grid (cards + 3/5/8-column density)
- Command palette (`⌥K`)
- New link dialog with tag autocomplete + auto-detect of title/description (500 ms after you paste a URL; oEmbed enrichment for YouTube/Vimeo)
- Import page (drag-drop) + preview with the mode picker
- Stats page (KPIs, top hosts, tag distribution)
- Extension popup

## Layout

| Path           | What |
|----------------|------|
| `backend/`     | Go service (Chi + pgx + Postgres 18) — REST API, redirect, preview + change-check + push workers |
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

Via UI: open the **Import / Export** page → the right column hosts the **💾 Full backup** card. Drag a `.zip` onto it to review the validation summary and pick a mode in `BackupRestoreDialog`. History (last 10 backups: date, duration, size, counts) persists in `localStorage`.

> **Restore idempotency caveat.** `mode=skip` is idempotent for the UNIQUE-constrained entities (tags by name, links by URL — re-running the same zip inserts none). `click_log` and `folder` have no natural key, so a second skip restore of the same zip **re-inserts** those rows. Run a skip restore once; use `mode=wipe` for a clean re-baseline.

Full design rationale: [docs/SDD-BACKUP-RESTORE.md](docs/SDD-BACKUP-RESTORE.md).

## Docs

- [Vision](docs/VISION.md) — problem, goals, success criteria
- [Architecture](docs/ARCHITECTURE.md) — stack, data model, API, ADRs
- [Tasks](docs/TASKS.md) — phased implementation log
- [SDD: Backup & Restore](docs/SDD-BACKUP-RESTORE.md) — DB + MinIO snapshot ZIP, conflict modes, validation flow

## License

[MIT](LICENSE) © 2026 Valmir Justo.
