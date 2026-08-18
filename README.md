# Foldex

<p align="right"><sub><strong>🇺🇸 English</strong> · <a href="./README.pt-BR.md">🇧🇷 Português</a></sub></p>

<p align="center">
  <img src="docs/assets/home-empty.png" alt="foldex — self-hosted bookmark manager (home view with empty state, tag sidebar, topbar with search + sort + density controls, New folder / New link CTAs)" width="100%"/>
</p>

> Self-hosted bookmark manager with rich tagging, nestable folders, click tracking, visual URL previews, **pastebin-style rich-text notes**, **per-link change detection + Web Push**, full backup, and a browser extension.

Foldex is a personal "smart bookmarks bar" — it stores links organized by **nestable folders + M:N tags**, shows **what you actually click** (telemetry via `/go/{slug}`), captures every URL visually (OG image / favicon / screenshot fallback), lets you jot down **rich-text notes** (Tiptap editor with inline images) that live in the same grid/search/tags/folders as links, **watches the pages you care about** (RSS/Atom feed fingerprint with content-hash fallback) and pings you via Web Push when they change, and runs **entirely on your own machine** (Postgres + RustFS + Go + React in containers).

> Stack: **Go 1.26 (Chi · pgx) · PostgreSQL 18 · RustFS · Vite 8 + React 19 + TypeScript + bun · TanStack Query · Tiptap 3 · react-i18next (en/pt/es) · Vitest 4**. Versioning policy + invariants in [`CLAUDE.md`](CLAUDE.md).

---

## Why foldex instead of the browser's built-in bookmarks?

Native bookmarks are fine for "save a page quickly and forget it". Once you pass 50 links, the friction starts to hurt. Foldex addresses each pain point:

| Native-bookmark pain                                                            | How foldex solves it                                                                                                            |
| ------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| **Locked to one browser.** Chrome ↔ Safari ↔ Firefox = 3 silos. Sync requires a vendor account. | Your own server. Reach it from any browser, on any machine on your network. Data lives in a Postgres **you** control. |
| **Tree-only.** A bookmark lives in ONE folder. Want "work + ai + notebookLLM"? You triplicate. | **M:N tags** (a link can carry N labels) **+ 1:N nestable folders** (iPhone-style containment). The two systems coexist. |
| **Zero telemetry.** You "favorite" 200 links and use 8. You don't know which.   | Every navigation goes through `/go/{slug}` which inserts into `click_log`. Stats page shows clicks per day, top hosts, top links (last 30d), tag distribution. |
| **Preview = 16×16 favicon.** A gray list with tiny icons.                       | Visual card with OG image. If the page has none, foldex **captures a screenshot** automatically (headless Chromium → RustFS). You can also upload any image manually. |
| **Weak search.** Title/URL match only.                                          | Full-text search via Postgres `pg_trgm` over title + URL + description. Composes with tag filter (AND-multi-tag) and folder scope. |
| **Backup = opaque Netscape file.** Images? Clicks? Hierarchy? All lost.         | Single backup ZIP with `manifest.json` + `database.json` (5 tables) + **every RustFS image**. Lossless round-trip, SHA-256 checksum verification, 3 conflict modes (wipe/skip/duplicate). |
| **Fixed shortcuts.** Cmd+D opens the browser's native dialog.                   | MV3 extension + Alt-K (palette), Alt-N (new link), Alt-F (new folder). iPhone-style drag-and-drop between cards/folders. |
| **Vendor lock-in.** Leaving Chrome = export HTML + lose metadata.               | Export to **Netscape HTML** (universal compat) **OR** JSON v2 (with folders + click_count) **OR** full backup ZIP. Importer accepts all three (idempotent by URL; `click_count` is bounded on import to keep a hostile file from ballooning the click log). |
| **English-only / no localization.**                                             | UI fully localized in **English / Português / Español** via `react-i18next`. Locale picker in the topbar; browser-language autodetect on first load; choice persists in `localStorage`. |
| **Pinned/favorites = a tiny separate folder.** Visual only.                     | `pinned` is a real column on the table. `ORDER BY pinned DESC, …` applies in every sort mode. Gradient badge always visible. |
| **Data embedded in the browser.** Switched machines? Reinstalled Chrome? Pray. | Postgres + RustFS in containers. `make up` on a new machine and your backup ZIP restores everything (DB + images) in ~minutes. |
| **No way to know when a page you bookmarked changes.** A board, a release notes page, a status page — you find out by opening it. | Per-link opt-in (hourly/daily/weekly). Backend runs a fingerprint worker (RSS/Atom feed if present, content-hash fallback) and fires a **Web Push notification** when content changes. Bell in the Topbar manages the subscription; amber badge on the card flags unseen changes; "Recent updates" section in the sidebar lists the last N. Works with the tab closed (Service Worker). |
| **Pastebin/notes app is a separate tool.** Snippets and links live in different places. | **Notes** (`⌥M`) are a first-class entity alongside links: rich-text editor (Tiptap) with a **formatting toolbar** — bold/italic/underline/strike, headings, bullet & numbered lists, text alignment, text color, font family, quotes/code, links and inline images, same tags/folders/pin/search as links, interleaved in the same grid with an emerald badge, shareable via a public `/n/{slug}` page. |
| **No way to keep a folder private** on a shared screen/machine without a whole second account. | **Folder passwords.** Set a bcrypt-hashed password on any folder — its links/notes stay hidden (and its preview thumbnails redacted, even on hover) until you unlock it for the session. Backend-enforced, not just a UI prompt: the API itself refuses a locked folder's contents without proof of the password. Deleting a protected folder prompts for that password; deleting a whole tree is refused if it contains independently protected subfolders, so unlocking only the root never erases them. Add an optional **reminder hint** (shown on the unlock prompt; can't be the password itself), and set a **master password** in **Settings** (with a strength meter, confirm field, and its own reminder hint) to reset a folder's password if you ever forget it. |

