# Foldex — Architecture

## Visão de sistema

```
        ┌──────────────┐     ┌──────────────┐     ┌──────────────┐
        │   Web SPA    │     │  Extension   │     │ Service Wkr  │
        │ (Vite/React) │     │   (MV3)      │     │ (push, PWA)  │
        └──────┬───────┘     └──────┬───────┘     └──────┬───────┘
               │ HTTP /api                                │ push
               └──────────┬───────────────────────────────┘
                          ▼                                 ▲
              ┌───────────────────────┐                     │ webpush-go
              │     Backend (Go)      │       ┌─────────────┴───────┐
              │     Chi router        │       │  internal/push      │
              │ ┌───────────────────┐ │       │  VAPID + sender     │
              │ │ links · tags ·    │ │       └─────────────▲───────┘
              │ │ folders · stats · │ │                     │
              │ │ /go · backup ·    │ │                     │
              │ │ push · import     │ │  enqueue        ┌───┴────────┐
              │ └───────┬───────────┘ │◀───── change ───│ changecheck│
              │ ┌───────▼───────────┐ │                 │ worker     │
              │ │ preview worker    │─┼─── HTML ─▶ ext. │ (fingerprt)│
              │ │ + screenshot ───▶ │ │      URLs       └────────────┘
              │ │   MinIO           │ │
              │ └───────────────────┘ │
              └────────┬───────┬──────┘
                pgxpool │       │ S3 SDK
                        ▼       ▼
              ┌────────────────────┐   ┌───────────┐
              │   PostgreSQL 18    │   │  MinIO    │
              │  tag · link ·      │   │  bucket   │
              │  link_tag · folder │   │ screensh. │
              │  click_log ·       │   │  images   │
              │  push_subscription │   └───────────┘
              └────────────────────┘
```

Todos os componentes rodam num `docker-compose`. Backend e web bindam só em `127.0.0.1` por default. O changecheck worker e o push sender são goroutines in-process no mesmo binário do backend (nenhum broker externo).

## Stack & rationale

| Camada       | Escolha                                                              | Por quê |
|--------------|----------------------------------------------------------------------|---------|
| Runtime API  | **Go 1.26** + Chi v5.2 + pgx/v5.9 + `slog`                          | Minimal router, pgxpool com tipos, log estruturado nativo. |
| DB           | **PostgreSQL 18** + `pg_trgm`                                        | Busca por substring com índice GIN, suficiente single-user. |
| Object store | **MinIO** (S3 SDK)                                                   | Backup/screenshots/uploads vivem fora do Postgres; bucket único, prefixos `screenshots/`/`images/`. |
| Migrations   | `golang-migrate` (`000NNN_*.up/down.sql`)                            | Reversível por padrão; mesma convenção compartilhada. |
| Workers      | Goroutine pools in-process (preview, changecheck) + buffered channels | Zero dependência operacional (sem Redis/queue). |
| Web Push     | `github.com/SherClockHolmes/webpush-go v1.4.0` + VAPID auto-gen      | RFC 8030. VAPID key persistida em `/data/vapid.json` (volume `foldex-data`), 0o600. |
| Imagem       | `golang.org/x/image` + stdlib decoders (pure Go, sem CGO)            | Re-encode JPEG q82 + downscale Catmull-Rom + decode-bomb guard 50 MP (`internal/imageopt`). |
| Headless     | `github.com/go-rod/rod v0.116` (Chromium)                            | Screenshot fallback quando o site não tem `og:image`. SSRF guard antes do launch. |
| Testes Go    | `testify` (unit) + `testcontainers-go v0.42` (integration, build tag)| Suite real contra Postgres efêmero; gate ≥85% (ver `CLAUDE.md`). |
| SPA          | **Vite 8 + React 19.2 + TypeScript 6 + MUI 9**                        | MUI só pra `createTheme`/`ThemeProvider`; visual vive em `web/src/styles/foldex.css` (CSS handoff). Bundle ~80 kB. |
| Server state | **TanStack Query 5**                                                  | Cache + invalidação por mutation + optimistic updates. |
| i18n         | **react-i18next 17** + i18next 26 (en/pt/es)                          | Locale picker no topbar persiste em `localStorage["foldex.locale"]`. Plurais via `_one`/`_other`. |
| PWA          | **vite-plugin-pwa 1.3** com `strategies: 'injectManifest'`            | SW hand-rolled em `web/src/sw.ts` (Cache API + push/notificationclick listeners). Workbox só injeta `__WB_MANIFEST` no build. |
| Testes web   | **Vitest 4** + `@testing-library/react 16` + jsdom 29                 | Mesmo gate ≥85% (`vitest.config.ts`). |
| Extension    | Vanilla MV3 (sem bundler)                                            | Popup tem ~80 LoC. Sem build = "load unpacked" direto. |
| Node runtime | **bun 1.3** (oven/bun:1.3-alpine)                                    | Bate com Vite 8 / Vitest 4 e resolve melhor packages platform-specific que npm em mirror privado. |

## Data model (estado atual, após 11 migrations)

```sql
-- 000001_init.up.sql        (+ pg_trgm)
-- 000002_constraints        → link_preview_status_check + link_url_unique
-- 000003_click_log          → tabela de eventos de clique
-- 000004_click_log_backfill → data migration idempotente
-- 000005_link_pinned        → coluna `pinned` + índice
-- 000006_drop_link_counters → REMOVE link.click_count + last_clicked_at
-- 000007_folders            → tabela `folder` + `link.folder_id` (1:N)
-- 000008_folder_nesting     → `folder.parent_id` (self-FK ON DELETE SET NULL)
-- 000009_link_slug          → `link.slug NOT NULL UNIQUE` + CHECK + backfill
-- 000010_link_change_check  → 6 colunas em `link` p/ change-detection per-link + 2 índices parciais
-- 000011_push_subscription  → tabela `push_subscription` (RFC 8030 + VAPID)

CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE tag (
  id         BIGSERIAL PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  color      TEXT NOT NULL DEFAULT '#6366F1',  -- aceita hex (#6366F1) OU gradient CSS ("linear-gradient(135deg, #a, #b)")
  icon       TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 000007_folders.up.sql — iPhone-style folder organization (1:N).
CREATE TABLE folder (
  id         BIGSERIAL PRIMARY KEY,
  name       VARCHAR(200) NOT NULL,           -- sem UNIQUE (iPhone permite duplicatas)
  color      VARCHAR(200) NOT NULL DEFAULT '#6366F1',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE link ADD COLUMN folder_id BIGINT REFERENCES folder(id) ON DELETE SET NULL;
CREATE INDEX link_folder ON link (folder_id) WHERE folder_id IS NOT NULL;

-- 000008_folder_nesting → folders aninhadas (parent_id self-FK)
ALTER TABLE folder ADD COLUMN parent_id BIGINT REFERENCES folder(id) ON DELETE SET NULL;
CREATE INDEX folder_parent ON folder (parent_id) WHERE parent_id IS NOT NULL;

CREATE TABLE link (
  id             BIGSERIAL PRIMARY KEY,
  url            TEXT NOT NULL UNIQUE,
  slug           TEXT NOT NULL UNIQUE,                   -- 000009 (CHECK [a-z0-9]+(-[a-z0-9]+)* AND NOT all-numeric)
  title          TEXT NOT NULL,
  description    TEXT,
  favicon_url    TEXT,
  og_image_url   TEXT,
  preview_status TEXT NOT NULL DEFAULT 'pending'
                 CHECK (preview_status IN ('pending', 'ok', 'failed')),
  preview_error  TEXT,
  pinned         BOOLEAN NOT NULL DEFAULT FALSE,
  -- 000010: change-detection per-link (todos nullable, opt-in)
  check_interval          TEXT,                          -- CHECK NULL OR IN ('hourly','daily','weekly')
  last_checked_at         TIMESTAMPTZ,
  last_fingerprint        TEXT,                          -- 'feed:<sha256>' OU 'content:<sha256>' (prefixo = discriminador)
  last_change_detected_at TIMESTAMPTZ,
  change_seen_at          TIMESTAMPTZ,
  last_check_error        TEXT,                          -- isolado de preview_error (workers diferentes)
  created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX link_title_trgm        ON link USING gin (title gin_trgm_ops);
CREATE INDEX link_url_trgm          ON link USING gin (url   gin_trgm_ops);
CREATE INDEX link_created           ON link (created_at DESC);
CREATE INDEX link_pinned_created    ON link (pinned DESC, created_at DESC);
-- 000010: scanner do worker enxerga só os opt-in (O(opt-in), não O(total)).
CREATE INDEX link_check_due_idx     ON link (check_interval, last_checked_at)
                                      WHERE check_interval IS NOT NULL;
-- 000010: sidebar "Atualizações recentes" (últimos N dias).
CREATE INDEX link_change_recent_idx ON link (last_change_detected_at DESC)
                                      WHERE last_change_detected_at IS NOT NULL;

-- 000011: Web Push subscriptions. Single-user → sem user_id.
-- `endpoint UNIQUE` suporta upsert quando o navegador renova a subscription
-- (mesma URL com keys rotacionados). Sender remove o row em 404/410.
CREATE TABLE push_subscription (
  id           BIGSERIAL PRIMARY KEY,
  endpoint     TEXT NOT NULL,
  p256dh       TEXT NOT NULL,
  auth         TEXT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_used_at TIMESTAMPTZ,
  CONSTRAINT push_subscription_endpoint_unique UNIQUE (endpoint)
);

CREATE TABLE link_tag (
  link_id BIGINT NOT NULL REFERENCES link(id) ON DELETE CASCADE,
  tag_id  BIGINT NOT NULL REFERENCES tag(id)  ON DELETE CASCADE,
  PRIMARY KEY (link_id, tag_id)
);
CREATE INDEX link_tag_tag ON link_tag (tag_id);

-- Single source of truth for click events. `link.click_count` and
-- `link.last_clicked_at` are NOT stored — they are derived at read time.
CREATE TABLE click_log (
  id         BIGSERIAL PRIMARY KEY,
  link_id    BIGINT NOT NULL REFERENCES link(id) ON DELETE CASCADE,
  clicked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX click_log_clicked_at ON click_log (clicked_at DESC);
CREATE INDEX click_log_link_id_ts ON click_log (link_id, clicked_at DESC);
```

