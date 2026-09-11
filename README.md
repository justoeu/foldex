# Foldex

<p align="right"><sub><strong>🇺🇸 English</strong> · <a href="./README.pt-BR.md">🇧🇷 Português</a></sub></p>

<p align="center">
  <img src="docs/assets/login.png" alt="Foldex sign-in — Welcome back, e-mail and password, language flags" width="100%"/>
</p>

<p align="center">
  <img src="docs/assets/home-empty.png" alt="Foldex home — empty library, tag sidebar, search, New folder / New link" width="100%"/>
</p>

Self-hosted bookmark manager: **tags (M:N)**, **nestable folders**, click tracking via `/go/{slug}`, visual previews, rich-text **notes**, optional **change detection + Web Push**, and a full backup ZIP. Runs on your machine (Postgres + RustFS + Go + React). UI in **en / pt / es**.

> Stack: **Go 1.26 (Chi · pgx) · PostgreSQL 18 · RustFS · Vite 8 + React 19 + TypeScript 7 + bun**. Invariants: [`CLAUDE.md`](CLAUDE.md).

---

## Quickstart

```bash
make up              # pulls justoeu/foldex-{backend,web}:latest
make migrate-up      # SQL migrations
make seed            # optional sample data
open https://localhost:9444
```

`make up` and `make db-up` run `make env` automatically, generating Postgres and RustFS credentials in gitignored `.env` (mode `0600`) without printing them. Direct Compose calls must supply `POSTGRES_PASSWORD`: there is no known-password fallback. Existing volumes retain their password; rotate it in Postgres and update client settings together. Preview fetches block private/loopback addresses by default. Set `PREVIEW_STRICT_SSRF=0` only when trusted users need intranet previews; screenshots and Web Push remain public-only. First visit is the **setup** screen (create the admin); after that, **sign in**.

**Upgrading an existing volume:** before updating, make sure `.env` explicitly contains the password that Postgres currently accepts. Older installs may have no `POSTGRES_PASSWORD` line and rely on the removed `foldex` fallback. For such a volume, temporarily setting `POSTGRES_PASSWORD=foldex` preserves access; rotate the database role and all clients to a new secret as soon as possible. Do not leave that line empty, generate a replacement, or delete the volume expecting the existing database password to change. If you use intranet previews, explicitly set `PREVIEW_STRICT_SSRF=0` before upgrading. These configuration changes require a **major release** when published.

HTTPS on `:9444` uses mkcert locally — see [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) (ports & TLS). Pin a release with `FOLDEX_VERSION` in `.env`. Source build: `make up-build`.

## What you get

- Links + notes in one grid (tags, folders, pin, search)
- OG / favicon / screenshot previews
- Per-folder passwords and a master recovery password
- Import/export: Netscape HTML, JSON, full ZIP (DB + images)
- MV3 extension + palette (`⌥K`)
- Multi-user (sessions, 2FA, Google, RBAC) — on by default
- Scheduled off-site backups (dump, restore drill, object mirror, per-user ZIPs) — artifacts downloadable by the instance owner, opt-in

Native browser bookmarks are simpler if you have a handful of links in one browser. Foldex pays off with cross-browser access, telemetry, and two-axis organization.

## Smoke test

Accounts are on. Create an API token under **Settings → API tokens**:

```bash
AUTH="Authorization: Bearer fx_1_your-token-here"
curl -s localhost:9089/healthz
curl -s -X POST localhost:9089/api/tags -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"name":"jira","color":"#1f6feb"}'
open https://localhost:9444
```

More flows (notes, folder unlock, backup): [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

## Shortcuts

| Shortcut | Action |
|---|---|
| `⌥K` | Command palette |
| `⌥N` / `⌥F` / `⌥M` | New link / folder / note |
| `⌘V` / `Ctrl+V` | Paste a URL → new link dialog |
| `Esc` | Close modal / leave folder |

## Layout

| Path | What |
|---|---|
| `backend/` | Go API, workers |
| `web/` | React SPA |
| `extension/` | Manifest V3 |
| `docs/` | Vision, architecture, SDDs |

## Docs

- [Vision](docs/VISION.md) · [Architecture](docs/ARCHITECTURE.md) · [Auth / RBAC](docs/SDD-AUTH-RBAC.md)
- [Backup ZIP](docs/SDD-BACKUP-RESTORE.md) · [Ops backups](docs/SDD-OPS-BACKUP.md)
- [Folder passwords](docs/SDD-FOLDER-MASTER-PASSWORD.md) · [E-mail](docs/SDD-EMAIL-ASYNC.md)
- [Extension](extension/README.md)

## License

[MIT](LICENSE) © 2026 Valmir Justo.