### Real scenarios that flipped the switch (native bookmarks → foldex)

- **"Which dashboards am I actually using?"** → the stats page surfaces top hosts and top links over 30 days. Drop the ones at 0 clicks.
- **"I want to share a short link with the team."** → every URL gets a stable alias `/go/{slug}` that redirects + logs the click.
- **"Switch machines without losing anything."** → 1 button in the UI generates the full backup ZIP. Another button on the new machine restores with `mode=wipe`.
- **"The same link lives in 3 contexts (work + ai + architecture)."** → 3 tags. It shows up in all 3 filters.
- **"I want to know visually which link is which before clicking."** → every card shows an OG/screenshot/upload preview at 150px.
- **"Tell me when the on-call rotation page or the release notes change."** → flip the link to `daily` in the dialog, allow Web Push once, walk away. Notification fires the next time the fingerprint diverges. The card grows a "Monitored" chip and an amber badge until you mark it seen.

### When foldex is overkill

If you have fewer than 30 links saved and use **a single browser on a single machine**, native bookmarks are simpler. Foldex starts paying off once you need cross-browser access, telemetry, or real organization across more than one dimension.

---

## Quickstart

```bash
make up                 # pulls justoeu/foldex-{backend,web}:latest from Docker Hub
                        # + creates .env with persistent random RustFS secrets
                        # + boots Postgres on 127.0.0.1 (no Go/bun toolchain needed)
make migrate-up         # applies SQL migrations
make seed               # optional: sample tags + links

open https://localhost:9444
```

`make env` is idempotent: it generates independent 256-bit RustFS root/app
secrets only when missing (or when upgrading from the old public placeholders),
stores them in the gitignored `.env` with mode `0600`, and never prints them.
Direct `docker compose` use must provide those values; the bootstrap and backend
reject the old placeholders unless
`RUSTFS_ALLOW_INSECURE_DEV_CREDENTIALS=1` is explicitly set for an isolated,
disposable development instance.

For host-toolchain frontend development, `cd web && bun run dev` listens only
on `127.0.0.1:9088`. LAN access is a deliberate opt-in:
`VITE_DEV_LAN=1 bun run dev`.

### Choosing between pre-built images and local build