Click registration is **append-only** to `click_log`. The /go handler does:

```sql
-- inside a tx
SELECT url FROM link WHERE id = $1;            -- 404 check
INSERT INTO click_log (link_id) VALUES ($1);   -- the only writer
```

Every SELECT that needs `click_count` / `last_clicked_at` derives them via a LATERAL join, e.g.:

```sql
SELECT l.id, l.url, l.title, ...,
       COALESCE(cl.cnt, 0) AS click_count,
       cl.last_at          AS last_clicked_at,
       l.pinned, l.created_at
FROM link l
LEFT JOIN LATERAL (
  SELECT count(*) AS cnt, max(clicked_at) AS last_at
  FROM click_log WHERE link_id = l.id
) cl ON TRUE
ORDER BY l.pinned DESC, COALESCE(cl.cnt, 0) DESC;
```

Listagem com filtro (texto OR substring em title/url; tags como AND quando múltiplas):

```sql
SELECT l.*, COALESCE(array_agg(t.id) FILTER (WHERE t.id IS NOT NULL), '{}') AS tag_ids
FROM link l
LEFT JOIN link_tag lt ON lt.link_id = l.id
LEFT JOIN tag t       ON t.id = lt.tag_id
WHERE ($1::text IS NULL
       OR l.title ILIKE '%'||$1||'%'
       OR l.url   ILIKE '%'||$1||'%')
  AND ($2::bigint[] IS NULL
       OR l.id IN (
         SELECT link_id FROM link_tag
         WHERE tag_id = ANY($2)
         GROUP BY link_id
         HAVING count(DISTINCT tag_id) = array_length($2,1)
       ))
GROUP BY l.id
ORDER BY l.created_at DESC
LIMIT $3 OFFSET $4;
```

## API surface

