# Foldex

<p align="center">
  <img src="docs/assets/hero.svg" alt="foldex — self-hosted bookmark manager" width="100%"/>
</p>

> Self-hosted bookmark manager with rich tagging, nestable folders, click tracking, visual URL previews, full backup, and a browser extension.

Foldex é um "smart bookmarks bar" pessoal — guarda links organizados por **pastas aninhadas + tags M:N**, mostra **o que você de fato clica** (telemetria via `/go/:id`), captura visualmente cada URL (OG image / favicon / screenshot fallback) e roda **inteiro na sua máquina** (Postgres + MinIO + Go + React em containers).

> Stack: **Go 1.26 · PostgreSQL 16 · MinIO · Vite 8 + React 19 + bun · Vitest 4**. Versionamento + invariants em [`CLAUDE.md`](CLAUDE.md).

---

## Por que foldex e não o bookmark do browser?

Bookmark nativo é ótimo pra "salvar uma página rápida e esquecer". Quando você passa de 50 links, começa a doer. Foldex resolve cada uma dessas dores:

| Problema do bookmark nativo                                                   | Como foldex resolve                                                                                                            |
| ----------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| **Preso a um browser.** Chrome ↔ Safari ↔ Firefox = 3 silos. Sync exige conta no fornecedor. | Servidor próprio. Acessa de qualquer browser, em qualquer máquina da sua rede. Os dados ficam num Postgres que **você** controla. |
| **Só árvore.** Um bookmark mora em UMA pasta. Quer "trabalho + ia + notebookLLM"? Triplica. | **Tags M:N** (um link pode ter N labels) **+ pastas 1:N aninhadas** (containment iPhone-style). Os dois sistemas coexistem. |
| **Zero telemetria.** Você "favorita" 200 links, usa 8. Não sabe quais.        | Toda navegação passa por `/go/:id` que insere em `click_log`. Página de estatísticas mostra: cliques por dia, top hosts, top links 30d, distribuição por tag. |
| **Preview = favicon 16×16.** Lista cinza com mini-ícone.                      | Card visual com OG image. Se a página não tem, foldex **captura screenshot** automaticamente (headless Chromium → MinIO). Você pode ainda fazer upload manual da imagem que quiser. |
| **Busca raquítica.** Match só no título/URL.                                  | Busca full-text via Postgres `pg_trgm` em título + URL + descrição. Combinada com filtro por tag (AND-multi-tag) e escopo de pasta. |
| **Backup = arquivo Netscape opaco.** Imagens? Cliques? Hierarquia? Perde tudo. | Backup ZIP único com `manifest.json` + `database.json` (5 tabelas) + **todas as imagens do MinIO**. Round-trip lossless, validação com checksums SHA-256, 3 modos de conflito (wipe/skip/duplicate). |
| **Atalhos engessados.** Cmd+D abre o diálogo nativo do browser.               | Extensão MV3 + atalhos Alt-K (palette), Alt-N (novo link), Alt-F (nova pasta). Drag-and-drop iPhone-style entre cards/pastas. |
| **Lock-in do fornecedor.** Sair do Chrome = exportar HTML + perder metadados. | Export pra **Netscape HTML** (compat universal) **OU** JSON v2 (com folders + click_count) **OU** ZIP full backup. Importer aceita os três. |
| **Pinned/favoritos = uma pastinha à parte.** Só visual.                       | `pinned` é coluna real na tabela. `ORDER BY pinned DESC, …` em todo sort. Badge gradiente sempre visível.                  |
| **Dados embarcados no browser.** Trocou de máquina? Reinstalou Chrome? Reza.  | Postgres + MinIO em containers. `make up` numa máquina nova e seu backup ZIP restaura tudo (DB + imagens) em ~minutos.       |

### Cenários reais que viraram a chave (de bookmark nativo → foldex)

- **"Quais dashboards eu de fato uso?"** → stats page mostra top hosts e top links 30d. Larga os que ficaram em 0 cliques.
- **"Quero compartilhar `localhost:9089/go/42` com a equipe."** → toda URL ganha um alias estável `/go/:id` que redireciona + loga clique.
- **"Trocar de máquina sem perder nada."** → 1 botão "Gerar backup completo" na UI gera ZIP. Outro botão "Restaurar" na máquina nova com `mode=wipe`.
- **"O mesmo link mora em 3 contextos (trabalho + ia + arquitetura)."** → 3 tags. Aparece nos 3 filtros.
- **"Quero saber visualmente qual link é qual antes de clicar."** → cada card mostra preview OG/screenshot/upload em 150px.

### Quando foldex é overkill

Se você tem <30 links salvos e usa **um único browser numa única máquina**, o bookmark nativo é mais simples. Foldex faz sentido a partir do ponto em que você quer cross-browser, telemetria, ou organização real por mais de uma dimensão.

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

> **"Not Secure" no browser depois de tudo?** Significa que a CA root do
> mkcert não está no trust store dessa máquina (ou está mas o cert foi
> assinado por outra CA — comum quando se move o projeto entre máquinas).
> Rode `mkcert -install` e reemita os pem files com o bloco acima; depois
> `make up` para rebuildar o nginx com os certs novos.

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
  <img src="docs/assets/home-empty.png" alt="Foldex home view — base vazia mostrando o sidebar de tags, topbar e empty-state com CTA pra adicionar o primeiro link" width="100%"/>
</p>

<p align="center"><sub><em>Home view (sem links ainda) — tag sidebar à esquerda, topbar com busca + filtros, CTAs Novo link / Nova pasta à direita, empty-state convidando o primeiro import.</em></sub></p>

> *Outras capturas pendentes:*
>
> - Home grid populado (cards + densidade 3/5/8 col)
> - Command palette (`⌥K`)
> - New link dialog com tag autocomplete
> - Import page (drag-drop) + preview com mode picker
> - Página de stats (KPIs, top hosts, distribuição por tag)
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

Via UI: go to **Importar / Exportar** → right column has the **💾 Backup completo** card. Drag a `.zip` onto it to review the validation summary and pick a mode in `BackupRestoreDialog`. History (last 10 backups: date, duration, size, counts) persists in `localStorage`.

Full design rationale: [docs/SDD-BACKUP-RESTORE.md](docs/SDD-BACKUP-RESTORE.md).

## Docs

- [Vision](docs/VISION.md) — problem, goals, success criteria
- [Architecture](docs/ARCHITECTURE.md) — stack, data model, API, ADRs
- [Tasks](docs/TASKS.md) — phased implementation log
- [SDD: Backup & Restore](docs/SDD-BACKUP-RESTORE.md) — DB + MinIO snapshot ZIP, conflict modes, validation flow

## License

Personal project. No license granted.