| Want to … | Run | Notes |
|---|---|---|
| Just run Foldex | `make up` | Pulls `justoeu/foldex-{backend,web}:${FOLDEX_VERSION}` from Docker Hub. Default tag is `latest`. |
| Pin to a specific build | set `FOLDEX_VERSION=sha-3f6cc06` (or `1.4.1` — image tags drop the `v`) in `.env` then `make up` | Image tags are available for manually published commit or semver targets. |
| Refresh to the latest tag | `make pull && make up` | `pull` re-fetches without restarting; `up` notices the new image and rolls. |
| Develop / build from source | `make up-build` | Uses the same `Dockerfile`s but builds locally, ignoring the registry image. Needs Docker; does NOT need Go/bun on the host (they run inside the build stages). |
| Apply local code changes | `make restart-backend` / `make restart-web` | Same as `up-build` but only the named service. |

Maintainer releases are manual: dispatch `release.yml` while selecting `main`
and provide either strict `vMAJOR.MINOR.PATCH` or a full 40-character SHA. A tag
push never publishes. The gate accepts only commits already in `origin/main`,
requires semver to match both version files, rejects pre-existing release tags,
creates the tag only after both image manifests publish, and makes every publisher wait on
the GitHub environment named `release`. Configure that environment with required
reviewers and keep `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN` as environment
secrets. Delete repository-level copies so a workflow stored in a historical
tag cannot read them.

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

Accounts are on, so authenticated `/api/*` routes need a credential. The only
session-less API read is `/api/files/notes/{uuid}.{ext}`, used by public `/n/{slug}`
pages. Only canonical note UUID keys
are accepted; link media remains session-gated and owner-scoped. Open <https://localhost:9444>, complete
the setup screen, then create an **API token** under Settings → API tokens and export it:

```bash
AUTH="Authorization: Bearer fx_1_your-token-here"
JSON="Content-Type: application/json"
```

```bash
# 1. Backend up? (/healthz is public — it is the one endpoint that needs nothing.)
curl -s localhost:9089/healthz | jq .

# 2. Create a tag.
curl -s -X POST localhost:9089/api/tags -H "$AUTH" -H "$JSON" \
  -d '{"name":"jira","color":"#1f6feb","icon":"🪲"}' | jq .

# 3. Create a link tied to that tag (preview is enqueued async).
curl -s -X POST localhost:9089/api/links -H "$AUTH" -H "$JSON" \
  -d '{"url":"https://news.ycombinator.com","title":"HN","tag_ids":[1]}' | jq .

# 4. Wait ~2s for the worker; then fetch — `preview_status` should be "ok".
sleep 3 && curl -s localhost:9089/api/links/1 -H "$AUTH" -H "$JSON" | jq '.preview_status, .og_image_url'

# 5. Resolve the short link (302 + counter bump). By SLUG: numeric /go/1 is off
#    by default now, because that route resolves with no session and link ids
#    are shared across accounts. See PUBLIC_NUMERIC_IDS.
curl -sI localhost:9089/go/hn | head -3

# 6. Create a note (server-side sanitized rich HTML) and render its public page.
curl -s -X POST localhost:9089/api/notes -H "$AUTH" -H "$JSON" \
  -d '{"title":"Scratchpad","body_html":"<p>Hello <strong>world</strong></p>"}' | jq .
curl -s localhost:9089/n/scratchpad | grep -o '<h1>.*</h1>'

# 7. Create a password-protected folder, confirm its contents are gated
#    without the unlock token, then confirm they unlock with it.
curl -s -X POST localhost:9089/api/folders -H "$AUTH" -H "$JSON" \
  -d '{"name":"Private","password":"hunter22"}' | jq .
curl -s -H "$AUTH" -H "$JSON" localhost:9089/api/entries?folder_id=1 | jq .    # 403 folder_locked
UNLOCK=$(curl -s -X POST localhost:9089/api/folders/1/unlock -H "$AUTH" -H "$JSON" \
  -d '{"password":"hunter22"}' | jq -r .unlock_token)
curl -s -H "$AUTH" -H "$JSON" -H "X-Foldex-Folder-Unlock: $UNLOCK" \
  localhost:9089/api/entries?folder_id=1 | jq .                    # 200

# 7b. Set a master password (Settings), then recover the forgotten folder.
curl -s -X PUT localhost:9089/api/settings/master-password -H "$AUTH" -H "$JSON" \
  -d '{"password":"master-recover-1"}' | jq .
curl -s -X POST localhost:9089/api/folders/1/reset-password -H "$AUTH" -H "$JSON" \
  -d '{"master_password":"master-recover-1"}' -o /dev/null -w '%{http_code}\n'  # 204
curl -s -H "$AUTH" -H "$JSON" localhost:9089/api/entries?folder_id=1 | jq .    # 200 — folder is now unprotected

# 8. The token's scope is real — these must all be refused.
curl -s -H "$AUTH" -H "$JSON" localhost:9089/api/auth/sessions -o /dev/null -w '%{http_code}\n'  # 403
curl -s -H "$AUTH" -H "$JSON" localhost:9089/api/admin/users  -o /dev/null -w '%{http_code}\n'  # 403 (admin) / 404 (user)
curl -s -H "$AUTH" -H "$JSON" -X POST localhost:9089/api/backup -o /dev/null -w '%{http_code}\n'  # 403

# 9. Open the SPA and try ⌥K (palette) / ⌥N (new link) / ⌥M (new note); Settings gear + avatar
#    menu (profile & sign out) in the topbar.
open https://localhost:9444
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

A vanilla Manifest V3 extension lives in `extension/`. Load it as **unpacked** from `chrome://extensions` → Developer mode → Load unpacked → pick the `extension/` folder.