| Grupo  | Método | Path                                  | Propósito                                          |
|--------|--------|---------------------------------------|----------------------------------------------------|
| Links  | GET    | `/api/links`                          | List; query: `q`, `tag` (repeatable), `limit`, `offset`, `sort=created\|clicks\|recent\|alpha\|alpha_desc`, **`folder_id=N`** (links na pasta), **`ungrouped=1`** (links sem pasta). Pinados sempre vêm primeiro. `alpha`/`alpha_desc` ordenam por `lower(title)`. |
|        | GET    | `/api/links/recent-changes`           | Últimos links com `last_change_detected_at IS NOT NULL`. Query: `?days` (1..365, default 7), `?limit` (1..100, default 10), via `clampInt`. Powering a seção "Atualizações recentes" da sidebar (refetch 60 s). |
|        | GET    | `/api/links/url-metadata?url=…`       | Pré-fetch síncrono usado pelo `LinkDialog` para auto-preencher Título/Descrição assim que o usuário cola/digita uma URL. Reusa `preview.NewFetcher` (mesmo SSRF gate, mesma posture); rejeita scheme não-http(s) → 400 `invalid_scheme`, URL > 2 KiB → 400 `invalid_url`. Falhas de fetch (DNS, SSRF, 4xx, timeout) colapsam em 502 `fetch_failed` sem vazar a mensagem interna. Debounce 500 ms + cache módulo-level com TTL 5 min no front; backend é fire-on-demand sem cache próprio. **YouTube/Vimeo bypassam HTML via oEmbed** (`preview.Fetcher.Fetch` curto-circuita pro endpoint oEmbed quando host bate `knownOEmbedProviders`, porque YouTube serve HTML degraded a fingerprints de container — UA/headers/cookies não ajudam). Discovery genérico via `<link rel="alternate" type="application/json+oembed">` enriquece outros sites quando o HTML carrega mas falta título/imagem. |
|        | POST   | `/api/links`                          | Body: `{url, title, description?, tag_ids?, pinned?, folder_id?, slug?, check_interval?}` → enqueue preview |
|        | GET    | `/api/links/{id}`                     | One link with tags (`click_count` derivado)        |
|        | PATCH  | `/api/links/{id}`                     | Update qualquer campo + `tag_ids` (replace set), `pinned`. `folder_id`/`slug`/`check_interval` são tri-state: ausente=não toca, valor=atribui, `null`=limpa. Em opt-out de `check_interval`, repository limpa também `last_checked_at`/`last_fingerprint`/`last_change_detected_at`/`change_seen_at` na mesma UPDATE. |
|        | DELETE | `/api/links/{id}`                     | Hard delete (cascade em `link_tag` e `click_log`)  |
|        | POST   | `/api/links/{id}/refresh-preview`     | Re-enqueue meta fetch                              |
|        | POST   | `/api/links/{id}/seen-change`         | Marca o badge "atualizado" como lido (bump `change_seen_at = now()`). 404 quando `last_change_detected_at IS NULL` — bloqueia bump out-of-band antes de qualquer detecção. |
|        | POST   | `/api/links/{id}/screenshot`          | Captura sob demanda via Chromium headless. SSRF gate obrigatório (`links.URLPolicy`, default `preview.IsPublicURL`); rejeita scheme não-http(s) com 400 `invalid_scheme` e privado/IMDS com 400 `private_target`. Policy nil = 500 `policy_unconfigured`. |
|        | POST   | `/api/links/{id}/image`               | Upload manual de imagem (multipart `file`). Cap de body 5 MiB; aceita `{png, jpeg, gif, webp}` (SVG cai fora). Pipeline `imageopt` re-encoda em JPEG q82, downscale ≤1024 px, decode-bomb guard 50 MP. Curto-circuita o worker. |
|        | DELETE | `/api/links/{id}/image`               | Remove `og_image_url` + DELETE no objeto MinIO + zera `preview_status`. |
| Files  | GET    | `/api/files/*`                        | Proxy pro MinIO. Key precisa cair em `screenshots/`/`images/` (rejeita `..` e prefixo arbitrário). `DetectContentType` no objeto servido + `X-Content-Type-Options: nosniff`. |
| Tags   | GET    | `/api/tags`                           | List with `link_count`                             |
|        | POST   | `/api/tags`                           | Body: `{name, color?, icon?}`                      |
|        | PATCH  | `/api/tags/{id}`                      |                                                    |
|        | DELETE | `/api/tags/{id}`                      | Cascades junction                                  |
| Folders| GET    | `/api/folders`                        | Query: `?root=1` (só pastas raiz, `parent_id IS NULL`), `?parent_id=N` (filhas diretas de N), ausente (flat, todas). Retorna `link_count` + `preview_links` (até 4, LATERAL+jsonb_agg, ordem pinned DESC, created DESC) + `parent_id`. |
|        | POST   | `/api/folders`                        | Body: `{name, color?, parent_id?}` — `parent_id` opcional (null = raiz). |
|        | GET    | `/api/folders/{id}`                   |                                                    |
|        | PATCH  | `/api/folders/{id}`                   | `parent_id` é tri-state (absent=não toca, N=move pra dentro de N, null=promove pra raiz). |
|        | DELETE | `/api/folders/{id}`                   | Default: SET NULL em links E em subpastas (filhas viram raiz). Com `?cascade=1`: recursivo via CTE — apaga toda a subtree de pastas + links. |
| Stats  | GET    | `/api/stats/summary`                  | Totals: links, tags, clicks 30d/prev30d, novos 30d, top host |
|        | GET    | `/api/stats/daily?days=N`             | Array `[{date, clicks}]` zero-filled via `generate_series` |
|        | GET    | `/api/stats/top?limit=N`              | Top links por cliques (lifetime + janelas 30d / prev) |
|        | GET    | `/api/stats/tags`                     | Distribuição: por tag, soma de cliques + nº de links |
| I/O    | POST   | `/api/import`                         | Multipart `file` + `format=netscape\|json` (JSON restaura cliques via click_log) |
|        | GET    | `/api/export?format=netscape\|json`   | Download (click_count derivado em subquery)        |
| Backup | POST   | `/api/backup`                         | Stream ZIP completo (DB + MinIO). `Content-Type: application/zip`. Disponível só quando MinIO está acessível. Ver [SDD-BACKUP-RESTORE.md](./SDD-BACKUP-RESTORE.md). |
|        | POST   | `/api/backup/validate`                | Multipart `file=<zip>` → `{ok, manifest, conflicts, warnings, errors}` sem aplicar |
|        | POST   | `/api/backup/restore?mode=…`          | Multipart `file=<zip>` + `mode=wipe\|skip\|duplicate` (default `skip`) → `{inserted, skipped, wiped, files, duration_ms}` |
| Stats  | GET    | `/api/stats/storage`                  | `{objects, total_bytes}` do bucket MinIO; registrado só quando o storage está disponível |
| Push   | GET    | `/api/push/vapid-key`                 | Retorna a chave pública VAPID (base64url) — front usa em `PushManager.subscribe({applicationServerKey})`. Atrás do `SHARED_SECRET` quando set. |
|        | POST   | `/api/push/subscriptions`             | Upsert por `endpoint` (UNIQUE) com p256dh/auth atualizados. Renovação do navegador é silenciosa. |
|        | DELETE | `/api/push/subscriptions`             | Remove a subscription pelo endpoint (chamado no unsubscribe do usuário). |
|        | POST   | `/api/push/test`                      | Dispara notificação de teste pra todas as subscriptions ativas. Útil pra validar VAPID/SW. |
| Redir  | GET    | `/go/{id-or-slug}`                    | 302 + INSERT no click_log (fora de `/api`). ID-first; fallback pra slug (mig 000009). |
| Health | GET    | `/healthz`                            | `{status, db}` + 200/503                           |

Erros em JSON uniforme: `{ "error": { "code": "not_found", "message": "..." } }`.

## Preview worker

- Implementação em `internal/preview/`:
  - `worker.go`: pool de N goroutines (`PREVIEW_WORKER_CONCURRENCY`, default 4), consome de `chan PreviewJob`.
  - `fetcher.go`: `http.Client{Timeout: 5s}`, parse HTML head com `golang.org/x/net/html` — extrai `<title>`, `meta[og:title|og:image|og:description]`, `link[rel~=icon]`.
  - `public.go`: `IsPublicURL(ctx, url)` — gate do fallback de screenshot (resolve o host e rejeita IMDS/loopback/RFC1918/link-local/ULA).
  - `enqueue.go`: `Enqueue(linkID int64)` chamado por `links.Create` e por `POST /links/:id/refresh-preview`.
- Side effects: `UPDATE link SET preview_status, favicon_url, og_image_url, description, preview_error, updated_at` após o fetch.
- SSRF guard: **IMDS (`169.254.169.254`) é sempre bloqueado** (sem opt-out). Loopback, RFC1918, link-local, IPv6 ULA só são bloqueados quando `PREVIEW_STRICT_SSRF=1` no `.env`. Default = permissivo, porque intranet (Jira/Grid/Confluence) é caso de uso primário do foldex.
- **Short-circuit por upload manual** (`Worker.process` no topo): se `link.og_image_url` já tem valor quando o job vira processado, o worker **pula tudo** (sem fetch HTML, sem screenshot) e só flipa `preview_status` de `pending`→`ok` se ainda estiver `pending`. É a peça que garante: upload feito enquanto o job estava na fila não dispara trabalho extra, e a label "capturando…" some.
- **Upload manual mexe em 3 colunas no mesmo UPDATE.** `repository.UpdateOGImage` seta `og_image_url`, `preview_status='ok'` e `preview_error=NULL` atomicamente. Sem race com o worker (transação no mesmo row).
- **Screenshot fallback** (`Worker.maybeScreenshot`): após o fetch HTML, **se** `og:image` veio vazio **e** o link ainda não tem `og_image_url` (não foi feito upload manual) **e** o host resolve pra IP público (via `IsPublicURL`) **e** o worker foi inicializado com `WithScreenshotFallback(sc, up)`, então:
  1. `sc.Capture(ctx, url)` — Chromium headless via `internal/screenshot/` (`go-rod`), viewport 1280×800
  2. `imageopt.Optimize(png, …)` — downscale ≤ 1024 px + re-encode JPEG q≈82 (ver "Pipeline de imagens" abaixo)
  3. `up.DeleteObject(ctx, "screenshots/{id}.{png,gif,webp}")` — purga extensões legadas pra esse id
  4. `up.Upload(ctx, "screenshots/{id}.jpg", jpg, "image/jpeg")` — MinIO
  5. `repo.UpdateOGImage(ctx, id, "/api/files/screenshots/{id}.jpg")`
  
  O fallback é **silencioso em falha** (apenas loga) — o link permanece sem imagem. Falhas comuns: site bloqueando bots, JS-heavy page sem og:image, Chromium ausente. Se `imageopt.Optimize` retornar erro (corrupção rara), o worker armazena o PNG cru em `screenshots/{id}.png` como fallback — nunca aborta a etapa só por causa do re-encode.

## Change-check worker (`internal/changecheck`)