Open its options and paste an **API token** (Foldex → **Settings → API tokens**). The extension has no cookie jar shared with the app, so a session would not reach it; the token is what identifies your account. Then click the icon on any tab and hit Save. See `extension/README.md`.

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

Full snapshot of the DB **and** the RustFS bucket into a single ZIP. Core endpoints:

```bash
# Generate — streams a ZIP. Headers expose counts + duration.
curl -OJ -X POST http://localhost:9089/api/backup
unzip -l foldex-backup-*.zip
#   manifest.json
#   database.json
#   files/screenshots/{id}[.{uuid}].{ext}
#   files/images/{id}[.{uuid}].{ext}
#   files/notes/{uuid}.{ext}

# Validate (without applying)
curl -X POST -F file=@foldex-backup-*.zip \
  http://localhost:9089/api/backup/validate | jq

# Restore — 3 conflict modes
curl -X POST -F file=@foldex-backup-*.zip \
  'http://localhost:9089/api/backup/restore?mode=skip' | jq
#   mode=wipe       — delete YOUR rows and YOUR files, then restore (DESTRUCTIVE)
#   mode=skip       — preserve existing (ON CONFLICT DO NOTHING; default)
#   mode=duplicate  — rename conflicting tags to "nome (2)"; folders always new;
#                     links with URL collision fall back to skip + warning
```

Via UI: open the **Import / Export** page → the right column hosts the **💾 Full backup** card. Chrome streams directly into the selected file; Firefox and Safari use a short-lived, one-time, account/session-bound native download, so no browser builds the full ZIP in JavaScript memory. The server reports completion metadata separately, preserving history (last 10 backups: date, duration, size, counts) in `localStorage` without parsing the archive. Drag a `.zip` onto the card to review the validation summary and pick a mode in `BackupRestoreDialog`. Closing or replacing the file cancels validation; once an import or restore starts, the dialog stays open and cannot be dismissed until the server responds.

Uploads are capped at 2 GiB compressed. Before validation or restore touches the database, Foldex rejects duplicate ZIP names, more than 100,000 entries, manifest/database JSON above 32/256 MiB, files above 64 MiB each, or more than 4 GiB expanded in total. Export applies the same envelopes, with at most 99,998 files (two entries are reserved for manifest/database) and 1,024-byte object keys. Only one export, validation, or restore runs at a time; a concurrent request receives `429 backup_busy` before DB, object-store, body-read, or temp-file work and can retry.

> **`mode=skip` is convergent for the entire archive.** Foldex durably checkpoints the exact archive digest and its fresh-ID/media mappings per account. Re-running a completed ZIP does not reinsert folders, notes, associations, or clicks and performs no repeated object upload checks. If object upload fails after the database commit, retry the same ZIP: restore resumes the committed mapping and writes only missing files. `wipe` clears prior checkpoints because it replaces their target rows; `duplicate` intentionally creates another copy.