Detecção periódica per-link de mudança de conteúdo. Opt-in via `link.check_interval ∈ {hourly, daily, weekly}` (default NULL = desativado). Disparo de Web Push quando o fingerprint muda.

- **Worker** (`worker.go`): pool de N goroutines (`CHANGECHECK_WORKER_CONCURRENCY`, default 2) + scanner que roda a cada `CHANGECHECK_SCAN_INTERVAL_SEC` (default 60s). Skeleton idêntico ao `preview.Worker`: `atomic.Bool stopped`, `sync.Once Stop`, channel buffered (256). `Enqueue` retorna `ErrQueueFull`/`ErrStopped`.
- **Scanner** (`scan`): SELECT com `CASE WHEN check_interval='hourly' THEN '1 hour' ...` resolve "due" sem hardcode no Go. O índice `link_check_due_idx` é parcial — varre só os opt-in, O(opt-in) não O(total).
- **Janela rolante, NÃO horário fixo.** O agendamento NÃO é cron-style — não roda "à meia-noite" nem "às 3am". Um link `daily` rodado pela primeira vez às 14:37 do dia 1 fica due novamente em ~14:37 do dia 2 (com drift de até `CHANGECHECK_SCAN_INTERVAL_SEC` + tempo do fetch HTTP). A predicação é `last_checked_at < now() - interval` e o tie-break é `ORDER BY COALESCE(last_checked_at,'epoch') ASC, id ASC` — links opt-in pela primeira vez (`last_checked_at IS NULL`) entram no scan imediatamente. Sem timezone awareness: usa `now()` do Postgres (UTC no container). Sem jitter — 100 links marcados juntos rodam juntos em batches de até 256 por tick. Catch-up automático no boot do backend: tudo que ficou vencido durante o downtime é processado em ordem pelo `last_checked_at` mais antigo.
- **Fingerprinter** (`fingerprint.go`): híbrido. Primeiro extrai `<link rel="alternate" type="application/(rss|atom)+xml">` e hashea os IDs/GUIDs ordenados. Se não tem feed, fallback content hash em `<main>`/`<article>` (whitespace-normalized; remove `<script>`/`<style>`/`<nav>`/`<header>`/`<footer>`).
- **Prefixo `feed:`/`content:` no hash armazenado é discriminador** — quando uma página content-only ganha um feed novo, a troca `content:` → `feed:` é tratada como "novo baseline", **não** como mudança. Sem o prefixo o primeiro scan pós-feed dispararia push falso.
- **First observation nunca conta como change.** `last_fingerprint IS NULL` → grava o novo hash sem bumpar `last_change_detected_at`. Sem isso, todo opt-in viraria push no primeiro scan.
- **Reusa o `preview.Fetcher`** via interface `HTTPGetter` (`GetRaw`) — o mesmo `safeDialer` com pre-dial LookupIP + post-dial RemoteAddr. Forkar um HTTP client aqui dividiria a postura SSRF.
- **Push é fire-and-forget.** `worker.process` lança `sender.Notify` em goroutine com `context.Background()` + 15s timeout, isolado do `RecordCheckResult` durável. Falha de push nunca rolla back a detecção.
- **Erros isolados em `last_check_error`** — não polui `preview_error` (worker diferente, surface diferente no LinkCard).

## Web Push (`internal/push`)

Notificação background quando o changecheck detecta change. RFC 8030 + VAPID via `github.com/SherClockHolmes/webpush-go`.

- **VAPID** (`vapid.go`): `LoadOrGenerate` prioriza env (`VAPID_PUBLIC_KEY`/`VAPID_PRIVATE_KEY`/`VAPID_SUBJECT`) → state file (`VAPID_STATE_PATH`, default `/data/vapid.json`) → autogen + persiste com `os.WriteFile(..., 0o600)` (umask não confiável). Volume `foldex-data:/data` no compose preserva entre recreations; pinar em `.env` mantém subscriptions estáveis.
- **Subscription repo** (`subscription.go`): `INSERT … ON CONFLICT (endpoint) DO UPDATE SET p256dh, auth` — renovação do browser converge no mesmo row.
- **Sender** (`sender.go`): fan-out paralelo. 404/410 → `DeleteByEndpoint` (RFC 8030 §7.3 — endpoint morto). 2xx → `MarkUsed`. Transport errors → log + segue (blip de rede não apaga subscription).
- **Handler** (`handler.go`): rotas montadas só quando `PushHandler != nil`. Tudo herda o `SHARED_SECRET` middleware (inclusive `vapid-key` — não vaza superfície).
- **Service Worker hand-rolled** (`web/src/sw.ts`): Cache API + `push` listener + `notificationclick` listener. `vite-plugin-pwa` com `strategies: 'injectManifest'` injeta `__WB_MANIFEST` no build sem trazer runtime workbox-* (que exigiria regenerar `bun.lock`).

## Pipeline de imagens (`internal/imageopt`)

Todo byte que entra no MinIO via upload do usuário ou via screenshot fallback passa por `imageopt.Optimize(data, Options{MaxDim: 1024, Quality: 82})` antes do `Upload`. Implementação 100% Go (`image/png|jpeg|gif` da stdlib + `golang.org/x/image/draw` pra resize Catmull-Rom + `golang.org/x/image/webp` só pra decode); sem CGO, sem libwebp no Dockerfile.

**Algoritmo:**

1. `http.DetectContentType` sniff dos bytes; rejeita qualquer MIME fora de `{image/png, image/jpeg, image/gif, image/webp}` → `ErrUnsupportedFormat`.
2. `image.Decode` decodifica usando o decoder registrado pelo MIME sniffed. Falha → `ErrDecode`.
3. Se algum lado > 1024 px, calcula `(W', H')` preservando aspect ratio.
4. Cria `*image.RGBA` no tamanho final preenchido com branco, depois `draw.CatmullRom.Scale(…, draw.Over)` pra blendar a fonte. Isso resolve resize + composição de alpha sobre branco numa só operação (JPEG não tem alpha — sem o branco, pixels transparentes virariam pretos).
5. `jpeg.Encode` com `Quality: 82`.
6. **Guard de não-regressão (só pra JPEG de entrada):** se a entrada já era JPEG, não foi feito resize, e o output ficou ≥ ao input, devolve os bytes originais. Pra PNG/GIF/WebP, sempre re-encoda — garante que `images/{id}.jpg` é o caminho canônico no MinIO.

**Pontos de chamada:**

- `internal/links/screenshot_handler.go:UploadImage` — uploads manuais (`POST /api/links/{id}/image`). Cap de body = 5 MiB (`MaxBytesReader`), pixel cap = 50 MP via `imageopt.DecodeConfig`.
- `internal/links/screenshot_handler.go:CaptureAndStore` — screenshot sob demanda (`POST /api/links/{id}/screenshot`). **Gate SSRF obrigatório**: chama `links.URLPolicy` (passada por `main.go` como `preview.IsPublicURL`) antes de invocar o Chromium — rejeita scheme não-http(s) com 400 `invalid_scheme` e IMDS/RFC1918/loopback com 400 `private_target`. Policy nil = fail-closed (deny). Sem esse gate o endpoint vira read-anywhere (`file:///etc/passwd` → screenshot → `/api/files/`).
- `internal/preview/worker.go:maybeScreenshot` — screenshot fallback do worker (mesma `IsPublicURL`).

Cada um, além do `Optimize`, dispara `DeleteObject` nas extensões irmãs do mesmo id (purga de orphans quando o formato muda de `.png` pra `.jpg`). `DeleteObject` é idempotente (NoSuchKey = sucesso). **Arquivos antigos pré-deploy ficam intocados** — o `ProxyFile` continua servindo `.png/.gif/.webp` históricos sem mudança.

### imageopt — decode-bomb guard

`imageopt.Optimize` chama `image.DecodeConfig` antes de `image.Decode` e rejeita com `ErrTooLarge` qualquer payload cujas dimensões declaradas excedam `maxPixels = 50_000_000` (50 MP). Sem isso, um PNG de ~30 KB declarando 60000×60000 alocaria ~14 GB de RGBA em `image.NewRGBA` e travaria o backend. O cap é generoso para qualquer foto de celular (top consumer é ~108 MP, mas esses comprimem para >5 MB e o upload pré-cap de 5 MiB já corta antes).

## Portas, hostnames e deploy local

- **Backend:** `127.0.0.1:9089` no host, `9089` no container. Lê `BACKEND_PORT` do env.
- **Web (nginx servindo bundle Vite):** `127.0.0.1:9088 → nginx:80` no container. Proxa `/api` e `/go` pro `backend:9089` na rede `foldex`.
- **Postgres:** o `docker-compose.db.yml` traz `foldex-db` (postgres:18.2-alpine) na rede `foldex` **sem publicar porta no host** por default (evita conflito com outras instâncias). Pra reusar um Postgres já rodando no host (ex: `postgres18`), setar `POSTGRES_HOST=localhost` em `.env` — o container backend resolve `localhost` pro host real via `extra_hosts`.
- **Network compose:** rede `foldex` externa (nomeada), pra que apps e db sejam composes separados.

## Variáveis de ambiente

Todas em `.env` (gitignored). Defaults sane em `.env.example`:

```
POSTGRES_USER=foldex
POSTGRES_PASSWORD=foldex
POSTGRES_DB=foldex
POSTGRES_PORT=5432
POSTGRES_HOST=db            # `db` (compose), `localhost`, `host.docker.internal`, ou hostname externo
POSTGRES_SSLMODE=disable    # disable | require | verify-full
BACKEND_PORT=9089
WEB_PORT=9088
VITE_API_BASE=http://localhost:9089
PREVIEW_WORKER_CONCURRENCY=4
PREVIEW_FETCH_TIMEOUT_SEC=5
PREVIEW_STRICT_SSRF=        # vazio = permissivo; "1" = strict
SHARED_SECRET=              # vazio = sem auth; setado = exige X-Foldex-Secret nos /api/*
CORS_ORIGINS=*
BACKEND_BIND=127.0.0.1      # bind do backend; non-loopback + SHARED_SECRET vazio + CORS=* recusa boot

# Change-check worker (mig 000010)
CHANGECHECK_ENABLED=1
CHANGECHECK_WORKER_CONCURRENCY=2
CHANGECHECK_SCAN_INTERVAL_SEC=60
CHANGECHECK_FETCH_TIMEOUT_SEC=20

# Web Push / VAPID (mig 000011) — autogen on first boot se *_KEY vazios; pinar pra subscriptions estáveis
VAPID_PUBLIC_KEY=
VAPID_PRIVATE_KEY=
VAPID_SUBJECT=mailto:foldex@localhost
VAPID_AUTO_GENERATE=1
VAPID_STATE_PATH=/data/vapid.json   # 0o600; volume `foldex-data:/data` no compose
```

DB_URL é DERIVADO desses (em `docker-compose.yml` e `backend/Makefile`). Não duplicar.

## Import/Export formats

**Netscape Bookmarks HTML** (formato do Chrome export):

```html
<!DOCTYPE NETSCAPE-Bookmark-file-1>
<DL><p>
  <DT><A HREF="https://news.ycombinator.com" ADD_DATE="1715520000" ICON="data:...">Hacker News</A>
  <DT><H3>Jira</H3>
  <DL><p>
    <DT><A HREF="https://jira.example/board/1">Board 1</A>
  </DL><p>
</DL><p>
```

Parser usa `golang.org/x/net/html` e percorre `<A>` + stack de `<H3>`. **Semântica atual (pós-folders):** o `<H3>` mais profundo no escopo de cada link vira `folder` (pasta), e os `<H3>` ancestrais viram `tags`. Ex: `Bookmarks Bar → Work → Issues → linkA` resulta em `linkA.folder = "Issues"` + `linkA.tags = ["Bookmarks Bar", "Work"]`. Foldex folders são flat (1 nível) — o aninhamento é colapsado pro mais profundo. Insert idempotente: `INSERT ... ON CONFLICT (url) DO NOTHING`. Folders são resolvidas via `ensureFolder(name)` (match-or-create por nome) ou criadas a partir do array `folders[]` do JSON.

**JSON versionado** (formato próprio):

```json
{
  "version": 2,
  "exported_at": "2026-05-13T18:50:00Z",
  "tags": [
    { "name": "jira", "color": "#1f6feb", "icon": "🪲" }
  ],
  "folders": [
    { "name": "Trabalho", "color": "#0EA5E9" }
  ],
  "links": [
    {
      "url": "https://jira.example/board/1",
      "title": "Board 1",
      "description": "Sprint board",
      "tags": ["jira"],
      "folder": "Trabalho",
      "click_count": 47,
      "created_at": "2026-04-01T12:00:00Z"
    }
  ]
}
```

**Versionamento**: importer aceita v1 (pré-folders, sem `folders[]`/`folder`) e v2 (com pastas). Exporter sempre escreve v2. Round-trip idempotente: re-importar um JSON exportado não duplica folders (match por name via `ensureFolder`).

## Browser extension

- Manifest V3, permissions: `activeTab`, `storage`.
- Popup (React) abre ao clicar no ícone; lê `chrome.tabs.query({active:true})` → prefill URL + title.
- Tag picker carrega `GET /api/tags` do backend configurado (options page).
- Save → `POST /api/links` → toast → fecha popup.
- Options page: input pra `BACKEND_BASE_URL` (default `http://localhost:9089`), botão "Test connection" que faz `GET /healthz`.
- Build: `@crxjs/vite-plugin` gera `dist/` com manifest expandido; carrega como unpacked extension.

## Deploy local

`docker compose up` sobe três containers; web é multi-stage build com nginx servindo o bundle estático. Nginx proxa `/api → backend:9089` pra evitar CORS no produto final (a SPA chama `/api/...` relativo). Backend só responde em `127.0.0.1` no host (porta `9089` por padrão; web em `9088`).

Backup recomendado: cron de `pg_dump` (template em `scripts/backup.sh`).

## ADRs

### ADR-1 — Worker in-process em vez de Redis/queue
Single-user, baixa taxa de escrita (alguns links por dia). Channel buffered + pool de goroutines elimina deploy de broker e dependência operacional. Trade-off: jobs perdidos em crash; mitigado por `preview_status='pending'` + endpoint `refresh-preview`. **Revisitar** se virar multi-user ou se importarmos milhares de links de uma vez.

### ADR-2 — `golang-migrate` sobre goose/tern
Padrão `000NNN_*.up/down.sql` já é o que o `app-genfin` usa. Mantém memória muscular e Makefile compartilhável. Não precisamos de Go-migrations (goose) nem de migrate-on-startup automágico (tern).

### ADR-3 — Sem auth no MVP
Backend bindado em `127.0.0.1` via Compose. `SHARED_SECRET` opcional já está no middleware (default off). Endurece a extensão sem rework quando precisarmos.

### ADR-4 — MUI sobre Chakra/shadcn
MUI é o que o usuário já usa em outros projetos pessoais. shadcn exige mais setup; MUI dá command palette (Autocomplete), Dialog, Snackbar, Drawer prontos.

### ADR-5 — `/go/:id` fora de `/api`
URL curta (`http://foldex.local/go/47`) é compartilhável. Evita preflight CORS (é GET). Isola o efeito colateral (bump do contador) do CRUD.

### ADR-6 — Extension sem código compartilhado com SPA
Manifest V3 service worker faz HTTP plain pro `/api/links`. Sem npm workspace no v1; tipos `Link`/`Tag` ficam duplicados na extension (5 campos, sem custo real). Se incomodar, promover a `packages/shared-types`.