> **Restore no longer preserves IDs, in any mode.** Row ids come from sequences that are shared across accounts, so re-using the ids inside a backup could collide with rows that already exist. Every restore mints fresh ids and re-points image keys and click history at them; what round-trips is the content and its relationships, not the integers. A backup also carries **no login data** — no passwords, no sessions, no 2FA secrets — and restoring one always creates content owned by whoever is restoring it, never by whoever exported it. Restoring someone else's backup is allowed and simply shows a warning that the content is changing hands.

Full design rationale: [docs/SDD-BACKUP-RESTORE.md](docs/SDD-BACKUP-RESTORE.md).

## Accounts & sign-in

Accounts are **on by default** since 1.13.0.

**First run — including an upgrade.** The SPA shows a setup screen. The account you
create there becomes the administrator and **adopts every link, note, folder and tag
that already existed**: nothing is lost and nothing has to be re-imported. Set
`AUTH_PUBLIC_URL` to the origin you actually browse to, because it is what invitation,
reset and verification links are built from.

```bash
AUTH_PUBLIC_URL=https://localhost:9444
```

Prefer the old behaviour? `AUTH_ENABLED=0` keeps it: every request is attributed to the
bootstrap administrator and nothing ever asks for a password. That is a genuine option
for a single-user machine on a private network — but on that setting anyone who can
reach the port owns the whole library, so keep the loopback bind.

**Adding people.** There is no public sign-up: an administrator sends an invitation from
**Settings → Administration → Users**, and only the address on that invitation can accept it — with a
password or with the matching Google account. The invite link is shown once when you
create it, and is also e-mailed. Invite, reset and verification credentials live after
`#` in those links, so the initial HTTP request and nginx access log never receive them;
the SPA removes the fragment immediately.

**Roles.** There are four, and what each one may do is a permission matrix the server
enforces — you can read it in **Settings → Administration → Roles and permissions**.

| role | what it is for |
|---|---|
| **Owner** | Runs the instance. Exactly one account holds it, and it moves only by transfer. Only the owner edits the password and sign-in policy. |
| **Admin** | Manages people, invitations and the audit trail — but does not set the rules they manage people under. |
| **Editor** | An ordinary account: full read/write over its own library. This is what every pre-4-role `user` became. |
| **Viewer** | Same library, read-only. Can still export a backup; cannot create, edit, import or restore. |

**Content stays private per account, in every role.** A role decides whether a write is
accepted and whether the administration screens exist — never whose links you can see. An
administrator manages accounts and still cannot read another account's rows.

**One settings screen.** The gear in the topbar opens the whole thing: a page head with
its own actions (export a backup, invite someone), a card showing who you are signed in as
— with a nudge to switch two-step verification on while you have none — and a grid of
cards, one per panel. Administrators get a **Personal × Administration** switch above it;
everyone else sees only the personal scope, because `/api/admin` answers 404 for them and a
disabled tab would promise a surface the server denies.

**Managing people.** **Settings → Administration** lists every account with its role, last
sign-in and status, and lets you change roles, disable, delete, sign out everywhere, send
account recovery, and (as the owner) transfer the instance. Guards enforced by the server,
not just hidden in the UI: you cannot demote, disable or delete **yourself**; the **last
active administrator** cannot be removed by anyone; and the **owner's** role and status
cannot be changed at all except by transferring. Transferring signs out both accounts.

**Audit trail.** **Settings → Administration → Audit log** records sign-ins and their
failures, role and status changes, invitations, forced recoveries and policy edits. It
survives the accounts it describes: deleting a user does not erase what that user did.

**Instance policy (owner only).** **Settings → Administration → Password and sign-in
policy** sets the minimum password length, the mailed-code lifetime and resend cooldown,
and which e-mail domains may sign in with Google. Every value has a floor the
configuration cannot cross, so an instance can be made stricter but never weaker than the
built-in minimum. The same screen holds **automatic account creation** for Google — off by
default, and refused unless you have listed at least one allowed domain, because turning
it on means this instance stops being invite-only. New accounts created that way always
arrive as Editor or Viewer, never as an administrator.