### ADR-7 — `/go/{id-or-slug}` aceita ambos (Done — migration 000009)
A versão original (numeric-only) foi implementada primeiro porque IDs são triviais e slugs adicionam constraint UNIQUE + UX de "escolher o slug". Quando a base passou de "alguns links pessoais" pra "links que você quer compartilhar com a equipe", a leitura de `localhost:9089/go/42` virou ruído — daí a evolução pra slugs amigáveis.

**Como funciona:** `link.slug TEXT NOT NULL UNIQUE` (migration 000009) com CHECK `^[a-z0-9]+(-[a-z0-9]+)*$ AND NOT ^[0-9]+$`. Slug é auto-derivado do título no create via `Slugify` (lowercase ASCII, accent-fold, hyphen-collapse, max 80 chars na hyphen-boundary); usuário pode override no `LinkDialog`. Backfill SQL no up.sql cobre os links existentes.

**Resolução `/go/{valor}`:** ID-first (preserva backward-compat de todo `/go/42` antigo), depois slug-fallback. A constraint que rejeita slug puramente numérico garante que nunca há ambiguidade — `/go/42` SEMPRE significa link 42.

**Backup/import/export:** snapshot inclui slug; restore com `mode=skip|wipe|duplicate` resolve colisões com sufixo `-2`, `-3`, … via `uniqueLinkSlug`. Importer (Netscape/JSON) gera slug auto pra cada link novo.

### ADR-8 — SSRF guard no preview fetcher
Fetcher visita URLs arbitrárias fornecidas pelo usuário. **IMDS (169.254.169.254) é sempre bloqueado**, sem opt-out — é o único alvo que nunca é legítimo num app pessoal. Os demais ranges privados (loopback, RFC1918, link-local, IPv6 ULA) só são bloqueados quando `PREVIEW_STRICT_SSRF=1`. Default é permissivo: foldex é single-user local e links de intranet (Jira/Grid/Confluence/dashboards internos) são caso de uso primário — bloquear o que o usuário visita no próprio browser todos os dias é fricção sem ganho. Revisitar se virar multi-user ou expor o backend pra rede pública.

### ADR-9 — Click_log como única fonte de verdade
Migration 000006 dropou `link.click_count` e `link.last_clicked_at`. Cliques agora vivem só em `click_log`. Contagens e timestamps são derivados via `LEFT JOIN LATERAL` no SELECT. **Por quê:** durante o desenvolvimento, percebemos que mantinhamos dois lugares pra contar (UPDATE atômico no link + INSERT no click_log) e qualquer divergência seria irrecuperável (qual é a verdade?). Single source of truth elimina o problema. **Trade-off:** O(log N) lookup por link na listagem (mitigado pelo índice `click_log_link_id_ts`). Pra single-user com até 10k links, é irrelevante. Se virar gargalo no futuro: materialized view com REFRESH no /go handler.

### ADR-10 — Pin é coluna na `link`, não tabela
`link.pinned BOOLEAN` (migration 000005) + índice `link_pinned_created (pinned DESC, created_at DESC)`. Optei por coluna em vez de tabela separada `pinned_links` porque (a) é 1:1 com link (b) toggle é uma operação simples (c) ORDER BY pinned DESC é trivial. Hipotético upgrade futuro pra "pinado por contexto/lista": só virar uma tabela `link_pin (link_id, list_id)`.

### ADR-11 — Postgres host configurável (`db` / `localhost` / `host.docker.internal`)
`docker-compose.yml` deriva `DB_URL` de `POSTGRES_HOST` (e `POSTGRES_SSLMODE`). O backend container declara `extra_hosts: ["localhost:host-gateway", "host.docker.internal:host-gateway"]` pra que ambos os nomes resolvam pro host real, não pra ele mesmo. **Por quê:** o usuário pode ter um Postgres já rodando no host e querer reusar; também serve quando se troca pra RDS/Neon (basta setar `POSTGRES_HOST=hostname-real`). Foi importante MANTER `POSTGRES_HOST=db` como default no `.env.example` pra quem usa o `docker-compose.db.yml`.

### ADR-12 — SSRF guard permissivo por default
IMDS (`169.254.169.254`) é sempre bloqueado (sem opt-out — único alvo que nunca é legítimo). Os outros ranges privados (loopback, RFC1918, link-local, IPv6 ULA) só são bloqueados quando `PREVIEW_STRICT_SSRF=1`. **Por quê:** foldex é single-user local; intranet (Jira, Grid, Confluence, dashboards internos) é caso de uso primário. Bloquear o que o usuário visita no browser todos os dias = fricção sem ganho. Revisitar se virar multi-user ou expor publicamente.

### ADR-13 — Confirm modal próprio (não window.confirm) + Esc fecha tudo
`useConfirm({ title, message, destructive })` retorna uma `Promise<boolean>`. Substitui qualquer `window.confirm()`. Tipografia coerente com o resto (Space Grotesk + Nunito Sans), botões com gradient indigo/vermelho, kicker mono. **Por quê:** `confirm()` quebra o tom visual e o teclado fica preso ao chrome do browser. Esc em qualquer modal cai por hook `useEscape(onClose, open)`.

### ADR-14 — Gerenciamento de tags via modal próprio (não inline na sidebar)
Sidebar mostra só lista enxuta (dot + nome + count). Edit/delete moveu pra `TagManagerDialog` aberto pelo botão "Gerenciar tags" no rodapé da sidebar. **Por quê:** botões inline por linha brigavam com o layout `grid-template-columns: 16px 1fr auto` e quebraram em N+1 linhas em vez de uma. Tag management é ação eventual, não navegação — modal próprio é o lugar certo.

### ADR-15 — Coverage gate de 85%
Definido em `CLAUDE.md`. Backend: `make coverage` roda unit + integration tests com `-coverpkg` excluindo `cmd/server`, `internal/db`, `internal/testdb` e falha se total < 85%. Frontend: `vitest.config.ts` define `thresholds.lines/statements/functions: 85, branches: 80`. Toda mudança de comportamento deve vir com teste no mesmo PR.

### ADR-10 — Versões "always-latest-stable"
Antes de pinar uma dep nova, conferir `https://go.dev/dl/` e `npm view <pkg> version --registry=https://registry.npmjs.org/` (sempre o registro público, nunca um mirror privado, pra checagem de versão). Tabela de versões correntes vive em `CLAUDE.md` §1.

### ADR-16 — Screenshot só como fallback (nunca obrigatório)
A captura de tela headless (`internal/screenshot/` via `go-rod`) **só roda** quando o fetch HTML não devolveu `og:image`, o usuário ainda não fez upload manual, **e** o host resolve pra IP público (`preview.IsPublicURL`). Os três gates são curto-circuito — qualquer falha desliga o screenshot e o link fica sem imagem (em vez de mostrar uma tela de login interna ou consumir Chromium em vão). MinIO ausente = fallback desligado, demais endpoints continuam ok. **Por quê:** screenshot é caro (Chromium + I/O), arrisca expor páginas internas, e na maioria dos sites públicos o `og:image` já cobre. Fallback troca "imagem pobre" por "alguma imagem" sem dar custo no caminho feliz.

### ADR-19 — Folders 1:N exclusivo (containment) coexistindo com tags M:N (labels). Pastas aninhadas via self-FK.
`folder` é uma tabela nova, separada de `tag`, e `link.folder_id` é 1:N (`ON DELETE SET NULL` — quando uma pasta some, os links voltam pra raiz soltos). Folders também são **aninhadas** entre si via `folder.parent_id` (também `ON DELETE SET NULL` — quando pasta-pai some, filhas viram root).

**Por que tabela separada se a coluna `name`/`color` é parecida?**
- **Semântica diferente.** Pasta é onde o link *vive* (containment); tag é como o link *é descrito* (label). Met. iPhone — app está em UMA pasta, mas pode ter várias palavras-chave.
- **Home view filtra**. `GET /api/links?ungrouped=1` retorna só links com `folder_id IS NULL`. Sem essa exclusividade, o link apareceria 2x (dentro do card da pasta E na home).
- **Sem UNIQUE em folder.name.** iPhone permite duplicatas. ID é a identidade real.
- **Aninhamento via self-FK.** Diferente do iPhone (1 nível), foldex permite N níveis. Navegação é via stack interno (`folderPath: number[]`) — sem URL state, sem rotas, sem IDs no address bar.

**Comportamento UX (enforced no frontend):**
- Home (sem `openFolder`) = `<FolderCard>`s das pastas-raiz (`useFolders({scope:'root'})`) + links ungrouped no mesmo `fx-grid`.
- Dentro de uma pasta = subpastas (`useFolders({scope: openFolder})`) + links da pasta atual (`useLinks({folderId: openFolder, tagIds})`). Sidebar de tags **continua ativa** — filtros compõem com a pasta via AND no SQL (`folder_id = N AND tag_id = M`).
- "Nova pasta" criada dentro de uma pasta vira **subpasta** (POST `/api/folders` com `parent_id = openFolder`).
- Esc / "← Pastas" sobe **um nível** (não pula direto pra raiz) — implementado via `setFolderPath(path.slice(0, -1))`.
- `LinkDialog` e `CommandPalette` continuam usando `useFolders()` flat (sem scope) — pickers globais que precisam ver tudo.
- **Compactar pastas (RapidView).** Quando muitos folders cheios estouram a tela, o toggle do Topbar (`fx-viewseg`, visível só em `viewMode === 'cards'`) colapsa cada `FolderCard` numa tira fina (esconde a preview 2×2 e mantém só nome+contagem). O estado é per-context, persistido em `foldex.foldersCompact.map` keyed `home`/`folder.<id>` — mesma estratégia do `viewMode.map`, com o mesmo `useEffect` de pruning de chaves órfãs. Hover/focus no nome do folder dispara o `FolderRapidView`: um popover portal-mounted que lista as subpastas + primeiros links **lendo `preview_folders`/`preview_links` que já vêm em `useFolders`** — sem fetch extra. Cap de 10 itens com footer `+N mais` derivado de `link_count + folder_count − rows.length`; folders vazios não montam o popover.

**Delete behavior** (2 paths):
- `DELETE /api/folders/{id}` (manter links): só a pasta morre. Links voltam pra root (ON DELETE SET NULL em `link.folder_id`). Subpastas viram root (ON DELETE SET NULL em `folder.parent_id`).
- `DELETE /api/folders/{id}?cascade=1` (apagar tudo): recursivo via CTE — coleta toda a subtree, deleta links em todos os níveis, então deleta as pastas.

### ADR-18 — Grid layout: CSS Grid + density picker (não column-count)
`.fx-grid` e `.fx-pingrid` usam `display: grid; grid-template-columns: repeat(var(--fx-cols, 5), minmax(0, 1fr))`. O usuário troca a densidade entre **3, 5, ou 8 colunas** via `<DensityPicker>` integrado no `fx-viewseg` do Topbar (só visível em `viewMode === 'cards'`). Estado persiste em `localStorage` como `foldex.grid.cols`.

**Por quê CSS Grid e não `column-count`?** Multi-column distribui itens verticalmente e tenta balancear altura — com 6 cards em 5 colunas, o 6º ia parar no meio. Grid preenche row-major (esquerda → direita), sempre.

**Por quê 3/5/8 explícitos e não responsivo puro?** Foldex é app pessoal, o usuário sabe quanta densidade quer no monitor dele. Breakpoints só servem como teto inferior (≤980px → 2 cols; ≤640px → 1) pra não esmagar em mobile.

### ADR-17 — Tag color aceita CSS gradient inline (sem nova coluna)
`tag.color` é `TEXT` e aceita tanto um hex sólido (`#6366F1`) quanto um `linear-gradient(135deg, #a, #b)` completo. Frontend detecta via `isGradient()` em `web/src/lib/tagColor.ts` e:
- Chip text/borda usam `primaryColor()` (primeira parada) porque `color-mix(in srgb, var(--chip-c) X%, …)` precisa de cor sólida — gradiente quebraria;
- Dot do chip recebe o gradiente real via inline style;
- Sidebar/manager/palette dots já usam `background: t.color` direto, então o gradient renderiza sem mudança.

**Por quê uma string e não duas colunas (`color_from`/`color_to`)?** Mantém o schema estável, evita migration, e o backend não precisa saber a diferença — ele é só storage. Custo: queries SQL não conseguem filtrar "tags com gradient" sem `LIKE 'linear-gradient%'`, mas não temos esse caso.

### ADR-20 — Backup & Restore como ZIP único, idempotente, com 3 modos de conflito
Detalhe completo em [SDD-BACKUP-RESTORE.md](./SDD-BACKUP-RESTORE.md). Resumo das decisões load-bearing:

- **Um ZIP é a unidade de backup.** Contém `manifest.json` + `database.json` (todas as 5 tabelas) + `files/screenshots/` + `files/images/`. `og_image_url` continua como proxy URL `/api/files/<key>` — não embarca bytes inline em base64.
- **Streaming end-to-end.** Export usa `zip.NewWriter(http.ResponseWriter)` + `io.Copy` direto do MinIO GetObject. Restore usa `MultipartReader` (não `ParseMultipartForm`). Bucket de centenas de MBs sobrevive sem buffer.
- **3 endpoints**: `/api/backup` (gera), `/api/backup/validate` (sem efeito colateral — confere checksums + manifest + conflitos com DB atual), `/api/backup/restore?mode=…`.
- **3 modos de conflito**:
  - `wipe`: TRUNCATE 5 tabelas + DELETE prefix MinIO + restore com IDs originais preservados. UI exige confirm destrutivo.
  - `skip` (default): `ON CONFLICT DO NOTHING` em `tag.name`/`link.url`, mapping `oldID→curID` pra link_tag/click_log re-key.
  - `duplicate`: tags renomeiam pra `nome (N)`, folders sempre criam novo, links com URL conflict caem pra skip + warning (URL é UNIQUE — duplicar quebraria invariant).
- **REPEATABLE READ no export** garante que as 5 SELECTs vejam um snapshot consistente.
- **Validação prévia** é obrigatória no frontend: usuário vê manifest + counts + conflitos antes de escolher modo e confirmar.
- **`schema_version` no manifest** rejeita backups de futuro; backups antigos podem rodar com warning (campos novos default).
- **Restore não é atômico DB+MinIO** (sem 2PC entre Postgres e S3). Writes idempotentes + re-rodar com mesmo zip converge.

### ADR-21 — Paste anywhere = New Link dialog pre-filled
**Status:** Done.

Listener document-level (`web/src/hooks/usePasteUrl.ts`) intercepta `paste` no
`document` e, se o payload do clipboard parecer uma URL (`web/src/lib/url.ts:looksLikeUrl`),
abre o `LinkDialog` com `initialUrl=<clipboard>`. No-op quando o `e.target` é
editável (INPUT/TEXTAREA/SELECT/contentEditable) ou quando qualquer `.fx-overlay`
já está montado — sem hijack do paste dentro da busca, dentro de outro modal,
ou enquanto a palette está aberta. **Por quê** um listener de documento em
vez de campo: aceita "Ctrl+V em qualquer lugar da página", inclusive no menu
nativo "Paste" do iOS Safari, sem precisar mudar foco antes. **Por quê não
publicar a feature como atalho `⌥V`**: o evento `paste` nativo já carrega
o clipboard sem prompt de permissão; um atalho explícito teria que ler via
`navigator.clipboard.readText()` que requer HTTPS + permissão.