**E-mail.** `MAIL_DRIVER` defaults to `log`, which prints the invitation — link included
— to the backend log instead of sending it. That is deliberate: a self-hosted instance
with no SMTP server must still be able to invite someone. Read it with
`docker compose logs backend`, or copy the link the admin screen shows. For real
delivery set `MAIL_DRIVER=smtp` and the `MAIL_*` values; `make up-mail` starts
[Mailpit](https://mailpit.axllent.org/) with a local inbox at <http://localhost:8025>
for development.

Mail is **durable**: every message is written to a `mail_outbox` row in the same
database transaction as the credential it carries, so a restart, a deploy or a
provider outage cannot lose an invitation, a reset link or a sign-in code. A send
that fails is retried with a growing backoff (1 min → 5 → 15 → 30 → 60) and gives
up after six attempts, leaving the row as `failed` for you to inspect. The stored
payload is encrypted with a subkey derived from `AUTH_ENCRYPTION_KEY` — a queued row holds a live reset
link, and a database dump must not be an account-takeover kit. Messages are
rendered when they are sent, in English, Portuguese or Spanish, following the
**language chosen in the recipient's profile**, then the `Accept-Language` of
whoever triggered the send, then English. An invitation is the one message that
cannot follow a preference, because the invitee has no account yet.

**Language.** Each account picks its own in *Settings → Profile*, next to the
display name. Leaving it on *Follow my browser* is a real choice, not an absent
one: it keeps the current behaviour, where the language is guessed per request.
The topbar picker and the profile field are the same setting — changing either
while signed in updates the account, so the language on screen is the language
in your inbox.

**Sending through RabbitMQ (optional).** `MAIL_TRANSPORT` defaults to `inproc`,
where the backend renders and sends the queued messages itself. That needs no
broker and loses nothing: durability, retry and backoff all come from the
`mail_outbox` table. Set `MAIL_TRANSPORT=amqp` with an `AMQP_URL` to hand the
still-encrypted message to a broker instead and run the sending in its own
container:

```bash
docker compose --profile amqp up -d          # starts the `mailer` worker
```

| Var | Default | Meaning |
|---|---|---|
| `MAIL_TRANSPORT` | `inproc` | `inproc` \| `amqp`. An unknown value refuses to boot |
| `AMQP_URL` | — | Required for `amqp`. `amqp://` to a **remote** host is refused; use `amqps://` |
| `AMQP_EXCHANGE` | `foldex.mail` | Rename only to share one broker between instances |
| `AMQP_QUEUE` | `foldex.mail.send` | |
| `AMQP_PREFETCH` | `4` | Clamped to 1..64 |
| `MAIL_OUTBOX_BATCH` | `32` | Rows the relay claims per pass |
| `MAIL_OUTBOX_POLL_SEC` | `5` | How often it looks |

The worker gets `AUTH_ENCRYPTION_KEY` (it is the only process that opens the
payload) and **no database credential at all** — that separation is the point of
running it apart. Failed sends walk a retry ladder of dedicated queues (1 min →
5 min → 30 min) and then land in `foldex.mail.dead`, which the backend watches
so the outbox row still ends up marked `failed`.

On a shared broker, give foldex its own vhost and user rather than the default:

```bash
rabbitmqctl add_vhost /foldex
rabbitmqctl add_user foldex '<password>'
rabbitmqctl set_permissions -p /foldex foldex '^foldex\.' '^foldex\.' '^foldex\.'
# AMQP_URL=amqps://foldex:<password>@broker.example:5671/%2Ffoldex
```

**What each account can see.** Everything is private per account — administrators
included. An admin can create, disable and delete users, but never sees another
account's links or notes. Content is separated in the database itself, not by a
filter the UI applies.

**Sessions.** Sign-in sets httpOnly cookies: a short-lived access token plus a
30-day refresh token that rotates on every use. If a refresh token is ever replayed —
the signature of a stolen one — every session for that account is signed out and the
owner is e-mailed. Signing out is available everywhere; "sign out everywhere" revokes
every device. Credential changes such as changing/setting a password, disconnecting
Google, or turning off two-step verification keep the device you are on and drop the rest.

**Two-step verification.** Turn it on from **Settings → Two-step verification**, with
**two methods you can use separately or together**:

- **Authenticator app** — scan the QR with Google Authenticator, Authy, 1Password or
  Bitwarden and confirm one code. Works with no network connection.
- **E-mail codes** — confirm a code sent to your address. Needs no extra app, and is
  offered only when the instance has SMTP configured (`MAIL_DRIVER=smtp`); a code
  printed to the container log would not be a factor at all.

Either way you save the ten single-use **recovery codes** shown once in
`XXXX-XXXX-XXXX-XXXX` format. The server keeps only user-bound, server-keyed digests,
so it genuinely cannot show them again or test them from a database dump. Keep them:
an account arriving through a password-reset link is deliberately refused the e-mail
method — one mailbox must never satisfy both steps — so on an e-mail-only account they
are the way back in.

At sign-in the same six-digit field accepts a code from your app, a recovery code, or a
mailed code. In Settings, the field additionally offers **"E-mail me a code"** once
e-mail is set up, so changing a security setting on an e-mail-only account does not
cost you a recovery code. Removing one of two methods keeps your recovery codes;
removing the last one deletes them, since they would then guard nothing.

Administrators are required to have a second factor: with
`AUTH_REQUIRE_2FA_FOR_ADMINS=1` (the default) an admin without one is walked through
setting it up before their first session — choosing a method — rather than being locked
out. This also covers the first bootstrap account and administrator invitations.
Promoting a signed-in user to administrator signs out their existing sessions so the
next login can enforce verification. Turning the policy on for an existing instance
immediately blocks administrator features on old or refreshed sessions until a factor is
confirmed, while the enrollment routes remain available. An owner who wants the stricter
rule can set **instance policy → `admin_second_factor: totp_only`**, which stops an
e-mail factor from counting for administrators; the default `any` accepts either. An
administrator can always drop one method while the other stands, but never their last.

> Upgrading through migration `000023` invalidates older recovery sheets and pending
> e-mail codes because unkeyed digests cannot be converted without plaintext. Existing
> authenticator enrollment remains valid; regenerate recovery codes from Settings.

> **The key that encrypts the authenticator secrets must be backed up with your
> database.** Either let the backend generate `/data/auth_encryption.key` on first
> boot, or set `AUTH_ENCRYPTION_KEY` in `.env` (`openssl rand -base64 32`) — the env
> value wins, and if you use it, set `AUTH_ENCRYPTION_AUTO_GENERATE=0` so a
> dropped line becomes a boot failure instead of a fresh key that silently orphans
> every enrolled authenticator.
>
> Unlike the folder-unlock key it **cannot be regenerated** and there is no
> re-encryption: without it every enrolled account loses its second factor and needs
> an administrator to clear the enrollment.

**Sign in with Google.** Optional, and off until you configure it. Create an OAuth
client of type *Web application* in the Google Cloud console, register
`<AUTH_PUBLIC_URL>/api/auth/oauth/google/callback` as the redirect URI **exactly**, and
set:

```bash
GOOGLE_CLIENT_ID=…apps.googleusercontent.com
GOOGLE_CLIENT_SECRET=…
```

Two things it deliberately does not do. It **never creates an account** — the instance
is invite-only, and auto-provisioning would quietly bypass that. And it **never skips
two-step verification**: signing in with Google lands on the same code screen a password
login does.

**Connecting Google from Settings requires fresh proof.** **Settings → How you sign in →
Connect Google** opens a confirmation dialog for your current password and, when two-step
verification is enabled, a current authenticator or recovery code. Only then does the API
return the Google redirect URL. The five-minute link state is tied to that exact session and
credential version, so signing out, revoking the session, or changing/resetting the password
before the callback cancels the link. The password and code are sent only in the POST body,
never in a URL.

**Already have a password account with the same address?** Signing in with Google does
not log you in and does not refuse you — it asks for your **current password** once, and
then links Google and **removes the password**. From that point the account is
Google-only. Requiring the password is the whole point: an e-mail address is not a
secret, and anyone can put one in a Google profile, so a matching address alone must
never hand over an account. The trade is that *"I forgot my password, let me just use
Google"* does not work — reset first, convert afterwards.

Ways back into a Google-only account that loses its Google:

1. An administrator uses **Send recovery** on the Users & invitations screen inside the
   settings hub (Administration scope). Foldex sends a
   single-use link only to your verified mailbox through SMTP; the administrator never
   sees a password or token, and your credential and sessions do not change until you
   use the link and choose your own password. If SMTP fails, nothing is changed.
2. While still signed in via Google, **Settings → How you sign in → Set a password**.
   Only after that can you disconnect Google — doing it the other way round would leave
   the account with no way in at all, which the database refuses outright. Setting the
   password or disconnecting Google signs out your other devices.
3. Asking for ordinary password reset on such an account e-mails you *"this account signs in
   with Google"* rather than a reset link. That is on purpose: a link there would let
   an unauthenticated mailbox request resurrect the password the conversion retired.
   Administrator-triggered recovery additionally proves admin authorization.

**API tokens (browser extension, scripts).** **Settings → API tokens** mints a
long-lived bearer credential, shown **once** — the server keeps only a hash. It reads
and writes your links and notes and nothing else: it is refused on password changes,
sessions, invitations, user administration and backups, so a token pasted into an
extension's configuration is not the account. Revoke one and it stops working
immediately.

**Forgot your password.** **Reset it** from the sign-in screen — a fragment link arrives by
e-mail (or in the backend log with the default `log` driver), is good for 30 minutes and
can be used once. The link is also invalidated by a password change, sign-out-everywhere,
or an administrator changing the account's access or revoking its sessions. Using it signs you out of every other device, and an account with
two-step verification still has to present a code: a mailbox alone is never enough.

**Locked out?** With no way back into the only administrator account, recovery is a
direct database edit — the same status the master folder password already has. The one
case with no way out through the UI at all is the **last administrator, Google-only,
whose Google access is gone**: no other admin exists to reset their password. Setting
`AUTH_ENABLED=0` and restarting reverts the instance to single-user behaviour with all
content intact, which is the fastest way back in.

> **`SHARED_SECRET` was removed.** It predates accounts: it gated `/api`,
> identified nobody, and could not scope a single row — real authentication
> (ADR-30) replaced it. The env var, the `X-Foldex-Secret` header and its
> plumbing in the SPA and the extension are gone; delete them from your
> setup. `/api/*` protection is exclusively the auth stack's job.

> **Old `/go/42` links stopped working?** Numeric ids in `/go/{id}` and `/n/{id}` are off
> by default now. Those routes resolve with no session — they are public share links —
> and link ids are a counter shared across every account, so leaving them on would let
> anyone walk 1, 2, 3… and enumerate every link and note on the instance, other people's
> included. Slugs are unaffected. Set `PUBLIC_NUMERIC_IDS=1` if you have old numeric
> links already shared and would rather keep them working.

> **Preview network policy.** Cloud metadata/credential ranges and RFC6598 are
> always blocked. Set `PREVIEW_STRICT_SSRF=1` when users must not reach services
> on the host's internal network; strict mode rejects the complete IANA
> special-purpose registries. Leaving it unset preserves ordinary RFC1918
> intranet previews such as Jira and Confluence. Chromium screenshots always
> use the strict policy regardless of this setting.

Design rationale, threat model and the full API surface:
[docs/SDD-AUTH-RBAC.md](docs/SDD-AUTH-RBAC.md).

## Docs

- [Vision](docs/VISION.md) — problem, goals, success criteria
- [Architecture](docs/ARCHITECTURE.md) — stack, data model, API, ADRs
- [Tasks](docs/TASKS.md) — phased implementation log
- [SDD: Backup & Restore](docs/SDD-BACKUP-RESTORE.md) — DB + RustFS snapshot ZIP, conflict modes, validation flow
- [SDD: Folder master password](docs/SDD-FOLDER-MASTER-PASSWORD.md) — per-folder password recovery and hint
- [SDD: Auth, RBAC & multi-user](docs/SDD-AUTH-RBAC.md) — sessions, 2FA, Google OAuth, per-user data segmentation
- [SDD: Async e-mail & e-mail 2FA](docs/SDD-EMAIL-ASYNC.md) — transactional outbox, localized HTML templates, pluggable RabbitMQ transport and e-mail as a standing second factor (**shipped**)

## License

[MIT](LICENSE) © 2026 Valmir Justo.