Detecção é tolerante: aceita `http(s)?://`, `ftp://`, `file://`, e hosts
bare como `example.com/x`. Rejeita números puros, palavras soltas, strings
com whitespace, e schemes não-web (`mailto:`, `tel:`, `javascript:`). O
gotcha que motivou a checagem extra: `new URL("https://42")` parseia
hostname pra IPv4 `0.0.0.42` (octets implícitos), o que daria false-positive
para qualquer número solto — daí o `trimmed.includes('.')` antes do parse
no implicit-https path. 16 unit tests cobrem os edge cases em
`web/src/lib/url.test.ts`.

### ADR-22 — Mobile-first responsive (PWA-grade)
**Status:** Done.

Single SPA serve desktop + mobile via 3 breakpoints em `web/src/styles/foldex.css`:
- **≤980px / ≤640px**: grid de cards cai pra 2 / 1 colunas (teto inferior,
  override de qualquer densidade salva).
- **≤768px**: topbar vira **single row** com 5 elementos exatos:
  `[hamburger] [fx-mark] [search] [home + stats] [⋯]`. Tudo o que sobrou —
  sort, view, density, locale, theme, import/export, new folder, new link —
  vai pra dentro do popover do "⋯" (`MobileOverflowMenu`). Sidebar vira
  off-canvas drawer (`transform: translateX(-100%)`, `position: fixed`,
  z-index 90). FAB redondo aparece no canto inferior direito pra new-link
  rápido.
- **≤600px**: dialogs viram full-screen (`width: 100vw`, `height: 100dvh`,
  border-radius 0). `LinkDialog` ainda stack 2-cols → 1-col com header e
  footer sticky; `CommandPalette` ganha botão X (esc não existe no teclado
  virtual) + tap-no-backdrop fecha. Inputs sobem pra min-height 44px (alvo
  iOS), font 15px, footer respeita `env(safe-area-inset-bottom)`.

**Gotcha load-bearing**: `web/src/styles/overrides.css` é carregado **depois**
de `foldex.css`, então qualquer regra ali com a mesma specificity vence o
cascade — mesmo regras dentro de `@media` em `foldex.css`. Por isso o
`.fx-frame` (e `.fx-topbar`, `.fx-topbar .fx-search`) em `overrides.css`
estão escopados em `@media (min-width: 769px)`. Adicionar nova regra
"desktop-only" em `overrides.css` exige o mesmo wrapping ou a mobile
quebra silenciosamente.

PWA: `vite-plugin-pwa` com `strategies: 'injectManifest'` (Workbox SÓ pra injetar a precache list em `self.__WB_MANIFEST`; runtime workbox-* NÃO entra no bundle). SW hand-rolled em `web/src/sw.ts`: Cache API + `push` + `notificationclick` listeners. Detalhe completo no ADR-24.

### ADR-23 — Change detection: hybrid fingerprint (feed + content), prefix discriminator
**Status:** Done (migration 000010, PR #5).

Per-link opt-in via `link.check_interval ∈ {hourly, daily, weekly}`. Worker em `internal/changecheck` faz fingerprint híbrido: extrai `<link rel="alternate" type="application/(rss|atom)+xml">`, hashea GUIDs ordenados; fallback content hash em `<main>`/`<article>` (whitespace-normalized, sem `<script>`/`<style>`/`<nav>`/`<header>`/`<footer>`).

**Por quê duas estratégias.** Feed é o caminho ouro — mudança de feed quase sempre é mudança de conteúdo real, e enumerar items ordenados é estável (reordenação no servidor não dispara push). Content hash é fallback porque a maioria das páginas internas (Jira boards, Confluence) não tem feed; sem ele, o opt-in só funcionaria pra blogs.

**Por quê o prefixo `feed:`/`content:` no hash.** Páginas content-only mudam pra ter feed um dia. Sem o discriminador, a troca `content:` → `feed:` ia disparar push falso ("conteúdo mudou!"). O worker em `process()` exige `prevKind == newKind && prevHash != newHash` pra contar como change; troca de kind = re-baseline silencioso.

**Por quê "first observation nunca conta".** `last_fingerprint IS NULL` é o sinal — grava hash sem bumpar `last_change_detected_at`. Sem essa regra, todo opt-in dispararia push no primeiro scan, que é o oposto de útil.

**Por quê reusar `preview.Fetcher`.** O SSRF guard (pre-dial LookupIP + post-dial RemoteAddr, IMDS sempre bloqueado) é load-bearing. Forkar um HTTP client em `changecheck` dividiria a postura — duas pernas pra defender contra a mesma classe de bug. Interface mínima `HTTPGetter` exporta só `GetRaw`.

**Por quê `last_check_error` separado de `preview_error`.** Workers diferentes, superfícies diferentes. `preview_error` aparece no LinkCard como "preview falhou"; sobrepor erros de changecheck ali ia confundir o usuário (link tem preview ok, mas o card diria falhou). CLAUDE.md §4 "Worker is the only writer" — preview worker é dono daquele par de colunas.

### ADR-24 — Web Push: VAPID auto-gen on boot + hand-rolled SW
**Status:** Done (migration 000011, PR #5).

Notificações background quando o changecheck detecta change. RFC 8030 com VAPID via `webpush-go`. Single-user: `push_subscription` sem `user_id` (revisitar quando multi-user landar).

**Por quê VAPID auto-gen on first boot.** Plug-and-play: `make up` em um host limpo gera a key, persiste em `/data/vapid.json` (0o600), e o front busca via `GET /api/push/vapid-key`. Pinar `VAPID_*` em `.env` quando quiser manter subscriptions estáveis entre recreations. O volume nomeado `foldex-data` cobre o caso "esqueci de pinar".

**Por quê 404/410 → DELETE.** Convenção RFC 8030 §7.3 — endpoint morto. Sem cleanup, `push_subscription` acumula rows zumbis pra cada Chrome reinstalado / Safari resetado / device descartado. Transport errors (DNS, timeout) NUNCA disparam DELETE — um blip de rede apagaria subscriptions vivas.

**Por quê o sender é fire-and-forget no worker.** `worker.process` lança `sender.Notify` em goroutine com `context.Background()` + 15s timeout. Push lento não pode rollback o `RecordCheckResult` que é a fonte da verdade pra "este link mudou?". Falha de push = log, segue.

**Por quê SW hand-rolled em vez de `workbox-*` runtime.** `bun.lock` é fonte da verdade (CLAUDE.md §1) e adicionar workbox-* runtime exigiria regenerar lock + revalidar 200+ deps transitivas. Um par de `cache.put` + `push`/`notificationclick` listeners cabe em ~80 linhas (`web/src/sw.ts`). `vite-plugin-pwa` com `strategies: 'injectManifest'` injeta só o `__WB_MANIFEST` no build — zero runtime workbox.

**Por quê `/api/push/vapid-key` atrás do `SHARED_SECRET` middleware.** "É só a chave pública" não justifica vazar superfície — um attacker remoto enumerando endpoints saberia que foldex tem push wired. Tudo `/api/push/*` herda o guard.

## Future considerations

- **Auth + multi-user.** Login local (bcrypt + JWT) ou OAuth Google. Tabelas `user_id` em `link`/`tag`.
- **Sync entre máquinas.** Hospedar Postgres remoto, ou criar `foldex-sync` que replica via litestream.
- **AI suggestions.** Sugerir tags ao criar (LLM lê título + descrição), agrupar duplicatas.
- **Favicon cache local.** Worker baixa e armazena em volume; resolve broken icons offline/VPN.
- **Public sharing.** Sub-set de links visível sem auth (read-only link de partilha).
