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
              │ │   RustFS           │ │
              │ └───────────────────┘ │
              └────────┬───────┬──────┘
                pgxpool │       │ S3 SDK
                        ▼       ▼
              ┌────────────────────┐   ┌───────────┐
              │   PostgreSQL 18    │   │  RustFS    │
              │  tag · link ·      │   │  bucket   │
              │  link_tag · folder │   │ screensh. │
              │  click_log ·       │   │  images   │
              │  push_subscription │   └───────────┘
              └────────────────────┘
```

Todos os componentes rodam num `docker-compose`. Backend e web bindam só em `127.0.0.1` por default. O changecheck worker e o push sender são goroutines in-process no mesmo binário do backend (nenhum broker externo).

Defaults sensíveis falham fechados. O Vite de desenvolvimento também binda
`127.0.0.1`; `VITE_DEV_LAN=1` é o opt-in explícito para `0.0.0.0`. `make env`
gera uma vez e persiste no `.env` (0600, sem logar valores) segredos RustFS root
e app independentes; Compose exige ambos e o bootstrap recusa os placeholders
históricos fora de `RUSTFS_ALLOW_INSECURE_DEV_CREDENTIALS=1`. A policy do app
permite somente localização/listagem do bucket e get/put/delete/multipart de
objetos, sem `s3:*`, ACL ou administração de policies. Release é somente
`workflow_dispatch` carregado de `refs/heads/main`; push de tag não publica. Um
job sem credenciais Docker Hub valida target strict semver ou SHA completo,
prova ancestralidade em `origin/main`, exige semver igual aos dois manifests e
recusa tags preexistentes. A tag só é criada depois dos dois manifests serem
publicados pelos jobs aprovados no environment GitHub `release`; publishers fazem
checkout do SHA validado e recebem ali os segredos Docker Hub
e exigir reviewers. Não podem restar cópias desses segredos no nível do
repositório: workflows históricos não declaram o environment e ficam sem
credenciais mesmo se alguém fizer push de uma tag antiga.

## Stack & rationale

| Camada       | Escolha                                                              | Por quê |
|--------------|----------------------------------------------------------------------|---------|
| Runtime API  | **Go 1.26** + Chi v5.3 + pgx/v5.10 + `slog`                         | Minimal router, pgxpool com tipos, log estruturado nativo. |
| DB           | **PostgreSQL 18** + `pg_trgm`                                        | Busca por substring com índice GIN, suficiente single-user. |
| Object store | **RustFS 1.0.0-rc.2** (S3 SDK, imagem presa por digest)                | Upstream ainda não publicou GA; usamos o RC não-preview mais recente sem seguir tags móveis. Backup/screenshots/uploads vivem fora do Postgres; bucket único, prefixos `screenshots/`/`images/`. |
| Migrations   | `golang-migrate` (`000NNN_*.up/down.sql`)                            | Reversível por padrão; mesma convenção compartilhada. |
| Workers      | Goroutine pools in-process (preview, changecheck) + buffered channels | Zero dependência operacional (sem Redis/queue). |
| Web Push     | `github.com/SherClockHolmes/webpush-go v1.4.0` + VAPID auto-gen      | RFC 8030. VAPID key persistida em `/data/vapid.json` (volume `foldex-data`), 0o600. |
| Imagem       | `golang.org/x/image` + stdlib decoders (pure Go, sem CGO)            | Re-encode JPEG q82 + downscale Catmull-Rom + decode-bomb guard 50 MP (`internal/imageopt`). |
| Headless     | `github.com/go-rod/rod v0.116` (Chromium)                            | Screenshot fallback quando o site não tem `og:image`. BrowserContext isolado + proxy de egress estrito por captura. |
| Testes Go    | `testify` (unit) + `testcontainers-go v0.44` (integration, build tag)| Suite real contra Postgres efêmero; gate ≥85% (ver `CLAUDE.md`). |
| SPA          | **Vite 8 + React 19.2 + TypeScript 6 + MUI 9**                        | MUI só pra `createTheme`/`ThemeProvider`; visual vive em `web/src/styles/foldex.css` (CSS handoff). Bundle ~80 kB. |
| Server state | **TanStack Query 5**                                                  | Cache + invalidação por mutation + optimistic updates. |
| i18n         | **react-i18next 17** + i18next 26 (en/pt/es)                          | Locale picker no topbar persiste em `localStorage["foldex.locale"]`. Plurais via `_one`/`_other`. |
| PWA          | **vite-plugin-pwa 1.3** com `strategies: 'injectManifest'`            | SW hand-rolled em `web/src/sw.ts` (Cache API + push/notificationclick listeners). Workbox só injeta `__WB_MANIFEST` no build. |
| Testes web   | **Vitest 4** + `@testing-library/react 16` + jsdom 29                 | Mesmo gate ≥85% (`vitest.config.ts`). |
| Extension    | Vanilla MV3 (sem bundler)                                            | Popup tem ~80 LoC. Sem build = "load unpacked" direto. |
| Node runtime | **bun 1.3** (oven/bun:1.3-alpine)                                    | Bate com Vite 8 / Vitest 4 e resolve melhor packages platform-specific que npm em mirror privado. |

## Data model (estado atual, após 34 migrations)

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
-- 000012_link_folder_preview_index → índice coberto p/ preview de pastas
-- 000013_link_title_lower_index    → índice funcional p/ sort alpha sem runtime sort
-- 000014_notes              → tabela `note` + polimorfiza link_tag/click_log via entity_kind (ADR-27)
-- 000015_folder_password    → `folder.password_hash` nullable, bcrypt (ADR-28)
-- 000016_master_password_and_hint → tabela `app_setting` (KV, master password) + `folder.password_hint` (ADR-29)
-- 000017_multi_user_auth → multi-tenant (ADR-30/31/32). 12 tabelas de identidade
--   (app_user, user_identity, session, session_used_token, invite, password_reset,
--    auth_challenge, email_otp, totp_secret, recovery_code, api_token, oauth_state);
--   `user_id NOT NULL` em link/note/folder/tag/push_subscription; `link.url` e
--   `tag.name` viram UNIQUE (user_id, …) — `link.slug`/`note.slug` continuam
--   GLOBAIS porque /go/{slug} e /n/{slug} resolvem sem sessão; FKs COMPOSTAS
--   (user_id, folder_id) impedem referência cross-tenant no próprio banco; a
--   master password do ADR-29 migra de `app_setting` para `app_user`.
-- 000018_click_log_user_id → dono denormalizado + guard de consistência dos writers
-- 000019_two_factor_indexes → índices dos fluxos 2FA
-- 000020_email_otp_code_hash_index → lookup parcial do token de verificação
-- 000021_credential_coherence → estado OAuth no challenge + trigger de credencial ativa
-- 000022_note_media_ownership → ownership/lease/ref owner-scoped para mídia inline de notes
-- 000023_keyed_2fa_code_digests → MACs keyed/versionados para OTP e recovery
-- 000024_oauth_link_step_up → state de vínculo preso a sessão/epoch/prova recente
-- 000025_auth_challenge_credential_epoch → challenge preso ao token_version; enrollment TOTP também à sessão de Settings
-- 000026_pending_preview_index → índice parcial do recovery sweep de previews pendentes
-- 000027_backup_restore_ledger → checkpoint/mappings owner-scoped para skip restore convergente
-- 000028_password_reset_credential_epoch → reset preso ao token_version vivo; NULL legado falha fechado
-- 000029_slug_length → repara slugs legados >80 bytes e fixa o limite no DB para link/note
-- 000030_preview_generation → geração monotônica impede write de preview anterior após refresh
-- 000031_unique_live_challenge_email_otp → um único OTP de login não consumido por challenge
-- 000032_rbac_four_roles  → owner/admin/editor/viewer + índice parcial de dono único
-- 000033_audit_log        → trilha administrativa (ADR-34)
-- 000034_mail_outbox      → outbox transacional de e-mail, payload cifrado (ADR-36)
-- 000035_user_locale      → idioma preferido da conta; NULL = sem preferência (ADR-36 §12.3)

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

-- 000015_folder_password → NULL = sem proteção (padrão). Não-NULL = hash
-- bcrypt (internal/folders/password.go); plaintext nunca é armazenado. ADR-28.
ALTER TABLE folder ADD COLUMN password_hash TEXT;

-- 000016_master_password_and_hint → dica não-secreta exibida no unlock (≠ senha)
-- + tabela KV genérica p/ settings mutáveis pela UI (hoje só a master). ADR-29.
ALTER TABLE folder ADD COLUMN password_hint TEXT;
CREATE TABLE app_setting (
  key        TEXT PRIMARY KEY,            -- ex.: 'master_password_hash' (bcrypt via internal/pkg/pwhash)
  value      TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE link (
  id             BIGSERIAL PRIMARY KEY,
  url            TEXT NOT NULL UNIQUE,
  slug           TEXT NOT NULL UNIQUE,                   -- CHECK formato + octet_length <= 80 (000009/000029)
  title          TEXT NOT NULL,
  description    TEXT,
  favicon_url    TEXT,
  og_image_url   TEXT,
  preview_status TEXT NOT NULL DEFAULT 'pending'
                 CHECK (preview_status IN ('pending', 'ok', 'failed')),
  preview_error  TEXT,
  preview_generation BIGINT NOT NULL DEFAULT 1 CHECK (preview_generation > 0),
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

-- 000014: note mirrors link's title/slug/folder/pinned shape, swapping the
-- URL-specific fields (no preview pipeline, no favicon) for rich-content
-- ones. Same slug-format CHECK as link so /n/{slug} never shadows /n/{id}.
CREATE TABLE note (
  id         BIGSERIAL PRIMARY KEY,
  title      TEXT NOT NULL,
  slug       TEXT NOT NULL UNIQUE,             -- CHECK same as link.slug, including <=80 bytes
  body_html  TEXT NOT NULL DEFAULT '',         -- sanitized server-side (internal/pkg/htmlsanitize) before every write
  body_text  TEXT NOT NULL DEFAULT '',         -- denormalized plain text, ILIKE/trigram search only
  pinned     BOOLEAN NOT NULL DEFAULT FALSE,
  folder_id  BIGINT REFERENCES folder(id) ON DELETE SET NULL,
  cover_url  TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX note_title_trgm ON note USING gin (title gin_trgm_ops);
CREATE INDEX note_body_trgm  ON note USING gin (body_text gin_trgm_ops);
CREATE INDEX note_pinned_created_idx ON note (pinned DESC, created_at DESC);

-- 000022: a URL pública continua legível sem sessão, mas não concede posse.
-- Uploads novos recebem lease; refs só podem ligar note e mídia do mesmo dono.
-- Nenhuma linha é backfilled a partir de body_html: chaves legadas ficam
-- servíveis e fail-closed para delete/export.
CREATE TABLE note_media (
  object_key       TEXT PRIMARY KEY,
  user_id          BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  lease_expires_at TIMESTAMPTZ,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, object_key)
);
CREATE TABLE note_media_ref (
  user_id    BIGINT NOT NULL,
  note_id    BIGINT NOT NULL,
  object_key TEXT NOT NULL,
  PRIMARY KEY (note_id, object_key),
  FOREIGN KEY (user_id, note_id) REFERENCES note(user_id, id) ON DELETE CASCADE,
  FOREIGN KEY (user_id, object_key) REFERENCES note_media(user_id, object_key) ON DELETE CASCADE
);

-- 000014 polymorphizes link_tag/click_log so notes can share them without
-- duplicating the M:N/event tables — see ADR-27. `link_id` renamed to
-- `entity_id`, `entity_kind` discriminates ('link' | 'note'). The FK to
-- link(id) is dropped (a polymorphic column can't reference two tables);
-- cascade moves to app-level (links.Repository.Delete / notes.Repository.Delete
-- delete their own link_tag/click_log rows in the same tx as the entity row).
CREATE TABLE link_tag (
  entity_kind TEXT   NOT NULL CHECK (entity_kind IN ('link', 'note')),
  entity_id   BIGINT NOT NULL,
  tag_id      BIGINT NOT NULL REFERENCES tag(id) ON DELETE CASCADE,
  PRIMARY KEY (entity_kind, entity_id, tag_id)
);
CREATE INDEX link_tag_tag ON link_tag (tag_id);

-- Single source of truth for click/view events, link AND note alike.
-- `link.click_count`/`note.click_count` are NOT stored — derived at read
-- time via a LATERAL join scoped by entity_kind.
CREATE TABLE click_log (
  id          BIGSERIAL PRIMARY KEY,
  entity_kind TEXT   NOT NULL CHECK (entity_kind IN ('link', 'note')),
  entity_id   BIGINT NOT NULL,
  clicked_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX click_log_clicked_at ON click_log (clicked_at DESC);
CREATE INDEX click_log_entity_ts  ON click_log (entity_kind, entity_id, clicked_at DESC);

-- 000027: skip restore identity/checkpoint, never archive-provided ownership.
CREATE TABLE backup_restore (
  user_id            BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  archive_digest     BYTEA NOT NULL CHECK (octet_length(archive_digest) = 32),
  mode               TEXT NOT NULL CHECK (mode IN ('wipe', 'skip', 'duplicate')),
  inserted           JSONB NOT NULL,
  skipped            JSONB NOT NULL,
  warnings           JSONB NOT NULL,
  file_report        JSONB,
  db_completed_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  files_completed_at TIMESTAMPTZ,
  PRIMARY KEY (user_id, archive_digest, mode)
);
-- backup_restore_entity: (owner/archive/mode, kind, old id) -> fresh/current id.
-- backup_restore_file:   (owner/archive/mode, old note key) -> fresh note key.
-- Both child tables FK-cascade from the composite backup_restore key.
```

Click registration is **append-only** to `click_log`. The /go handler does:

```sql
-- inside a tx
SELECT url FROM link WHERE id = $1;                                  -- 404 check
INSERT INTO click_log (entity_kind, entity_id) VALUES ('link', $1);  -- the only writer
```

`/n/{id-or-slug}` (note render, ADR-27) does the same with `entity_kind='note'` in the same tx as resolving the note row.

Every SELECT that needs `click_count` / `last_clicked_at` derives them via a LATERAL join scoped to the entity's own kind — **every link_tag/click_log query MUST filter `entity_kind`**, since `entity_id` values overlap between link and note id spaces (a link and a note can share the same numeric id):

```sql
SELECT l.id, l.url, l.title, ...,
       COALESCE(cl.cnt, 0) AS click_count,
       cl.last_at          AS last_clicked_at,
       l.pinned, l.created_at
FROM link l
LEFT JOIN LATERAL (
  SELECT count(*) AS cnt, max(clicked_at) AS last_at
  FROM click_log WHERE entity_kind = 'link' AND entity_id = l.id
) cl ON TRUE
ORDER BY l.pinned DESC, COALESCE(cl.cnt, 0) DESC;
```

Listagem com filtro (texto OR substring em title/url; tags como AND quando múltiplas):

```sql
SELECT l.*, COALESCE(array_agg(t.id) FILTER (WHERE t.id IS NOT NULL), '{}') AS tag_ids
FROM link l
LEFT JOIN link_tag lt ON lt.entity_kind = 'link' AND lt.entity_id = l.id
LEFT JOIN tag t       ON t.id = lt.tag_id
WHERE ($1::text IS NULL
       OR l.title ILIKE '%'||$1||'%'
       OR l.url   ILIKE '%'||$1||'%')
  AND ($2::bigint[] IS NULL
       OR l.id IN (
         SELECT entity_id FROM link_tag
         WHERE entity_kind = 'link' AND tag_id = ANY($2)
         GROUP BY entity_id
         HAVING count(DISTINCT tag_id) = array_length($2,1)
       ))
GROUP BY l.id
ORDER BY l.created_at DESC
LIMIT $3 OFFSET $4;
```

`internal/entries` runs the link/note equivalents of the query above as the two arms of a `UNION ALL` (wrapped in a derived table so `ORDER BY lower(title)` is legal post-union — Postgres forbids expressions directly under a set operation's `ORDER BY`), giving the frontend one paginated, sorted, searched endpoint instead of merging two independently-paginated queries client-side. `GET /api/entries/counts` is the owner-scoped authoritative cardinality and counts link and note rows independently, without summing overlapping tag/folder collections. See ADR-27.

## API surface

| Grupo  | Método | Path                                  | Propósito                                          |
|--------|--------|---------------------------------------|----------------------------------------------------|
| Entries| GET    | `/api/entries`                        | Projeção read-only e owner-scoped de links+notes via `UNION ALL`; mesmos filtros, sort e paginação do grid. |
|        | GET    | `/api/entries/counts`                 | Totais owner-scoped `{links, notes}`; cardinalidade distinta por tipo. |
|        | GET    | `/api/entries/preview-status?id=…`    | Status compacto de até 100 links; aceita `id` repetível e `folder_id`, com o mesmo content gate de pasta. |
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
|        | DELETE | `/api/links/{id}/image`               | Remove `og_image_url` + DELETE no objeto RustFS + zera `preview_status`. |
| Files  | GET    | `/api/files/notes/{uuid}.{ext}`       | Leitura pública exigida por `/n/{slug}`, montada fora da sessão. Aceita só UUID canônico + extensão raster suportada; traversal, chave malformada e prefixo de link retornam 404. |
|        | GET    | `/api/files/*`                        | Proxy autenticado pro RustFS. Key precisa cair em `screenshots/`/`images/` e provar ownership do link (rejeita `..` e prefixo arbitrário). `DetectContentType` no objeto servido + `X-Content-Type-Options: nosniff`. |
| Tags   | GET    | `/api/tags`                           | List with `link_count`                             |
|        | POST   | `/api/tags`                           | Body: `{name, color?, icon?}`                      |
|        | PATCH  | `/api/tags/{id}`                      |                                                    |
|        | DELETE | `/api/tags/{id}`                      | Cascades junction                                  |
| Folders| GET    | `/api/folders`                        | Query: `?root=1` (só pastas raiz, `parent_id IS NULL`), `?parent_id=N` (filhas diretas de N), ausente (flat, todas). Retorna `link_count` + `has_password` + `preview_links` (até 4, LATERAL+jsonb_agg, ordem pinned DESC, created DESC — **sempre vazio quando `has_password=true`**, redação incondicional, ADR-28) + `parent_id`. `?parent_id=N` numa pasta protegida exige `X-Foldex-Folder-Unlock` válido, senão `403 folder_locked` (ADR-28). |
|        | POST   | `/api/folders`                        | Body: `{name, color?, parent_id?, password?, password_hint?}` — `parent_id` opcional (null = raiz); `password` opcional (min 4 chars), define proteção já na criação; `password_hint` opcional (≠ senha, ADR-29). |
|        | GET    | `/api/folders/{id}`                   | Retorna `password_hint` (não-secreto) quando presente (ADR-29). |
|        | PATCH  | `/api/folders/{id}`                   | `parent_id`/`password`/`password_hint` são tri-state (absent=não toca, valor=define, null=remove). Trocar/remover uma senha JÁ existente exige `current_password` (ADR-28). `password_hint` ≠ senha (checado via bcrypt na tx); remover a senha limpa a dica (ADR-29). |
|        | POST   | `/api/folders/{id}/unlock`            | Body: `{password}`. Verifica via bcrypt; `200 {unlock_token, expires_at}` (TTL 24h). Token inclui o `password_hash` atual no HMAC — trocar/remover a senha invalida todo token emitido antes. `400 not_protected`; `401 wrong_password` traz `failed_attempts`/`attempts_remaining`; após 5 erros seguidos → `429 too_many_attempts` com `locked_until`/`retry_after_seconds` + header `Retry-After` (bloqueio de 1h por pasta, em memória; acerto zera o contador). Detalhe: ADR-28. |
|        | POST   | `/api/folders/{id}/reset-password`    | Body: `{master_password}`. Recuperação (ADR-29): verifica a master e **limpa** senha+dica da pasta (não abre a pasta, não emite token). `400 master_not_configured` / `401 wrong_master_password`. |
|        | DELETE | `/api/folders/{id}`                   | Default: SET NULL em links E em subpastas (filhas viram raiz). Pasta raiz protegida exige `X-Foldex-Folder-Unlock`. Com `?cascade=1`: materializa e trava a subtree owner-scoped na tx; recusa atomicamente com `409 descendant_protected` + `count` se qualquer descendente (exceto a raiz já autorizada) tiver senha; caso contrário apaga toda a subtree + links/notas. API token é recusado. |
| Settings| GET   | `/api/settings/master-password`       | `{configured: bool}` — nunca retorna o hash (ADR-29). |
|        | PUT    | `/api/settings/master-password`       | Body: `{password (min 8), current_password?}`. Primeiro-set não exige atual; trocar exige `current_password` (`401 wrong_password`). |
|        | DELETE | `/api/settings/master-password`       | Body: `{current_password}`. Remove a master (`401 wrong_password` se errada; idempotente se não configurada). |
| Stats  | GET    | `/api/stats/dashboard?days=N&limit=N` | Uma query owner-scoped retorna `{summary, daily, top, tags}` sobre o mesmo snapshot materializado de links/cliques. `days`: default 60, clamp 1..365; `limit`: default 5, clamp 1..100. Storage permanece separado. |
|        | GET    | `/api/stats/summary`                  | Rota legada retida: totals de links, tags, cliques 30d/prev30d, novos 30d e top host. |
|        | GET    | `/api/stats/daily?days=N`             | Rota legada retida; `days` default 60/clamp 1..365; array zero-filled via `generate_series`. |
|        | GET    | `/api/stats/top?limit=N`              | Rota legada retida; `limit` default 10/clamp 1..100; top links lifetime + janelas 30d/prev. |
|        | GET    | `/api/stats/tags`                     | Rota legada retida; por tag, soma de cliques + nº de links. |
| I/O    | POST   | `/api/import`                         | Multipart `file` + `format=netscape\|json` (JSON restaura cliques via click_log) |
|        | POST   | `/api/import/validate`                | Preflight multipart agregado, sem itens: `{format, counts, conflicts, folders:[{path,name,count,conflicts}], ungrouped:{links,conflicts}, warnings}`. Conflitos de URL/tag são owner-scoped. |
|        | POST   | `/api/import/apply`                   | Aplica multipart com `mode=skip\|wipe\|duplicate` e `exclude_folders` opcional. |
|        | GET    | `/api/export?format=netscape\|json`   | Download (click_count derivado em subquery)        |
| Backup | POST   | `/api/backup`                         | Stream ZIP completo (DB + RustFS). `Content-Type: application/zip`. Disponível só quando RustFS está acessível. Ver [SDD-BACKUP-RESTORE.md](./SDD-BACKUP-RESTORE.md). |
|        | POST   | `/api/backup/download`                | Emite ticket opaco one-time (TTL 60 s), owner/session-bound, para download nativo sem Blob; exige sessão + CSRF e recusa API token. |
|        | GET    | `/api/backup/download?id=…&token=…`   | Consome ticket uma vez e streama o mesmo export com `Content-Disposition`; continua session-authenticated e usa o slot compartilhado. |
|        | GET    | `/api/backup/download/status?id=…`    | Estado owner-bound (`pending|running|complete|failed`) para histórico com counts, bytes e duração, sem ler o ZIP no JS; sobrevive ao refresh da sessão. |
|        | POST   | `/api/backup/validate`                | Multipart `file=<zip>` → `{ok, manifest, conflicts, warnings, errors}` sem aplicar |
|        | POST   | `/api/backup/restore?mode=…`          | Multipart `file=<zip>` + `mode=wipe\|skip\|duplicate` (default `skip`) → `{inserted, skipped, wiped, files, duration_ms}` |
| Stats  | GET    | `/api/stats/storage`                  | `{objects, total_bytes}` do bucket RustFS; registrado só quando o storage está disponível |
| Push   | GET    | `/api/push/vapid-key`                 | Retorna a chave pública VAPID (base64url) — front usa em `PushManager.subscribe({applicationServerKey})`. Exige sessão. |
|        | POST   | `/api/push/subscriptions`             | Upsert por `endpoint` (UNIQUE) com p256dh/auth atualizados. Cap transacional de 16 por usuário; renovar endpoint próprio continua permitido no teto, novo row retorna `409 subscription_limit_reached`. |
|        | DELETE | `/api/push/subscriptions`             | Remove a subscription pelo endpoint (chamado no unsubscribe do usuário). |
|        | POST   | `/api/push/test`                      | Dispara notificação de teste pra todas as subscriptions ativas. Útil pra validar VAPID/SW. |
| Redir  | GET    | `/go/{id-or-slug}`                    | 302 + INSERT no click_log (fora de `/api`). ID-first; fallback pra slug (mig 000009). |
| Health | GET    | `/healthz`                            | `{status, db}` + 200/503                           |

Erros em JSON uniforme: `{ "error": { "code": "not_found", "message": "..." } }`.

### Fronteira de erros

Persistência não conhece HTTP. Todo `repository*.go`, inclusive os arquivos
`repository_system.go`, devolve erros semânticos de `internal/pkg/domainerr` ou
sentinels do próprio pacote (`links.ErrURLTaken`, `notes.ErrStaleWrite`,
`folders.ErrLocked`, etc.). Helpers usados por repositórios, como o lifecycle de
slug, resolução de target público, tags pendentes e content gate de pastas,
seguem a mesma regra para não reintroduzir `httperr` de forma transitiva.

Somente handlers traduzem esses erros para status/code/message. As matrizes em
`handler_error_test.go` travam os envelopes existentes, incluindo 404
cross-tenant, conflitos de URL/slug/tag/CAS, senha de pasta e revogação de
invite/session. `internal/security.TestRepositoriesDoNotImportHTTPDelivery`
percorre a AST e falha se qualquer `repository*.go` de produção importar
`net/http` ou `internal/pkg/httperr`.

Delivery também depende de capabilities, não de adapters concretos:
`links.ScreenshotHandler` consome `ports.Uploader` e o sentinel
`ports.ErrObjectTooLarge`; `storage.Client` é construído e ligado somente em
`cmd/server`. `internal/security.TestDeliveryDoesNotImportStorageAdapters`
varre imports de produção em `internal/links`, sem proibir a composition root.

## Preview worker

- Implementação em `internal/preview/`:
  - `worker.go`: pool de N goroutines (`PREVIEW_WORKER_CONCURRENCY`, default 4, teto 8), consome de `chan PreviewJob`.
  - `fetcher.go`: `http.Client{Timeout: 5s}`, parse HTML head com `golang.org/x/net/html` — extrai `<title>`, `meta[og:title|og:image|og:description]`, `link[rel~=icon]`.
  - `public.go`: `IsPublicURL(ctx, url)` — gate do fallback de screenshot (resolve o host e rejeita metadata/RFC6598 e todos os ranges special-purpose da IANA).
  - `enqueue.go`: `Enqueue(linkID int64)` chamado por `links.Create` e por `POST /links/:id/refresh-preview`.
- Side effects: `UPDATE link SET preview_status, favicon_url, og_image_url, description, preview_error, updated_at` após o fetch.
- **Geração monotônica (migration 000030).** `link.preview_generation` nasce em 1 e todo restart para `pending` (inclusive `refresh-preview`) incrementa a geração antes do enqueue. O worker lê `updated_at` + geração na projeção estreita; metadata, falha, screenshot e convergência final só escrevem sob CAS de ambos, `preview_status='pending'` e, quando aplicável, imagem vazia. Uma geração antiga perde mesmo se os timestamps coincidirem; perda de CAS interrompe o restante e remove o objeto recém-criado.
- SSRF guard: ranges de metadata/credenciais cloud (EC2 IMDS, ECS, EKS Pod Identity, Alibaba e Tencent, inclusive IPv6 quando aplicável) e RFC6598 (`100.64.0.0/10`) são sempre bloqueados (sem opt-out) pela policy compartilhada em `internal/pkg/netpolicy`. O fetch HTML só bloqueia os demais ranges special-purpose da IANA, como loopback, RFC1918, benchmarking/documentação/reservado, link-local e IPv6 ULA, quando `PREVIEW_STRICT_SSRF=1`. Default = permissivo para intranets reais (Jira/Grid/Confluence), sem tratar CGNAT como internet pública.
- Screenshot Chromium tem postura deliberadamente mais estrita que o fetch HTML: o processo é pooled, mas cada `Capture` cria um BrowserContext incognito e um proxy HTTP local exclusivos. Cada proxy limita a 32 os túneis CONNECT ativos. Um segundo proxy estrito fica no launcher da geração, pois o Chromium decide o bypass implícito de localhost no proxy global; qualquer escape do proxy do contexto cai nessa camada. Ambos usam `ProxyBypassList="<-loopback>"`, bloqueiam os registries special-purpose IPv4/IPv6 da IANA antes do dial e validam o peer TCP depois, independentemente de `PREVIEW_STRICT_SSRF`. Capturas são serializadas porque o sinal de bloqueio desse proxy global pertence à geração inteira e não pode ser atribuído com segurança entre contextos concorrentes. Chromium roda como usuário não-root com sandbox habilitado; `disable-quic`, `disable-webrtc-multiple-routes` e `force-webrtc-ip-handling-policy=disable_non_proxied_udp` fecham egress UDP direto. Qualquer request bloqueado invalida a captura inteira. Worker e endpoint compartilham uma instância explícita de `screenshot.Pool`, sem estado global de lifecycle. Fila (5 s), startup/connect e execução da captura têm budgets limitados independentes sob envelope caller de 70 s; startup ocorre fora do mutex do pool. Retirement destaca teardown rastreado da latência da captura. Shutdown recusa trabalho novo, cancela launch e capturas em andamento, aguarda 7 s e força kill/cleanup de gerações ainda abertas antes de devolver controle ao deadline global de 12 s. O pool retém launcher/PID; toda falha de launch remove o profile, e falha em connect, create/dispose de contexto ou close limitado força `Kill` + cleanup context-aware. A remoção do profile usa raiz confinada, não segue symlinks e percorre entradas em lotes limitados, checando cancelamento entre lotes.
- Resource budgets complementam a policy: no máximo 32 conexões/CONNECT e 32 MiB agregados por proxy; CDP limita cada captura a 256 requests reais, inclusive streams HTTPS multiplexados dentro de CONNECT. Qualquer excesso invalida a captura. DNS recebe 5 s e storage pós-captura recebe 10 s próprios. A admissão manual aceita duas capturas globais e uma por usuário. A fila deduplica IDs, coalesce refresh explícito durante execução em um rerun e ignora IDs já agendados no sweep de recovery. O sweep usa uma projeção estreita sem aggregate de cliques e migration 000026 adiciona índice parcial dos pendentes. Shutdown HTTP/worker/Chromium ocorre em paralelo sob deadline global de 12 s; o pool usa 7 s de grace e força kill/cleanup antes de retornar. Erros persistidos/logados usam classificações estáveis, sem URL ou texto bruto de fetch/Chromium.
- **Short-circuit por upload manual** (`Worker.process` no topo): se `link.og_image_url` já tem valor quando o job vira processado, o worker **pula tudo** (sem fetch HTML, sem screenshot) e só flipa `preview_status` de `pending`→`ok` se ainda estiver `pending`. É a peça que garante: upload feito enquanto o job estava na fila não dispara trabalho extra, e a label "capturando…" some.
- **Upload manual mexe em 3 colunas no mesmo swap.** `repository.ReplaceOGImage` trava a linha, devolve a URL anterior exata e seta `og_image_url`, `preview_status='ok'` e `preview_error=NULL` atomicamente. O screenshot fallback e sua convergência final exigem CAS por `updated_at` + status pending + imagem vazia; upload manual ou refresh mais novo vence, e objeto fallback superseded é removido.
- **Screenshot fallback** (`Worker.maybeScreenshot`): após o fetch HTML, **se** `og:image` veio vazio **e** o link ainda não tem `og_image_url` (não foi feito upload manual) **e** o host resolve pra IP público (via `IsPublicURL`) **e** o worker foi inicializado com `WithScreenshotFallback(sc, up)`, então:
  1. `sc.Capture(ctx, url)` — Chromium headless via `internal/screenshot/` (`go-rod`), viewport 1280×800, BrowserContext + proxy estrito exclusivos da captura
  2. `imageopt.Optimize(png, …)` — downscale ≤ 1024 px + re-encode JPEG q≈82 (ver "Pipeline de imagens" abaixo)
  3. `up.Upload(ctx, "screenshots/{id}.{uuid}.jpg", jpg, "image/jpeg")` — chave exclusiva da operação
  4. CAS publica exatamente essa URL se `updated_at`, status e imagem continuam elegíveis
  5. Só após o CAS, remove variantes determinísticas legadas e o objeto local anteriormente referenciado
  
  O fallback é **silencioso em falha** (apenas loga) — o link permanece sem imagem. Falhas comuns: site bloqueando bots, JS-heavy page sem og:image, Chromium ausente. Se `imageopt.Optimize` retornar erro (corrupção rara), o worker armazena o PNG cru numa chave versionada; perda do CAS apaga somente essa chave da própria operação.

## Change-check worker (`internal/changecheck`)

Detecção periódica per-link de mudança de conteúdo. Opt-in via `link.check_interval ∈ {hourly, daily, weekly}` (default NULL = desativado). Disparo de Web Push quando o fingerprint muda.

- **Worker** (`worker.go`): pool de N goroutines (`CHANGECHECK_WORKER_CONCURRENCY`, default 2, teto 8) + scanner que roda a cada `CHANGECHECK_SCAN_INTERVAL_SEC` (default 60s). `atomic.Bool stopped`, `sync.Once Stop`, channel buffered (256). `Enqueue` retorna `ErrQueueFull`/`ErrStopped`.
- **Scanner** (`scan`): o mesmo `UPDATE ... FOR UPDATE SKIP LOCKED` que reserva até 256 links devolve a projeção estreita usada pelo fetch (`id`, owner, URL, título, intervalo, fingerprint e o `last_checked_at` do claim). Não existe `SystemGet` por item nem aggregate de `click_log`. O `CASE WHEN check_interval='hourly' THEN '1 hour' ...` resolve "due" sem hardcode no Go. O índice `link_check_due_idx` é parcial — varre só os opt-in, O(opt-in) não O(total).
- **Janela rolante, NÃO horário fixo.** O agendamento NÃO é cron-style — não roda "à meia-noite" nem "às 3am". Um link `daily` rodado pela primeira vez às 14:37 do dia 1 fica due novamente em ~14:37 do dia 2 (com drift de até `CHANGECHECK_SCAN_INTERVAL_SEC` + tempo do fetch HTTP). A predicação é `last_checked_at < now() - interval` e o tie-break é `ORDER BY COALESCE(last_checked_at,'epoch') ASC, id ASC` — links opt-in pela primeira vez (`last_checked_at IS NULL`) entram no scan imediatamente. Sem timezone awareness: usa `now()` do Postgres (UTC no container). Sem jitter — 100 links marcados juntos rodam juntos em batches de até 256 por tick. Catch-up automático no boot do backend: tudo que ficou vencido durante o downtime é processado em ordem pelo `last_checked_at` mais antigo.
- **Fingerprinter** (`fingerprint.go`): híbrido. Primeiro extrai `<link rel="alternate" type="application/(rss|atom)+xml">` e hashea os IDs/GUIDs ordenados. Se não tem feed, fallback content hash em `<main>`/`<article>` (whitespace-normalized; remove `<script>`/`<style>`/`<nav>`/`<header>`/`<footer>`).
- **Prefixo `feed:`/`content:` no hash armazenado é discriminador** — quando uma página content-only ganha um feed novo, a troca `content:` → `feed:` é tratada como "novo baseline", **não** como mudança. Sem o prefixo o primeiro scan pós-feed dispararia push falso.
- **First observation nunca conta como change.** `last_fingerprint IS NULL` → grava o novo hash sem bumpar `last_change_detected_at`. Sem isso, todo opt-in viraria push no primeiro scan.
- **Reusa o `preview.Fetcher`** via interface `HTTPGetter` (`GetRaw`) — o mesmo `safeDialer` com pre-dial LookupIP + post-dial RemoteAddr. Forkar um HTTP client aqui dividiria a postura SSRF.
- **Resultado e push respeitam a configuração reclamada.** `SystemRecordCheckResult` faz CAS pelo `last_checked_at` exato do claim e devolve `applied=false` se URL/intervalo mudou, houve opt-out ou outro claim venceu; só um resultado aplicado entra na fila de push. Alterar de fato URL/intervalo limpa o baseline e torna o link due imediatamente; campos iguais presentes no payload não limpam estado. Push roda em workers fixos, numa fila de 32 itens que descarta a notificação mais nova quando cheia; cada envio herda o contexto do worker + timeout de 15s, então `Stop` cancela e junta somente um número fixo de goroutines. Falha de push nunca rolla back a detecção durável.
- **Erros isolados em `last_check_error`** — não polui `preview_error` (worker diferente, surface diferente no LinkCard).

## Web Push (`internal/push`)

Notificação background quando o changecheck detecta change. RFC 8030 + VAPID via `github.com/SherClockHolmes/webpush-go`.

- **VAPID** (`vapid.go`): `LoadOrGenerate` prioriza env (`VAPID_PUBLIC_KEY`/`VAPID_PRIVATE_KEY`/`VAPID_SUBJECT`) → state file (`VAPID_STATE_PATH`, default `/data/vapid.json`) → autogen + persiste com `os.WriteFile(..., 0o600)` (umask não confiável). Volume `foldex-data:/data` no compose preserva entre recreations; pinar em `.env` mantém subscriptions estáveis.
- **Subscription repo** (`subscription.go`): `INSERT … ON CONFLICT (endpoint) DO UPDATE SET p256dh, auth, user_id` — renovação do browser converge no mesmo row. Um lock `FOR NO KEY UPDATE` no owner serializa o cap de 16 subscriptions; upsert de endpoint já pertencente ao caller não consome slot. `List` é owner-scoped e usa o mesmo teto como `LIMIT` defensivo.
- **Sender** (`sender.go`): fan-out limitado a 16 alvos e 4 envios concorrentes por notificação. Resultados 2xx e 404/410 são coletados por id e persistidos em no máximo um `MarkUsed` e um `DeleteGone`, ambos owner-scoped com `ANY(bigint[])`; transport/other-status errors ficam isolados por subscription id e não apagam subscription. Endpoint é capability secreta e nunca entra no log. O client usa transporte sempre public-only, sem depender de `PREVIEW_STRICT_SSRF`: valida IP antes/depois do dial contra ranges privados/special-use e recusa redirects, impedindo endpoint ou redirect controlado de alcançar serviços internos.
- **Handler** (`handler.go`): rotas montadas só quando `PushHandler != nil`. Tudo fica atrás da pilha de sessão (inclusive `vapid-key` — não vaza superfície). `/push/test` tem admissão global fail-fast de duas fan-outs simultâneas; excesso recebe `429 push_busy` em vez de criar mais workers/requests.
- **Service Worker hand-rolled** (`web/src/sw.ts`): Cache API + `push` listener + `notificationclick` listener. `vite-plugin-pwa` com `strategies: 'injectManifest'` injeta `__WB_MANIFEST` no build sem trazer runtime workbox-* (que exigiria regenerar `bun.lock`).

## Pipeline de imagens (`internal/imageopt`)

Todo byte que entra no RustFS via upload do usuário ou via screenshot fallback passa por `imageopt.Optimize(data, Options{MaxDim: 1024, Quality: 82})` antes do `Upload`. Implementação 100% Go (`image/png|jpeg|gif` da stdlib + `golang.org/x/image/draw` pra resize Catmull-Rom + `golang.org/x/image/webp` só pra decode); sem CGO, sem libwebp no Dockerfile.

**Algoritmo:**

1. `http.DetectContentType` sniff dos bytes; rejeita qualquer MIME fora de `{image/png, image/jpeg, image/gif, image/webp}` → `ErrUnsupportedFormat`.
2. `image.Decode` decodifica usando o decoder registrado pelo MIME sniffed. Falha → `ErrDecode`.
3. Se algum lado > 1024 px, calcula `(W', H')` preservando aspect ratio.
4. Cria `*image.RGBA` no tamanho final preenchido com branco, depois `draw.CatmullRom.Scale(…, draw.Over)` pra blendar a fonte. Isso resolve resize + composição de alpha sobre branco numa só operação (JPEG não tem alpha — sem o branco, pixels transparentes virariam pretos).
5. `jpeg.Encode` com `Quality: 82`.
6. **Guard de não-regressão (só pra JPEG de entrada):** se a entrada já era JPEG, não foi feito resize, e o output ficou ≥ ao input, devolve os bytes originais. Pra PNG/GIF/WebP, sempre re-encoda; a extensão da chave operation-owned continua refletindo o conteúdo armazenado.

**Pontos de chamada:**

- `internal/links/screenshot_handler.go:UploadImage` — uploads manuais (`POST /api/links/{id}/image`). Cap de body = 5 MiB (`MaxBytesReader`), pixel cap = 50 MP via `imageopt.DecodeConfig`; grava chave operation-owned e faz swap atômico que devolve a URL superseded exata. Assim upload manual continua vencendo o worker e cleanup concorrente remove apenas bytes que já deixou de publicar.
- `internal/links/screenshot_handler.go:CaptureAndStore` — screenshot sob demanda (`POST /api/links/{id}/screenshot`). **Gate SSRF obrigatório**: chama `links.URLPolicy` (passada por `main.go` como `preview.IsPublicURL`) antes de invocar o Chromium — rejeita scheme não-http(s) com 400 `invalid_scheme` e qualquer target special-purpose com 400 `private_target`. Policy nil = fail-closed (deny). O proxy estrito dentro de `internal/screenshot` repete a decisão em cada redirect/subresource/CONNECT e valida o peer pós-dial; o gate do handler continua necessário para rejeição barata e erro 400 estável. Sem essas camadas o endpoint vira read-anywhere (`file:///etc/passwd` → screenshot → `/api/files/`).
- `internal/preview/worker.go:maybeScreenshot` — screenshot fallback do worker (mesma `IsPublicURL`).

Após publicar a URL nova, cada fluxo remove a imagem local anteriormente referenciada e as variantes determinísticas legadas do mesmo id. `DeleteObject` é idempotente (NoSuchKey = sucesso); perda do CAS remove só a chave operation-owned recém-criada. **Arquivos antigos pré-deploy não referenciados ficam intocados** — o `ProxyFile` continua servindo `.png/.gif/.webp` históricos sem mudança.

### imageopt — decode-bomb guard

`imageopt.Optimize` chama `image.DecodeConfig` antes de `image.Decode` e rejeita com `ErrTooLarge` qualquer payload cujas dimensões declaradas excedam `maxPixels = 50_000_000` (50 MP). Sem isso, um PNG de ~30 KB declarando 60000×60000 alocaria ~14 GB de RGBA em `image.NewRGBA` e travaria o backend. O cap é generoso para qualquer foto de celular (top consumer é ~108 MP, mas esses comprimem para >5 MB e o upload pré-cap de 5 MiB já corta antes).

## Portas, hostnames e deploy local

- **Backend:** `127.0.0.1:9089` no host, `9089` no container. Lê `BACKEND_PORT` do env.
- **Web (nginx servindo bundle Vite):** `127.0.0.1:9088 → nginx:8080` no container (HTTP) / `127.0.0.1:9444 → nginx:8443` (HTTPS). Roda como user `nginx` non-root — ports internos >1024 por isso. Proxa `/api` e `/go` pro `backend:9089` na rede `foldex`.
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
CORS_ORIGINS=*
BACKEND_BIND=127.0.0.1      # bind do backend; non-loopback + AUTH_ENABLED=0 recusa boot

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

Parser usa `golang.org/x/net/html` e percorre `<A>` + stack de `<H3>`. **Semântica atual (pós-folders):** o `<H3>` mais profundo no escopo de cada link vira `folder` (pasta), e os `<H3>` ancestrais viram `tags`. Ex: `Bookmarks Bar → Work → Issues → linkA` resulta em `linkA.folder = "Issues"` + `linkA.tags = ["Bookmarks Bar", "Work"]`. Foldex folders são flat (1 nível) — o aninhamento é colapsado pro mais profundo. A aplicação inteira usa uma transação e tabelas temporárias alimentadas por `CopyFrom`: URLs são idempotentes por `(user_id, url)`, folders fazem match-or-create por nome do dono, e links/tags/clicks são gravados em operações set-based. Slugs globais relevantes são pré-carregados por uma consulta limitada e alocados em memória antes do batch.

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

**Versionamento**: importer aceita v1 (pré-folders, sem `folders[]`/`folder`) e v2 (com pastas). Exporter sempre escreve v2. Round-trip idempotente: re-importar um JSON exportado não duplica folders (match por nome dentro do dono). `click_count` aceita no máximo 10.000 por link e 1.000.000 cumulativos por import; cliques sintéticos de links novos são inseridos por um único `generate_series` set-based.

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

**Como funciona:** `link.slug TEXT NOT NULL UNIQUE` (migration 000009) com CHECK `^[a-z0-9]+(-[a-z0-9]+)*$ AND NOT ^[0-9]+$`. Slug é auto-derivado do título no create via `Slugify` (lowercase ASCII, accent-fold, hyphen-collapse, max 80 bytes na hyphen-boundary); usuário pode override no `LinkDialog`. A migration 000029 repara colisões legadas acima desse limite e adiciona `octet_length(slug) <= 80` a `link` e `note`.

**Resolução `/go/{valor}`:** ID-first (preserva backward-compat de todo `/go/42` antigo), depois slug-fallback. A constraint que rejeita slug puramente numérico garante que nunca há ambiguidade — `/go/42` SEMPRE significa link 42.

**Backup/import/export:** snapshot inclui slug; restore com `mode=skip|wipe|duplicate` e importer resolvem colisões com sufixo `-2`, `-3`, … reservando o espaço do sufixo dentro dos 80 bytes. O preload inclui as bases truncadas de cada largura de sufixo, sem consulta por candidato.

### ADR-8 — SSRF guard no preview fetcher
Fetcher visita URLs arbitrárias fornecidas pelo usuário. **Ranges de metadata/credenciais cloud e RFC6598 são sempre bloqueados**, sem opt-out. Os demais ranges special-purpose da IANA só são bloqueados quando `PREVIEW_STRICT_SSRF=1`. O default permissivo para RFC1918 é uma compatibilidade explícita com links de intranet (Jira/Grid/Confluence/dashboards internos), não uma suposição de usuário único; em modo multi-tenant o operador deve habilitar strict quando usuários não devem alcançar serviços internos do host.

### ADR-9 — Click_log como única fonte de verdade
Migration 000006 dropou `link.click_count` e `link.last_clicked_at`. Cliques agora vivem só em `click_log`. Contagens e timestamps são derivados via `LEFT JOIN LATERAL` no SELECT. **Por quê:** durante o desenvolvimento, percebemos que mantinhamos dois lugares pra contar (UPDATE atômico no link + INSERT no click_log) e qualquer divergência seria irrecuperável (qual é a verdade?). Single source of truth elimina o problema. **Trade-off:** O(log N) lookup por link na listagem (mitigado pelo índice `click_log_link_id_ts`). Pra single-user com até 10k links, é irrelevante. Se virar gargalo no futuro: materialized view com REFRESH no /go handler.

### ADR-10 — Pin é coluna na `link`, não tabela
`link.pinned BOOLEAN` (migration 000005) + índice `link_pinned_created (pinned DESC, created_at DESC)`. Optei por coluna em vez de tabela separada `pinned_links` porque (a) é 1:1 com link (b) toggle é uma operação simples (c) ORDER BY pinned DESC é trivial. Hipotético upgrade futuro pra "pinado por contexto/lista": só virar uma tabela `link_pin (link_id, list_id)`.

### ADR-11 — Postgres host configurável (`db` / `localhost` / `host.docker.internal`)
`docker-compose.yml` deriva `DB_URL` de `POSTGRES_HOST` (e `POSTGRES_SSLMODE`). O backend container declara `extra_hosts: ["localhost:host-gateway", "host.docker.internal:host-gateway"]` pra que ambos os nomes resolvam pro host real, não pra ele mesmo. **Por quê:** o usuário pode ter um Postgres já rodando no host e querer reusar; também serve quando se troca pra RDS/Neon (basta setar `POSTGRES_HOST=hostname-real`). Foi importante MANTER `POSTGRES_HOST=db` como default no `.env.example` pra quem usa o `docker-compose.db.yml`.

### ADR-12 — SSRF guard permissivo por default
Metadata/credenciais cloud (EC2 IMDS, ECS e EKS Pod Identity, inclusive IPv6) e RFC6598 são sempre bloqueados. Os demais ranges special-purpose da IANA só são bloqueados quando `PREVIEW_STRICT_SSRF=1`. **Por quê:** intranet (Jira, Grid, Confluence, dashboards internos) continua um caso de uso primário, inclusive em instalações autenticadas; o default preserva esses destinos RFC1918. Em ambientes multi-tenant onde usuários não devem alcançar a rede interna do host, o operador habilita strict.

### ADR-13 — Confirm modal próprio (não window.confirm) + Esc fecha tudo
`useConfirm({ title, message, destructive })` retorna uma `Promise<boolean>`. Substitui qualquer `window.confirm()`. Tipografia coerente com o resto (Space Grotesk + Nunito Sans), botões com gradient indigo/vermelho, kicker mono. **Por quê:** `confirm()` quebra o tom visual e o teclado fica preso ao chrome do browser. Esc em qualquer modal cai por hook `useEscape(onClose, open)`.

### ADR-14 — Gerenciamento de tags via modal próprio (não inline na sidebar)
Sidebar mostra só lista enxuta (dot + nome + count). Edit/delete moveu pra `TagManagerDialog` aberto pelo botão "Gerenciar tags" no rodapé da sidebar. **Por quê:** botões inline por linha brigavam com o layout `grid-template-columns: 16px 1fr auto` e quebraram em N+1 linhas em vez de uma. Tag management é ação eventual, não navegação — modal próprio é o lugar certo.

### ADR-15 — Coverage gate de 85%
Definido em `CLAUDE.md`. Backend: `make coverage` executa unit + integration tests em `./...`, incluindo os testes de `cmd/server` e `cmd/rustfs-bootstrap`; `-coverpkg` continua medindo somente produção em `internal/...`, excluindo `internal/db`, `internal/testdb` e `authctxtest`, portanto boot/helpers não deflacionam o gate de 85%. No CI, `coverage-run` gera o profile uma vez e `coverage-check` aplica o mesmo gate sem repetir a suíte. O runner Ubuntu precisa fornecer `google-chrome` executável e exporta seu caminho em `CHROME_PATH` antes da suíte, então os testes `LiveChrome` não podem virar skip silencioso. Frontend: `vitest.config.ts` define `thresholds.lines/statements/functions: 85, branches: 80`, aplicados pelo único `bun run coverage`; o mesmo job executa uma vez o script `test` da extensão. Todos os testes e thresholds bloqueiam o PR.

Em uma máquina sem Chrome/Chromium local, o equivalente isolado para os testes live usa Chromium como usuário não-root dentro de container (rode da raiz do repositório):

```bash
docker run --rm -v "$PWD/backend:/src" -w /src golang:1.26-alpine sh -ec '
  apk add --no-cache chromium su-exec
  adduser -D -h /tmp/foldex-test foldex-test
  chown -R foldex-test:foldex-test /go /tmp/foldex-test
  exec su-exec foldex-test env HOME=/tmp/foldex-test \
    CHROME_PATH=/usr/bin/chromium-browser CGO_ENABLED=0 \
    go test -count=1 ./internal/screenshot -run LiveChrome
'
```

### ADR-10 — Versões "always-latest-stable"
Antes de pinar uma dep nova, conferir `https://go.dev/dl/` e `npm view <pkg> version --registry=https://registry.npmjs.org/` (sempre o registro público, nunca um mirror privado, pra checagem de versão). Tabela de versões correntes vive em `CLAUDE.md` §1.

### ADR-16 — Screenshot só como fallback (nunca obrigatório)
A captura de tela headless (`internal/screenshot/` via `go-rod`) **só roda** quando o fetch HTML não devolveu `og:image`, o usuário ainda não fez upload manual, **e** o host resolve pra IP público (`preview.IsPublicURL`). Os três gates são curto-circuito — qualquer falha desliga o screenshot e o link fica sem imagem (em vez de mostrar uma tela de login interna ou consumir Chromium em vão). O processo Chromium é pooled, mas estado de navegação não é: cada captura usa BrowserContext incognito descartável e proxy de egress estrito próprio, sem bypass de loopback, com validação pré e pós-dial em toda navegação/redirect/subresource/CONNECT. Um bloqueio tardio invalida a captura. RustFS ausente = fallback desligado, demais endpoints continuam ok. **Por quê:** screenshot é caro (Chromium + I/O), arrisca expor páginas internas, e na maioria dos sites públicos o `og:image` já cobre. Fallback troca "imagem pobre" por "alguma imagem" sem dar custo no caminho feliz.

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
- `DELETE /api/folders/{id}?cascade=1` (apagar tudo): recursivo via CTE — coleta owner-scoped e trava toda a subtree na transação; se houver descendente protegido diferente da raiz, retorna `409 descendant_protected` + `count` sem mutar nada. Sem essa barreira, deleta links/notas em todos os níveis e então as pastas.

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
- **Streaming end-to-end.** Export não materializa `Snapshot`: sob `REPEATABLE READ`, cursor rows alimentam um spool `database.json` 0600/256 MiB com hash+counts inline; RustFS LIST visita metadados por callback e retém só matches owner-scoped; após commit, `zip.NewWriter(http.ResponseWriter)` copia o spool e faz `io.Copy` direto do GetObject. Restore usa `MultipartReader` (não `ParseMultipartForm`) e publica note media do spool em que foi validado/otimizado uma vez. Skip resolve keys existentes por LIST paginado, wipe usa multi-delete somente de keys owner-scoped, e PUTs usam pool 8.
- **3 operações canônicas**: `/api/backup` (gera), `/api/backup/validate` (sem efeito colateral — confere checksums + manifest + conflitos com DB atual), `/api/backup/restore?mode=…`. O handoff `/api/backup/download` (POST ticket + GET one-time + GET status) é só um transporte browser-managed alternativo para a mesma geração, não uma quarta operação nem um segundo ZIP.
- **Admissão é compartilhada por export/validate/restore; preflight por validate/restore.** Um slot fail-fast é adquirido antes de query/spool/body read; concorrência responde `429 backup_busy` sem chamar o service. Export retém O(owner-file-count) sob caps de 99.998 files, key 1.024 bytes e manifest 32 MiB. Antes de DB/RustFS, uploads passam por nome único, CRC e leitura real `max+1`, sob caps de 100k entries, 32 MiB de manifest, 256 MiB de database JSON, 64 MiB por arquivo e 4 GiB expandidos no total. Arrays de conteúdo têm cap de 250k; relações/eventos, 2M; `app_settings` legado, 1k.
- **3 modos de conflito**:
  - `wipe`: DELETE somente das rows/keys do caller + restore com IDs frescos. UI exige confirm destrutivo.
  - `skip` (default): `ON CONFLICT DO NOTHING` em `(user_id, tag.name/link.url)`; migration 000027 persiste digest + mappings old→new no mesmo commit do conteúdo e checkpointa files depois.
  - `duplicate`: tags renomeiam pra `nome (N)`, folders sempre criam novo, links com URL conflict caem pra skip + warning (URL é UNIQUE — duplicar quebraria invariant).
- **REPEATABLE READ no export** garante que as 5 SELECTs vejam um snapshot consistente.
- **Validação prévia** é obrigatória no frontend: usuário vê manifest + counts + conflitos antes de escolher modo e confirmar.
- **`schema_version` no manifest** rejeita backups de futuro; backups antigos podem rodar com warning (campos novos default).
- **Restore não é atômico DB+RustFS** (sem 2PC entre Postgres e S3). No `skip`, ledger `(user_id, archive_digest, mode)` + mappings duráveis retomam files após commit falho sem reaplicar folders/notes/associações/clicks. Entity/slug writes usam staging set-based; object upload/delete usa pool cancelável de 8 workers.

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

Notificações background quando o changecheck detecta change. RFC 8030 com VAPID via `webpush-go`. `push_subscription.user_id` restringe o fan-out ao dono; `endpoint` segue globalmente único porque representa um canal físico do browser.

**Por quê VAPID auto-gen on first boot.** Plug-and-play: `make up` em um host limpo gera a key, persiste em `/data/vapid.json` (0o600), e o front busca via `GET /api/push/vapid-key`. Pinar `VAPID_*` em `.env` quando quiser manter subscriptions estáveis entre recreations. O volume nomeado `foldex-data` cobre o caso "esqueci de pinar".

**Por quê 404/410 → DELETE.** Convenção RFC 8030 §7.3 — endpoint morto. Sem cleanup, `push_subscription` acumula rows zumbis pra cada Chrome reinstalado / Safari resetado / device descartado. Transport errors (DNS, timeout) NUNCA disparam DELETE — um blip de rede apagaria subscriptions vivas. O sender limita cada fan-out a 16 alvos/4 envios concorrentes e consolida os ids 2xx/404/410 em um UPDATE e um DELETE owner-scoped, em vez de serializar rede e fazer uma write por endpoint. Esses writes incluem `user_id` porque um endpoint listado por A pode ser reassinado a B antes da persistência do resultado; o resultado stale de A não pode marcar nem apagar o row de B.

**Por quê o sender é desacoplado mas pertence ao lifecycle do worker.** Depois do CAS durável, `worker.process` tenta publicar numa fila fixa de 32 notificações; workers fixos chamam `sender.Notify` com o contexto cancelável do change-check + timeout de 15s. Push lento não faz rollback do resultado, a fila cheia descarta a notificação mais nova sem criar goroutines, e `Stop` cancela/junta todo envio ativo. Falha de push = log, segue.

**Por quê SW hand-rolled em vez de `workbox-*` runtime.** `bun.lock` é fonte da verdade (CLAUDE.md §1) e adicionar workbox-* runtime exigiria regenerar lock + revalidar 200+ deps transitivas. Um par de `cache.put` + `push`/`notificationclick` listeners cabe em ~80 linhas (`web/src/sw.ts`). `vite-plugin-pwa` com `strategies: 'injectManifest'` injeta só o `__WB_MANIFEST` no build — zero runtime workbox.

**Por quê `/api/push/vapid-key` fica atrás de autenticação** (à época do guard `SHARED_SECRET`; hoje a pilha de sessão). "É só a chave pública" não justifica vazar superfície — um attacker remoto enumerando endpoints saberia que foldex tem push wired. Tudo `/api/push/*` exige sessão.

### ADR-25 — oEmbed enrichment via o mesmo `preview.Fetcher`
**Status:** Done (v1.4.0).

Preview/metadata enriquecem título/descrição/imagem com oEmbed quando o HTML é pobre (ex: YouTube serve HTML degraded pra fingerprint de container). `internal/preview/oembed.go:fetchOEmbed` reusa `f.client` (o transport SSRF-guarded) — **nunca** um segundo HTTP stack.

**Por quê o scheme guard no edge é crítico.** O `OEmbedURL` é capturado de `<link rel="alternate" type="application/json+oembed">` — HTML controlado por atacante. O transport default do Go lê `file:///etc/passwd` feliz porque o dialer a nível de IP não dispara pra schemes não-http(s). Por isso `fetchOEmbed` força `u.Scheme ∈ {http, https}` ANTES do `http.NewRequestWithContext` (mesma postura do metadata handler).

**Detalhes de implementação.** A discovery URL é resolvida contra o `finalURL` da página via `resolveRelatives`, pra `href`s path-relative (WordPress, SoundCloud, Flickr) baterem no host certo. Sub-deadline de 5s por leg oEmbed limita wall-clock independente do ctx do caller. `knownOEmbedProviders` (hosts YouTube + Vimeo) atalha direto pro oEmbed quando o host bate; o resto leva HTML fetch + enrichment oportunista por discovery. **Merge contract:** HTML sempre ganha o que tem — oEmbed só preenche campos vazios.

### ADR-26 — Camadas de segurança no CI: SAST + DAST + Dependabot
**Status:** Done (v1.4.7).

Empilhamos múltiplos scanners em vez de um só, pra comparar cobertura e não depender de um único engine:

- **SAST estático** — três engines em paralelo: **CodeQL** (`security-extended`, Go com `build-mode: manual` porque o `go.mod` fica em `backend/`, + JS/TS) em `codeql.yml`; **Semgrep** (packs OWASP/secrets/golang/typescript/react/dockerfile/github-actions, imagem digest-pinned) + **gosec** (linter Go: SSRF, crypto fraca, SQLi) em `sast.yml`. Todos sobem SARIF pra aba **Security ▸ Code scanning**; o upload é guardado por `hashFiles()` pra um scan que não gerou arquivo não virar o job vermelho.
- **DAST dinâmico** — **OWASP ZAP baseline** (passivo, não-destrutivo, imagem digest-pinned) em `dast.yml`, rodando **mensalmente** (`cron: 0 6 1 * *`) + dispatch manual. Builda a stack do código (`docker compose --build`), espera `/healthz` (nginx faz `proxy_pass` de `/healthz` → backend), escaneia o nginx pela rede `foldex` mirando `https://web` (ZAP aceita cert self-signed upstream por default). Relatório HTML/MD/JSON como artefato de 30 dias.
- **Dependabot** — `dependabot.yml` cobre 4 ecossistemas (github-actions, gomod, docker ×2), agrupando minor+patch pra reduzir ruído de PR; major fica separado pra review de breaking change. **npm fica de fora de propósito**: o web usa bun (`web/bun.lock`) e o ecossistema npm do Dependabot só reescreve `package.json` sem regenerar o `bun.lock` — todo PR dele quebrava o `bun install --frozen-lockfile`. Frontend deps são atualizadas manualmente via `bun update` (fluxo do §1). (Outra limitação conhecida: o ecossistema docker só lê `FROM` em Dockerfiles, não os `image:` dos `docker-compose.*.yml` — o pin triplo do Postgres do §1 continua manual.)

**Por quê informativos primeiro.** Todos seguem a postura do CLAUDE.md §2 (govulncheck/bun audit): `|| true` / `-no-fail` / `continue-on-error`, então surfam achados sem travar merge. Vira gate rígido removendo essas válvulas quando houver baseline limpa. SAST roda com `paths-ignore` pra commits só-docs (não queima runner). O DAST precisa de cert pré-gerado em `web/certs` porque o compose monta esse dir `:ro` e o entrypoint do nginx não consegue escrever o par efêmero num mount read-only. Imagens de container (Semgrep, ZAP) são pinadas por **digest** — a regra de SHA-pin do §4 vale igual pra elas, já que tag mutável tem o mesmo risco de swap silencioso. Um job `actionlint` em `ci.yml` (imagem digest-pinned, traz shellcheck) linta todos os workflows em cada PR pra pegar regressão de sintaxe / action não-pinada antes de um run real.

**Baseline triada (1ª passada, 21 alertas → 0 reais).** O primeiro scan abriu 21 alertas (CodeQL 3, gosec 14, Semgrep 4); todos triados e **dispensados** na aba Security — **nenhum acionável**. 6 `false positive` (sanitizadores que as ferramentas não modelam: `safeLinkHref` só passa `^https?://`; `http.Redirect` limpa CR/LF; `<img src>` não é sink de script; MIME já validado; sem captura de loop-var; misfire de regra em `json.Marshal`) e 15 `won't fix` (mitigações por design: o `safeDialer` SSRF pré+pós-dial que o CodeQL não enxerga; `http.MaxBytesReader` + cap de 50 MP; segredo VAPID `0o600`; path de config do operador; `$host` do nginx inofensivo num deploy single-user localhost). Cada dismiss carrega comentário com o motivo. **Antes de tratar um achado novo como real, conferir se não é uma re-emissão (fingerprint novo) de um destes padrões já triados** — refatorar uma dessas linhas pode reabrir o mesmo "não-problema". Se a re-emissão virar recorrente num ponto, migrar pra supressão inline (`#nosec` / `# nosemgrep`) que viaja com o código.

### ADR-27 — Notes como terceira entidade polimórfica, compartilhando link_tag/click_log/folder
**Status:** Done (migration 000014).

Notes são entradas pastebin-style — título + corpo HTML rico (Tiptap, markdown paste, imagens inline) — que se comportam como um terceiro tipo de entidade de primeira classe ao lado de `link`/`folder`: mesmo grid, mesma busca, mesmo sistema de tags/pastas, badge diferenciado no card.

**Por quê tabela `note` separada em vez de uma coluna `kind` em `link`.** `link` carrega invariantes URL-específicas (UNIQUE url, pipeline de preview, change-detection) que não se aplicam a notes. Um `kind` discriminador em `link` significaria uma dúzia de colunas nullable e um CHECK cada vez mais largo — pior que duas tabelas com o overlap real (título/slug/folder/pinned) replicado, que é pequeno.

**Por quê polimorfizar `link_tag`/`click_log` em vez de duplicá-las.** A alternativa óbvia — `note_tag`/`note_click_log` — duplicaria a lógica de M:N e de agregação de cliques (já não-trivial: `tagsFor`, `setLinkTags`, o LATERAL join de click_count, os índices de busca por tag). Em vez disso, `link_id` virou `entity_id` + um novo `entity_kind TEXT CHECK (IN ('link','note'))`. Custo: a FK pra `link(id)` precisou ser dropada (uma coluna polimórfica não pode referenciar duas tabelas), então o cascade de delete que antes vinha de `ON DELETE CASCADE` virou app-level — `links.Repository.Delete` e `notes.Repository.Delete` agora apagam suas próprias rows de `link_tag`/`click_log` na mesma tx do delete da entidade. **Toda query contra essas duas tabelas DEVE filtrar `entity_kind`** — sem isso, um id de note pode colidir com um id de link (mesmo espaço numérico) e vazar tag/clique pro lado errado. `TestCrossContamination_LinkAndNoteRowsDoNotLeak` (`internal/notes`) é o regression guard.

**Por quê `internal/entries` (UNION ALL) em vez de merge client-side.** O grid interleaved precisa de busca + sort + paginação unificados entre link e note. Hoje folders "interleiam" com links só porque folders carregam tudo de uma vez (não paginado) enquanto links são paginados — não existe precedente de merge entre dois streams independentemente paginados, e notes precisam de paginação real. Um merge client-side de duas `useInfiniteQuery` mantendo ordem global consistente através de pinned + 5 modos de sort seria um subsistema novo e frágil. Uma única query SQL `UNION ALL` (cada braço com o mesmo filtro/ordenação que `links.List`/`notes.List` já fazem) mantém sort/paginação numa única fonte, no padrão "busca é 100% server-driven" que o resto do projeto já segue. `internal/entries` é **somente leitura**; list, counts e preview-status são projeções, enquanto mutações continuam em `/api/links` e `/api/notes`. Detalhe de implementação: o `ORDER BY` precisa estar fora do `UNION ALL` (numa subquery/derived table) porque Postgres proíbe expressões como `lower(title)` direto sob um set operation.

**Por quê `/n/{id-or-slug}` renderiza em vez de redirecionar.** `/go/{id-or-slug}` resolve pra uma URL externa e redireciona; uma note não tem URL externa — `/n/` precisa renderizar o conteúdo. Fica fora de `/api` (junto com `/go/`) pra ficar compartilhável sem o guard de `SHARED_SECRET`, mesma postura de link. Loga em `click_log` (`entity_kind='note'`) na mesma tx da resolução — é o que justifica ter polimorfizado `click_log` em primeiro lugar, e dá a notes um `click_count` de graça via o mesmo padrão de LATERAL join.

**Por quê sanitização server-side obrigatória (`internal/pkg/htmlsanitize`).** O body da note é HTML renderizado cru tanto no app (dialog/editor) quanto na página pública `/n/`. O cliente nunca é confiável — um cliente de API malicioso pode mandar qualquer coisa. `htmlsanitize.Sanitize` roda em todo Create/Update, allowlist explícita (`bluemonday.NewPolicy()`, não um preset) batendo exatamente no output do Tiptap StarterKit: sem `<table>` (extensão não usada ainda — fechar a allowlist em vez de abrir especulativamente), sem URL scheme `data:` (força toda imagem inline a passar pelo endpoint de upload em vez de embutir base64). `body_text` (coluna de busca ILIKE/trigram) é **sempre** derivado server-side do HTML já sanitizado — nunca aceito do cliente — pra search não poder divergir do que está armazenado/renderizado.

**Gaps conhecidos do v1.** Imagens inline removidas do editor antes de salvar (nunca chegaram a `body_html`) não são limpas do object storage — só o delete de uma note faz best-effort cleanup das imagens ainda referenciadas no `body_html` no momento do delete; não existe um job de sweep de órfãos. `<table>` fica fora da allowlist até uma extensão de tabela ser adicionada ao editor.

### ADR-28 — Senha por pasta: redação sempre-ativa + gate por token curto-vivo
**Status:** Done (migration 000015).

Feature de **privacidade**, não de segurança dura: Foldex é single-user atrás de um `SHARED_SECRET` único; uma senha por pasta protege contra "alguém olhando por cima do ombro / compartilhando tela", não contra um atacante que já tem acesso à API. Isso guiou toda decisão de escopo abaixo — proteção real o suficiente pra não ser teatro, mas sem inventar um segundo sistema de auth completo.

**Por quê `password_hash` nullable em `folder` em vez de uma tabela separada.** Mesma lógica de `link.slug`/`link.pinned`: é um atributo 1:1 da pasta, não uma entidade própria. `NULL` = sem proteção (todo folder existente após a migração). bcrypt (`golang.org/x/crypto/bcrypt`, já presente em `go.sum` como indireto — zero deps novas) hasheia; o plaintext nunca é armazenado nem logado.

**Por quê dois mecanismos separados — redação sempre-ativa vs. gate por token.** São dois vazamentos diferentes: (1) um card de pasta protegida ainda aparece na listagem (nome, cor, contadores) — mas seu preview (thumbnails de links/subpastas dentro dela) não pode vazar por hover, mesmo sem tentar abrir; (2) efetivamente *entrar* na pasta (ver seus links/notes reais, ou listar suas subpastas) precisa de prova de senha. `folders.Repository.List`/`Get` **sempre** zeram `preview_links`/`preview_folders` quando `has_password=true`, incondicionalmente — nenhum request, com ou sem token, vê esse preview numa listagem. Isso significa que `FolderRapidView` (hover popover do frontend) fica vazio de graça, sem precisar de lógica extra no client. Já o gate por token cobre todas as listagens que podem revelar conteúdo real: `GET /api/{entries,links,notes}?folder_id=X` e `GET /api/folders?parent_id=X`. Todas exigem um token válido pra `X` quando `X` é protegida, senão `403 folder_locked`.

**Um helper é dono do protocolo de listagem protegida.** `folders.ListWithContentGate[T]` recebe o `folder_id` opcional e uma closure `List` tipada. Sem pasta ele executa a closure diretamente; com pasta faz check owner-scoped do hash atual, executa a closure uma vez e repete o check antes de devolver o valor. O segundo check rejeita o payload se a senha mudar durante a query. Lookup nil falha fechado antes da consulta, e qualquer falha de gate devolve o valor zero de `T`, nunca o conteúdo já lido. Os handlers de entries/links/notes só parseiam a query/header, fornecem a closure concreta, mapeiam o erro e serializam seus tipos de resposta existentes. API tokens continuam credenciais de conteúdo e atravessam o mesmo protocolo; não há branch por tipo de principal.

**Por quê um token HMAC curto-vivo em vez de sessão server-side ou reenvio da senha a cada request.** `POST /api/folders/{id}/unlock` verifica a senha via bcrypt e emite `hex(HMAC-SHA256(secret, "<folderID>:<expiresAt>:<passwordHashATUAL>")) + "." + expiresAt`. O truque: o HMAC inclui o `password_hash` **atual** da pasta, buscado fresco do banco a cada verificação — trocar ou remover a senha invalida automaticamente todo token emitido antes, sem precisar de uma lista de revogação. TTL de 24h é só um teto de segurança; a sessão real do usuário é mais curta que isso porque o frontend nunca persiste o token além do reload da página (decisão do usuário — unlock é por sessão de browser, não sobrevive a restart). O secret HMAC (`internal/folders/password.go:LoadOrGenerateFolderUnlockKey`) segue o mesmo padrão env→file→autogen(`0600`) que `push.LoadOrGenerate` já estabeleceu pro VAPID — mesmo volume `foldex-data`.

**Por quê trocar/remover uma senha existente exige a senha atual, mas renomear/recolorir/mover não.** Decisão explícita do usuário (registrada em memory/sessão): editar metadados de uma pasta nunca precisa de senha — só *trocar ou remover uma senha já existente* exige prová-la primeiro, verificado dentro da mesma tx SERIALIZABLE que já protege o cycle-check de `parent_id` (`folders.Repository.Update`). **Sem bypass de admin** — se o usuário esquecer a senha, a única recuperação é editar o banco diretamente. Setar uma senha pela primeira vez (pasta ainda sem proteção) não exige `current_password`, porque não há nada pra autorizar contra ainda.

**Cascade destrutivo para na primeira fronteira protegida.** O token de unlock é específico da raiz e não prova a senha de uma subpasta, pois proteção não é herdada. `DeleteCascade` materializa a subtree com `user_id` e trava suas linhas na mesma transação; qualquer `password_hash` em descendente diferente da raiz aborta tudo como `409 descendant_protected` com a quantidade encontrada. Uma raiz protegida ainda pode ser apagada com seu token atual quando não há descendente protegido. Na UI, DELETE sem token ou com token stale recebe `folder_locked`, abre o prompt existente, guarda o novo token só em memória e repete uma vez; nova falha e `descendant_protected` ficam inline, sem promise rejection solta.

**Rate limiting + revelação da dica (por pasta).** O endpoint de unlock tem um limitador **em memória por pasta** (`internal/folders/ratelimit.go`): 5 senhas erradas seguidas → bloqueio de **1 hora** (`429 too_many_attempts`, com `Retry-After`), e um acerto zera o contador; o estado é in-memory (reinício do backend limpa — aceitável no modelo single-user/local, o custo do bcrypt é o piso real). O frontend (`PasswordPromptDialog`) mostra tentativas restantes, faz a contagem regressiva do bloqueio, e **só revela a `password_hint` depois da 3ª tentativa errada** (a dica é não-secreta, mas segurá-la até o 3º erro incentiva lembrar de cabeça antes).

**Escopo explicitamente fora do v1 (documentado, não esquecido).** Sem cascata pra subpastas — proteger uma pasta não protege automaticamente o que está dentro dela; cada pasta precisa da própria senha (decisão do usuário: modelo mental mais simples, sem precedente de permissão herdada em nenhum outro lugar do app). Mover um link/note PRA DENTRO de uma pasta trancada (drag-and-drop ou folder picker) não exige desbloqueio — é escrita, não revela conteúdo existente. `exporter`/`importer`/backup não são gateados — são operações já-confiadas do dono autenticado, equivalentes a acesso direto ao banco; mas o hash em si atravessa backup/restore **verbatim** (nunca re-hasheado, nunca dropado — `backup.FolderRow.PasswordHash`, presente nos 3 modos de restore) pra um restore não remover silenciosamente a proteção. Sem rate-limiting além do custo intrínseco do bcrypt (~100ms/tentativa) — proporcional ao modelo de ameaça single-user local.

### ADR-29 — Senha master (recuperação) + palavra-dica por pasta
**Status:** Done (migration 000016). **Relaxa** a cláusula "sem bypass de admin" do ADR-28 — apenas para recuperação.

O ADR-28 deixou a recuperação de uma senha de pasta esquecida como "edite o banco direto". Na prática isso é hostil pro dono single-user. O ADR-29 adiciona um mecanismo de **recuperação** — nunca um bypass de visualização.

**Senha master: só recuperação, nunca unlock.** `POST /api/folders/{id}/reset-password` recebe `{master_password}`, verifica-a e, se correta, **limpa** `password_hash` + `password_hint` da pasta (`folders.Repository.ResetPasswordByMaster`). Depois disso a pasta fica desprotegida e o dono define uma nova senha pelo fluxo normal de primeiro-set (PATCH sem `current_password`). Deliberadamente NÃO tocamos a semântica de unlock/token nem criamos um branch de bypass em `checkPasswordChangeAuthorized`: a master não abre pasta pra visualização, não emite `unlock_token`. Como o HMAC do token de unlock inclui o `password_hash`, zerá-lo já invalida todo token vivo (mesma propriedade do ADR-28, de graça). `400 master_not_configured` se não há master; `401 wrong_master_password` se errada.

**Por quê `app_setting(key,value)` (KV) em vez de env var ou coluna.** A master precisa ser definível/alterável **pela UI** — env var (imutável em runtime) não serve. Uma tabela KV genérica (`internal/settings`) é a única opção mutável e é future-proof pra outras settings singleton sem nova migração. Duas chaves hoje: `master_password_hash` (bcrypt via o leaf `internal/pkg/pwhash`, compartilhado com `folders` — mesma função de hash pra toda senha do app) e `master_password_hint` (frase-lembrete NÃO-secreta, análoga a `folder.password_hint` — retornada verbatim, nunca hasheada, nunca igual à senha). O hint é **tri-state** no `SetMasterPassword` (mesma tx do hash): `nil` = mantém o hint atual (trocar a senha com o campo vazio NÃO apaga o lembrete), `""` = remove, valor = define; `ClearMasterPassword` remove hash + hint juntos. Após salvar, o form da UI limpa tudo e mostra o hint atual como linha somente-leitura. O plaintext da senha nunca é armazenado nem logado; `GET /api/settings/master-password` retorna só `{configured: bool, hint}` (a dica, nunca o hash). Política de comprimento mais forte que a de pasta (≥8 vs ≥4) — a master é a chave de recuperação de tudo. Trocar exige a master atual; remover exige a master atual; setar pela primeira vez não exige nada. A UI ainda adiciona **medidor de complexidade** e **confirmar senha** como guias client-side (o backend só força o mínimo de comprimento — o medidor é orientação, não gate).

**Palavra-dica (`folder.password_hint`): não-secreta por design.** Uma frase-lembrete opcional, exibida no popup de unlock pra ajudar o dono a lembrar a senha. Ao contrário de `password_hash`, ela é retornada **verbatim** em toda resposta de folder (não redigida) — expô-la é o propósito, e o modelo single-user/local torna isso aceitável. Invariante travada em teste: a dica **nunca pode ser igual à senha**. No create a comparação é de plaintext; no update (onde a senha pode não estar mudando) a verificação é `bcrypt.CompareHashAndPassword(effectiveHash, hint)` dentro da mesma tx SERIALIZABLE — pega a igualdade mesmo sem o plaintext. Remover a senha limpa a dica junto (dica sem senha é dado morto).

**Backup.** `app_setting` e `folder.password_hint` atravessam backup/restore **verbatim** nos 3 modos (`backup.AppSettingRow`, `backup.FolderRow.PasswordHint`) — o snapshot continua completo. Wipe restaura exatamente as settings do zip (inclusive "sem master" pra um backup antigo, ADR-28-era); skip/duplicate não clobberam uma setting singleton já existente (`ON CONFLICT DO NOTHING`). Consequência de segurança: o zip de backup agora carrega o hash da master (como já carregava os hashes de pasta) — mesma postura.

**Duas ações por pasta em Configurações** (ambas via o mesmo endpoint master-verified, diferindo só no follow-up): **Redefinir senha** limpa e oferece "Definir nova senha" (recuperação: esqueci, quero trocar); **Remover senha** limpa e deixa a pasta desprotegida (sem sugerir nova). Ambas exigem a senha master.

**Escopo fora do v1.** Sem "limpar todas as pastas de uma vez" (reset é por-pasta, cirúrgico, a partir de uma lista em Configurações). Sem recuperação da própria master esquecida (aí sim volta a ser edição direta no banco — é o segredo raiz). Sem dica pra master (só pra pastas).

### ADR-30 — Autenticação multi-usuário: sessão por cookie, CSRF assinado, RBAC e posse por linha
**Status:** In progress — PR1 (segmentação, migration 000017/000018), **PR2 (identidade)** e **PR3 (2FA + recuperação de senha)** entregues; PR4 (OAuth + `AUTH_ENABLED=1` por padrão) pendente. **Supersede o ADR-3** ("Sem auth no MVP"). Detalhe de implementação em [`docs/SDD-AUTH-RBAC.md`](SDD-AUTH-RBAC.md).

**O que o PR2 entregou:** `internal/mailer` (drivers `smtp` + `log`, este último o padrão para que uma instância sem SMTP ainda consiga convidar alguém), `internal/auth` (sessão, rotação, CSRF, RBAC, convites, sweeper), `internal/pkg/secrets` e `internal/pkg/attemptlimit`, a superfície `/api/auth/*` + `/api/admin/*`, e no frontend `AuthProvider`/`AuthGate` acima do `<App/>` com as telas de login, setup e aceite de convite. `AUTH_ENABLED` continua `0` por padrão.

**O que o PR3 entregou:** TOTP (`pquerna/otp`, parâmetros fixos em SHA1/6/30, QR renderizado no servidor), códigos de recuperação de uso único, OTP por e-mail, `auth_challenge` + cookie `fx_pa`, enrollment obrigatório para admin, `password/forgot` + `password/reset`, `internal/pkg/keyfile` e `secrets.Cipher`. Frontend: `OtpInput`, as telas de código, esqueci-a-senha, enviado, redefinir e enrollment, mais a seção de 2FA em Configurações. **Migration `000019_two_factor_indexes`** — nenhuma tabela nova (a 000017 já criara as cinco), só dois índices que faltavam e a coluna `mailbox_already_proven`; é 19 e não 18 porque o PR1 já entregara `000018_click_log_user_id`. `AUTH_ENABLED` continua `0` por padrão.

**Correção do PR3 que vale registrar: código numérico e código de recuperação se distinguem pelo comprimento SEM SEPARADORES, não pela contagem de dígitos.** A primeira implementação filtrava a entrada para dígitos e perguntava "tem seis?". O formato atual tem 16 símbolos de um alfabeto de 32, dos quais 10 são dígitos — então **cerca de 18%** contêm exatamente seis dígitos e seriam roteados para TOTP, onde nunca casam. Travado por `TestRecoveryCode_WithSixDigitsIsNotMistakenForATOTPCode`, que constrói o caso em vez de depender do acaso.

**Correção do PR3 que quase escapou: uma caixa postal comprometida emitia sessão.** O reset de senha diverge corretamente para o desafio — provou só um fator — mas o fallback de OTP por e-mail mandava o código de seis dígitos para **o mesmo endereço** que recebeu o link. `/password/forgot` → link → `/password/reset` → desafio → `/2fa/email` → código na mesma caixa → sessão: os dois passos fechavam num canal só, e o docstring do handler dizia literalmente que o desvio existia para impedir isso. `auth_challenge.mailbox_already_proven` marca os desafios nascidos de um reset e o fator e-mail é recusado para eles. Pelo mesmo raciocínio o fator e-mail passou a exigir `MAIL_DRIVER=smtp`: o driver `log` escreve o corpo no stdout, troca deliberada para link de convite (numa instância sem SMTP o log É a caixa postal) mas não para um segundo fator.

**Redação de credencial mora no handler raiz de log, e `X-Forwarded-For` deixou de ser confiável por padrão.** `logsafe.RedactHandler` envolve o `slog.Handler` base em `main.go` e apaga o valor de todo atributo cuja chave nomeia credencial — inclusive através de `WithAttrs`, porque `logger.With("token", raw)` guarda o atributo uma vez e o repete em cada registro. Nada loga segredo hoje; a guarda existe para a próxima linha de log escrita às pressas. E `server.trustedProxyRealIP` substituiu o `middleware.RealIP` do chi: honrar `X-Forwarded-For` incondicionalmente está certo atrás do nginx e é forjável em bind direto, onde torna decorativo todo bucket de rate limit por IP. Sem `TRUSTED_PROXY_IPS` o header é ignorado — um endereço falsificável é pior que um grosseiro, porque permite ao atacante escapar do próprio bucket e ainda jogar o custo no de outro.

**A chave que cifra o seed TOTP não é regerável, e por isso `keyfile` tem `AllowEphemeral`.** `folders.LoadOrGenerateFolderUnlockKey` sempre aceitou seguir com uma chave só-de-sessão quando não conseguia persistir — correto lá, porque perdê-la só invalida tokens de unlock e o usuário redigita a senha da pasta. Para `AUTH_ENCRYPTION_KEY` o mesmo comportamento destrói dado em repouso: todo seed vira indecifrável e cada usuário com 2FA fica trancado para fora. Com `AllowEphemeral: false` o backend **recusa subir** — falhar na hora é reparável, subir e descobrir no restart seguinte não é.

**Correção do PR2 que vale registrar: a janela de graça emite uma sessão IRMÃ, não novos tokens na mesma linha.** A primeira implementação regravava o trio na própria linha da sessão, e isso derrota o propósito da janela: o servidor só guarda hashes, então não consegue devolver o trio que a requisição vencedora acabou de instalar, e regravar invalida o que ela está segurando. As duas requisições vêm do **mesmo cookie jar**, então o browser fica com a resposta que chegou por último enquanto a linha guarda a outra — e a aba que perde a corrida é deslogada. A irmã herda `family_id` (para morrer junto na detecção de reuso) **e** `created_at` (senão um cliente poderia surfar a janela a cada rotação e empurrar o teto absoluto de 90 dias para sempre). Travado por `TestRefresh_GraceSiblingInheritsFamilyAndAbsoluteCeiling` e `..._DiesWithTheFamilyOnReuse`.

**Bootstrap reivindica o placeholder, mas não depende dele.** O caminho normal é dar `UPDATE` na linha `pending` que a 000017 insere — é ela que adotou todo o conteúdo pré-migração, então reivindicá-la é o que faz uma instalação existente manter seus links. Quando não existe placeholder (banco restaurado de um dump, linha apagada à mão), o bootstrap **insere** um admin novo em vez de falhar: a tela de setup precisa ser suficiente para recuperar a instância, sem SQL direto. Os dois caminhos correm sob `pg_advisory_xact_lock`, porque o guard "já existe alguém ativo?" é read-then-write e o alvo pode não existir para travar com `FOR UPDATE`.

O foldex era single-user em três camadas que se sustentavam mutuamente: sem identidade (`SHARED_SECRET` responde "sim/não", nunca "quem"), sem posse (nenhuma tabela de conteúdo tem dono, e `tag.name`/`link.url` são UNIQUE **globais** — restrições que só fazem sentido com um usuário), e sem recuperação por pessoa (a master do ADR-29 é da instância). O ADR-30 troca as três de uma vez.

**Posse por parâmetro explícito, não por RLS.** Todo método de repositório passa a receber `uid authctx.UserID` — um tipo distinto, não `int64`, para que argumentos trocados sejam erro de compilação. Descartamos RLS com `SET LOCAL app.user_id` por três motivos concretos: (1) quase todo método é `r.pool.Query(...)` direto em conexão pooled, então RLS exigiria converter ~60 métodos para transação explícita — **mais** churn que adicionar um parâmetro; (2) RLS **falha em vazio** — esquecer o `SET LOCAL` devolve zero linhas, indistinguível de "usuário sem dados", e isso vai para produção; o parâmetro explícito **falha na compilação** e `go build ./...` enumera todos os call sites; (3) há leitores cross-tenant legítimos (`preview.Worker.requeuePending`, `links.FindDueForCheck`) que sob RLS precisariam de `BYPASSRLS`, segundo role e segunda DSN. Convenção de reforço: toda query sem escopo mora em `repository_system.go` com métodos prefixados `System*`, e um grep no CI barra `FROM link|note|folder` sem `user_id` fora desses arquivos.

**Integridade cross-tenant é do banco, não do handler.** `folder` ganha `UNIQUE (user_id, id)`, e as FKs de `link.folder_id`, `note.folder_id` e `folder.parent_id` viram compostas `(user_id, folder_id)` com `ON DELETE SET NULL (folder_id)` (lista de colunas, PG15+). Uma linha não consegue apontar para a pasta de outro tenant nem se um repositório perder o filtro. A lista de colunas é obrigatória: sem ela, apagar uma pasta tentaria anular também `user_id`, que é `NOT NULL`. Sobra **um** buraco que a FK não fecha — `link_tag` perdeu a FK para `link(id)` na 000014 ao ser polimorfizada, e `tag_id` não carrega `user_id` para compor; anexar tag alheia é barrado em `tags.SetEntityTags` e travado por teste.

**`link.slug` e `note.slug` continuam UNIQUE globais.** `/go/{slug}` e `/n/{slug}` resolvem **sem sessão**, logo sem tenant — se o slug fosse único por usuário, a rota pública não teria como escolher entre dois donos. Colisão entre usuários cai no sufixo `-2`/`-3` que já existe. Pela mesma lógica invertida, `link.url` e `tag.name` viram `UNIQUE (user_id, …)`: dois usuários podem salvar a mesma URL.

**Sessão: access curto em cookie + refresh opaco rotativo com detecção de reuso.** `fx_at` (15 min, `SameSite=Lax`, `Path=/`), `fx_rt` (30 d, `Strict`, `Path=/api/auth`, teto **absoluto** de 90 d porque a expiração desliza a cada rotação), `fx_csrf` (legível por JS). Todos guardados como `sha256` em `BYTEA`, nunca em claro — um dump do banco não pode ser um kit de sequestro de sessão. É `sha256` e não bcrypt porque são 256 bits de `crypto/rand` (não há dicionário a atacar) e a resolução está no hot path de toda request. Refresh consumido entra em `session_used_token`; um hit **fora** de uma janela de graça de 10 s revoga a *família* inteira. A janela existe porque sem ela qualquer duplo-mount do SPA (StrictMode, duas abas, reload rápido) é classificado como reuso e mata a sessão — o trade-off é uma janela de 10 s em que um replay muito rápido não é detectado.

**CSRF é double-submit *assinado*.** O header `X-Foldex-CSRF` é comparado contra `session.csrf_token_hash` — a linha da sessão, **não** o cookie. Double-submit ingênuo (header == cookie) é derrotado por cookie injection de subdomínio irmão, porque o atacante escolhe os dois lados; amarrando à sessão, ele precisaria forjar um valor cujo `sha256` bata com o guardado. Só verbos inseguros, e só quando a autenticação veio de sessão — bearer não tem credencial ambiente para carona.

**Revogação é instantânea**, para sessão e token de API, porque ambos são resolvidos no banco a cada request. Troca deliberada de um lookup indexado por request em favor de revogação exata.

**Linha alheia responde 404, nunca 403.** 403 confirmaria que o id existe, e num espaço BIGSERIAL denso isso enumera o conteúdo dos outros sem ler nada.

**Login indistinguível.** E-mail inexistente, senha errada e conta desabilitada devolvem `401 invalid_credentials` byte-idêntico. bcrypt **sempre** roda (contra um hash dummy no miss — pular é o oráculo clássico de ~80 ms), o bucket de rate limit por e-mail incrementa **também** para e-mails inexistentes (não incrementar é por si só um oráculo), e há um **piso** de 250 ms — piso e não jitter, porque jitter só adiciona variância que o atacante remove com média amostral.

**2FA: TOTP obrigatório para admin, OTP por e-mail como fallback.** O estado entre "senha OK" e "2FA OK" é um `auth_challenge` + cookie `fx_pa` que só autoriza `/api/auth/2fa/*` — nunca alcança endpoint de dados. Login, bootstrap, aceite de convite admin e OAuth passam pela mesma decisão; promoção `user → admin` revoga as sessões existentes na própria transação da mudança de papel. `RequireAdmin` consulta a existência atual de `totp_secret.confirmed_at`, portanto ligar `AUTH_REQUIRE_2FA_FOR_ADMINS` bloqueia poder administrativo também em access/refresh antigos sem impedir as rotas de enrollment; não há coluna de assurance. O contador de tentativas mora **no banco**, não no limitador em memória. A migration `000025` prende cada challenge e enrollment TOTP pendente ao `app_user.token_version` que recebeu a prova; linhas legadas sem epoch falham fechadas. Resolve/budget/consumo, enrollment e emissão de sessão revalidam o epoch vivo sob lock da linha do usuário, portanto reset/troca de senha, logout-all e revogação administrativa matam o estado antigo. Consumo de TOTP/recovery/e-mail OTP, challenge e emissão de sessão formam uma única transação; confirmação de enrollment inclui recovery codes e, no fluxo obrigatório, a sessão; set-password, disable e regenerate também consomem o proof na própria tx. Falha tardia não queima a prova nem deixa fator sem recovery. O seed TOTP é **cifrado** com AES-256-GCM, e cada consumo compara `secret_ciphertext` + `secret_nonce`; a confirmação também exige `enrollment_token_version` original. OTP numérico e recovery usam MACs HMAC-SHA256 indexáveis sob subchaves distintas derivadas de `AUTH_ENCRYPTION_KEY`; o material AES nunca é chave de MAC direta. A entrada inclui versão/purpose e bindings de usuário/challenge, e recovery tem 16 símbolos base32 (80 bits), formato `XXXX-XXXX-XXXX-XXXX`. A migration `000023` apaga digests legados, irreversivelmente, pois o plaintext necessário para convertê-los nunca foi armazenado; ciphertext/nonce continuam versionando o seed, enquanto `token_version` versiona a prova de credencial que autorizou o fluxo.

**Reset também pertence ao epoch da credencial.** A migration `000028` grava o `app_user.token_version` vivo em resets comuns e administrativos; linhas legadas permanecem NULL e falham fechadas. O consumidor resolve o owner, trava `app_user` e exige igualdade exata antes de gastar o token, trocar a senha ou revogar sessões. Troca/reset de senha, logout-all, role/status e revogação administrativa entre emissão e consumo invalidam o link; o spend, o novo hash, o bump e a revogação continuam numa única transação. O check `NOT VALID` exige epoch em linhas novas sem inventar prova para links pré-migration, e supersede ignora esses NULLs até o sweeper removê-los.

**E-mail assíncrono é bounded e process-owned.** Um único dispatcher de 2 workers e fila 32 substitui goroutines destacadas por envio; cancellation pertence ao dispatcher e `Stop` faz join depois do drain HTTP. Links de reset, OTP de login e links de verificação reservam admission antes da escrita durável, então fila cheia não supersede credencial anterior nem cobra budgets (`auth_challenge.sends` ou limiter de reset por endereço). Budget + INSERT do OTP são atômicos, e resend autenticado de verificação coalesce por 60 s. O force-reset administrativo permanece síncrono dentro da transação: só commit depois de SMTP aceitar.

**Master password migra de `app_setting` para `app_user`.** Uma master global deixaria qualquer admin limpar a senha de pasta de **outro** usuário — exatamente o bypass que o ADR-28 recusou.

**Backup muda de contrato.** Nenhuma tabela de auth entra no ZIP (hashes, seeds TOTP e refresh tokens vivos num arquivo que se joga no Drive converteriam uma conveniência em primitiva de roubo de credencial), e o restore **sempre** escreve para quem chamou — `user_id` nunca vem do ZIP, o que torna impossível forjar um backup que planta linhas na conta alheia. Consequência: `wipeAll` vira `wipeUser`, e **o modo wipe deixa de preservar ids** (`restoreIdentity` é removido; outro tenant pode ter aqueles ids, e dar `setval` numa sequência global a partir do restore de um usuário é errado). Como as chaves de objeto são planas, o wipe passa a apagar uma **lista explícita de chaves** derivada das linhas do próprio usuário, em vez de `DeleteObjectsPrefix`.

**A migration 000022 corrige a exceção de mídia inline sem tornar a leitura pública privada.** `note_media` persiste owner + lease e `note_media_ref` exige, por FK composta, que note e objeto tenham o mesmo `user_id`. `body_html` só fornece candidatos; `INSERT ... SELECT` owner-scoped decide quais refs existem, e os caminhos destrutivos consultam ownership/refs. Não há backfill por HTML, portanto chaves UUID legadas continuam legíveis e não podem ser apagadas. `GET /api/files/notes/{uuid}.{ext}` fica fora de `SHARED_SECRET` e `Authenticate` para a página pública `/n/{slug}` carregar imagens, mas a rota fixa o prefixo `notes/` e exige UUID canônico + extensão raster conhecida; traversal e chaves id-derived de `screenshots/`/`images/` não alcançam o bucket por ela. Mídia de link continua exigindo sessão, principal e ownership. Restore gera UUID novo para toda chave `notes/` referenciada, reescreve `body_html`/`cover_url` e só então mapeia os bytes do ZIP; `user_id` é campo desconhecido e invalida o snapshot.

**`SHARED_SECRET` coexiste e é rebaixado** a header de perímetro ("não autentica ninguém e não identifica ninguém"); com os dois configurados, a request precisa do header **e** da sessão, exceto a leitura pública UUID-keyed de mídia exigida por `/n/{slug}` e o GET nativo de backup. Este último não fica público: o POST anterior exigiu header + sessão + CSRF e emitiu uma capability one-time ligada à sessão; o guard só delega enquanto ela está pendente, e `Authenticate` + ownership ainda rodam antes do stream. Removido no release seguinte. A extensão MV3 migra para `Authorization: Bearer` com escopo `content`, rejeitado em `/api/auth/*`, `/api/admin/*` e `/api/backup/*` — um token de extensão roubado não pode cunhar sessão, desligar 2FA nem exfiltrar um backup.

**Escopo explicitamente fora do v1 (documentado, não esquecido).** Compartilhamento entre usuários (é colaboração, com modelo de ACL próprio). Papéis além de `admin`/`user` (a coluna é TEXT com CHECK — adicionar é uma migration de uma linha; RBAC especulativo envelhece mal). SSO genérico SAML/OIDC. Rotação de `AUTH_ENCRYPTION_KEY`. `user_id` em `click_log` — sem FK para `link`, seria segunda fonte de verdade sujeita a divergir; as queries alcançam o dono por semi-join, com a `000018` como plano B se `stats.Daily` regredir.

### ADR-31 — OAuth Google: `sub` é a chave, e-mail coincidente abre conversão (nunca login)
**Status:** Accepted — implementado no PR4 (v1.13.0). Detalhe em [`docs/SDD-AUTH-RBAC.md`](SDD-AUTH-RBAC.md) §7.

**`user_identity.subject` é a única chave usada para encontrar uma conta.** E-mail nunca resolve login: trocar o e-mail no Google não move vínculo nenhum. Duas UNIQUEs travam as duas metades da regra — `(provider, subject)` garante que uma conta Google mapeia para no máximo um usuário foldex, e `(user_id, provider)` que um usuário vincula no máximo uma conta por provider.

**Sem auto-provisionamento e sem auto-vínculo por e-mail.** `sub` desconhecido e e-mail inexistente → `403 oauth_not_linked`. Se existisse auto-vínculo por e-mail coincidente, quem controlasse uma conta Google com o endereço da vítima entraria na conta dela.

**Portabilidade: e-mail coincidente abre *conversão*, que exige a senha atual.** Quando o `sub` é desconhecido mas o e-mail bate com uma conta existente, o fluxo não loga e não recusa: emite um `auth_challenge('convert_google')` e devolve `convert_password_account`. Só depois de `POST /api/auth/oauth/google/convert` com a **senha atual** correta é que a identidade é criada, `password_hash` vira `NULL` (a conta passa a ser Google-only) e as demais sessões são revogadas — tudo numa transação. A escrita trava `app_user`, exige status/epoch/hash atuais e consome condicionalmente o challenge exato antes de qualquer mutação; reset concorrente, challenge substituído ou POST repetido não vinculam identidade nem removem senha. Exigimos `email_verified == true` do Google, e conta não-ativa devolve **a mesma resposta** do caso inexistente, para não confirmar que ela existe.

Isso é deliberadamente mais estrito do que o argumento "o e-mail já é a raiz da identidade" permitiria — o reset de senha já entrega a conta a quem controla a caixa postal. A escolha foi tornar a conversão uma **migração deliberada**, não um caminho de recuperação. **Trade-off aceito: "esqueci a senha, entro com Google" não funciona**; quem esqueceu usa o reset e converte depois.

**OAuth nunca pula 2FA.** O retorno do Google — login normal ou conversão — desemboca no mesmo `auth_challenge` de TOTP. Sem isso, com TOTP obrigatório para admin, o OAuth seria um furo direto na regra.

**Vincular exige step-up recente, não só sessão viva.** O `GET .../start?purpose=link` é recusado; Configurações faz `POST /api/auth/oauth/google/start` com CSRF, senha atual e, quando há TOTP confirmado, TOTP atual ou recovery single-use. Senha e código ficam no JSON, e a API só devolve a URL do Google depois da prova. A migration `000024_oauth_link_step_up` prende o state a `user_id`, `session_id`, `token_version` e `proof_at` de cinco minutos. O callback valida a mesma sessão/principal/epoch antes do exchange e `LinkIdentity` repete sob locks antes do INSERT, fechando TOCTOU: logout, revoke, troca/reset de senha, outra sessão do mesmo usuário, expiração e replay viram o mesmo `state_invalid`, sem vínculo. A conta vinculada continua sendo a da prova, nunca uma derivada do e-mail do Google; os e-mails não precisam coincidir. Aceitar convite via Google exige `email_verified` **e** e-mail idêntico ao do convite.

**PKCE `S256` apenas**, com o `code_verifier` no servidor (`oauth_state`) e só o `state` no cookie `fx_oauth` — ambos precisam bater no callback, e é isso que impede login-CSRF. `fx_oauth` é `SameSite=Lax` por necessidade: o redirect do Google é um GET top-level cross-site e `Strict` o descartaria, quebrando 100% dos callbacks.

**O `id_token` não é parseado** — chamamos `/v1/userinfo`. O foldex não tem lib JWT como dependência direta, e adotar uma significa assumir fetch/rotação de JWKS, `alg` confusion e validação de `aud`/`iss`/`exp` (uma classe inteira de CVEs) para economizar uma chamada HTTPS num fluxo mensal.

**Lockout de conta Google-only** tem três saídas: `force-password-reset` pelo admin dispara recovery SMTP somente para a caixa verificada, sem instalar/devolver segredo ao admin; "Definir senha" em Configurações estando logado via Google (e só então desvincular passa a ser permitido); e `/password/forgot` respondendo o mesmo `202` mas enviando "esta conta entra pelo Google" **em vez** de um link de reset, pois esse pedido não tem a autorização administrativa adicional. O recovery administrativo prepara token numa transação ainda aberta, envia por SMTP e só então faz commit; falha de envio não deixa token nem muda credencial. O target escolhe a senha no consumidor existente, que preserva TOTP como segundo fator e atomiza hash, `token_version` e revogação de sessões. Set-password e unlink também validam sessão/epoch vivos no write boundary, bumpam o epoch e revogam outras sessões na mesma tx da mudança de credencial. Resta um caso sem saída pela UI: o **último admin Google-only que perde o Google** sai por edição direta no banco, mesmo status da master esquecida no ADR-29.

O recovery administrativo captura esse mesmo epoch ainda sob o lock usado para preparar o envio SMTP. Falha de entrega faz rollback como antes; se qualquer mutação de credencial, logout-all ou ação administrativa incrementar o epoch depois do commit, o consumidor comum recusa o link antes de qualquer alteração.

**Tokens de e-mail não atravessam a requisição inicial.** Templates usam fragmentos `#invite=`, `#reset=` e `#verify=`; o frontend os captura e remove no bootstrap. Preview de convite e OAuth invite start são POST com token no body, e o último devolve a URL de autorização em JSON. `Optional` continua aplicando CSRF em sessão existente. O access log do nginx usa `$uri`, sem query, referrer ou bearer.

### ADR-32 — `/go/{id}` numérico vira opt-in quando há múltiplos usuários
**Status:** Accepted — implementado no PR4 (v1.13.0).

`redirect.Handler` resolve `/go/{id-or-slug}` tentando primeiro `strconv` e caindo para slug (ADR-7). Com um usuário, o ramo numérico é conveniência. Com vários, é uma **primitiva de enumeração cross-tenant**: um visitante anônimo caminha por ids sequenciais e descobre a URL de destino de todos os usuários, sem sessão e sem ler nada.

Decisão: o ramo numérico passa por `PUBLIC_NUMERIC_IDS`, default **`false`**, de modo que `/go/42` responde 404 e só `/go/{slug}` resolve. Mesmo tratamento para `/n/{42}` — e ali o argumento é mais forte, porque essa rota **renderiza o conteúdo** da nota em vez de redirecionar: um espaço de ids caminhável exporia o texto alheio, não só a URL de destino. O 404 é o mesmo que um slug inexistente recebe; dizer "lookup numérico está desligado" confirmaria que o espaço de ids é real e vale sondar quando a flag mudar.

(O nome do knob mudou de `PUBLIC_ID_REDIRECT_ENABLED` no plano para `PUBLIC_NUMERIC_IDS` na entrega: "redirect" não descrevia `/n/`, que renderiza.) O slug é a superfície de compartilhamento documentada (`CLAUDE.md` §4: "The slug IS exposed in LinkDialog") e tem entropia suficiente na prática. O ADR-7 continua válido para quem religar a flag.

Isso é **mudança de comportamento numa URL pública**, por isso um ADR próprio em vez de uma nota no ADR-30. Chega no PR4, não no PR1, para que a migração de dados e a de comportamento não caiam juntas.

### ADR-33 — RBAC de quatro papéis: owner/admin/editor/viewer, com matriz de permissões
**Status:** Accepted — implementado em v2.1.0.

O modelo de dois papéis (`admin`/`user`) confundia duas perguntas diferentes: *quem administra a instância* e *quem pode escrever conteúdo*. Não havia como expressar "esta pessoa lê, mas não altera", nem como distinguir quem detém a instância de quem apenas gerencia pessoas.

Decisão: quatro papéis, com a capacidade de cada um vivendo numa **matriz de 14 permissões** em `internal/pkg/authctx/permissions.go` em vez de num booleano.

| | owner | admin | editor | viewer |
|---|---|---|---|---|
| conteúdo (ler / escrever) | ✔ ✔ | ✔ ✔ | ✔ ✔ | ✔ ✘ |
| backup (exportar / restaurar) | ✔ ✔ | ✔ ✔ | ✔ ✔ | ✔ ✘ |
| pessoas, convites, auditoria | ✔ | ✔ | ✘ | ✘ |
| política da instância (ler / escrever) | ✔ ✔ | ✔ ✘ | ✘ ✘ | ✘ ✘ |
| transferir a instância | ✔ | ✘ | ✘ | ✘ |

**O conteúdo continua privado por conta.** As permissões de conteúdo decidem se uma escrita é aceita — nunca de quem são as linhas visíveis. O escopo por dono permanece exatamente onde o ADR-30 o colocou: parâmetro `uid` explícito em todo método de repositório, com `user_id` como primeiro predicado. Um viewer e um editor enxergam precisamente as mesmas linhas (as suas); diferem só em o servidor aceitar ou não a mutação. Foi essa a escolha deliberada frente à alternativa de compartilhar pastas entre contas, que reescreveria o núcleo de tenancy descrito no `CLAUDE.md` §0.

Três decisões de construção sustentam o resto:

1. **Lookup em mapa, falha FECHADA.** Papel ausente da matriz — ou string que escapou do CHECK — resolve para conjunto vazio: fica impotente, não irrestrito.
2. **Owner é único por índice parcial**, não por disciplina de handler. Dois owners não é estado que código algum deva alcançar, e uma transferência que escrevesse dois momentaneamente corromperia "a conta que não pode ser rebaixada". Por isso a troca é um ÚNICO `UPDATE` com `CASE` sobre os dois ids: o índice é verificado por statement, então promover-depois-rebaixar falharia na promoção e a ordem inversa deixaria a instância sem owner durante a transação.
3. **Remover `RoleUser` transformou cada suposição de 2 papéis em erro de compilação**, em vez de mudança silenciosa de comportamento — a mesma razão pela qual `authctx.UserID` é tipo distinto.

O gate de escrita é montado **por grupo** e é ciente de método em `/links`, `/notes`, `/tags` e `/import`, para que rota mutante adicionada depois nasça coberta. `/folders` e `/backup` ficam de fora de propósito: ambos respondem POST a operações que só LEEM — destrancar pasta prova uma senha para VER o conteúdo, exportar backup serializa linhas que o chamador já possui — e um gate cego por método trancaria o viewer para fora das próprias pastas protegidas.

`RequirePermission` responde **403**, e não o 404 do `RequireAdmin`. Os dois escondem coisas diferentes: `RequireAdmin` oculta que a superfície administrativa existe; passado aquele portão o chamador já sabe que existe, então "seu papel não permite" não vaza nada e é a única resposta que deixa um admin entender por que o botão exclusivo do owner falhou.

Migração 000032 mapeia `user → editor` (capacidade idêntica) e promove o administrador ativo mais antigo a owner. O rollback é **lossy** e está documentado no `.down.sql`: um viewer recupera acesso de escrita, porque o modelo antigo não tem como expressar somente-leitura.

### ADR-34 — Trilha de auditoria administrativa
**Status:** Accepted — implementado em v2.1.0.

Não havia registro de quem alterou papéis, revogou sessões ou emitiu convites — nem de rajadas de login falho. Um incidente terminava em `grep` no log do container, que rotaciona.

Decisão: tabela `audit_log` cobrindo a superfície de identidade (logins e falhas, mudanças de papel/status, convites, recuperações forçadas, edições de política). Conteúdo fica fora de escopo de propósito: já existe uma linha por clique em `click_log`, e misturar as duas soterraria os eventos de segurança que a tabela existe para expor.

Três decisões que valem o registro:

- **`ON DELETE SET NULL`, nunca CASCADE**, e o e-mail é desnormalizado ao lado do id. Apagar uma conta não pode apagar o registro do que ela fez — "uma conta já removida promoveu este usuário" é exatamente a entrada que uma investigação precisa — e depois que a linha some o id sozinho não identifica ninguém.
- **Sem coluna de IP.** `X-Forwarded-For` só é confiável atrás de proxy configurado (ver `trustedProxyRealIP`), então uma coluna de IP seria ao mesmo tempo autoritativa na aparência e controlada pelo atacante num bind direto — a pior combinação para uma tabela que se consulta durante um incidente.
- **A escrita nunca falha a operação.** `Audit` devolve erro para o chamador LOGAR: a ação já commitou, e responder 500 por causa do trail convidaria a um retry que a executaria duas vezes. Perder uma linha é a falha menor, e é visível.

O sucesso de login é gravado em `issueAndRespond` — o ponto único por onde toda via de credencial passa — e não em `Login`: uma senha aceita que desvia para o desafio de 2FA não é um login, e registrá-la como um faria o trail afirmar um acesso que não houve. A falha grava **uma** entrada no ramo que as três causas (endereço desconhecido, senha errada, conta inativa) compartilham; entradas distintas reconstruiriam o oráculo de enumeração que aquele ramo existe para fechar.

### ADR-35 — Política da instância configurável, e a revogação explícita do invite-only
**Status:** Accepted — implementado em v2.1.0.

Piso de senha, validade de OTP e quem pode entrar pelo Google eram constantes no código. Operadores pediam ajuste sem recompilar.

Decisão: `internal/policy`, persistido num único documento JSON em `app_setting`. Pacote folha: `auth` importa `policy` para enforcement e `policy` não importa nada de `auth` — o contrário fecharia o ciclo, e por isso o gancho de auditoria entra como função.

Um documento em vez de uma chave por campo: os valores são lidos juntos em todo login e escritos juntos por um formulário, e linha-por-campo deixaria uma escrita parcial rodando metade da política velha e metade da nova. `app_setting` está fora da superfície de backup nos DOIS sentidos (export não emite, restore ignora desde o snapshot v6), e é isso que impede um zip forjado de reescrever o piso de senha da instância.

**Todo valor tem um PISO que a configuração não cruza**, e o piso é o valor que o código já usava. Instância que nunca abre a tela se comporta exatamente como antes; a que abre não fica mais fraca que essa linha de base. `validatePassword` virou **método** justamente para o compilador arrastar os cinco call sites — como função de pacote continuaria aplicando a constante em silêncio, e política que nada aplica é pior que política nenhuma.

Escrita é do owner, leitura de qualquer admin: um admin precisa ver as regras sob as quais administra, mas um admin que pudesse baixar o piso de senha ou alargar a allowlist do Google baixaria a segurança da instância e entraria pela brecha.

**Este ADR revoga explicitamente a regra invite-only do ADR-31.** `google_auto_provision` cria conta para um endereço Google desconhecido. As salvaguardas são a razão de a revogação ser aceitável:

- **OFF por padrão.** Instância existente não muda de comportamento.
- **Exige allowlist não-vazia.** Lista aberta mais provisionamento é qualquer conta Google virando tenant.
- **O papel padrão é recusado em três camadas** (`Validate`, handler, repositório) e nunca pode ser administrativo: signup self-service não pode chegar com administração.
- **Toda recusa é o MESMO `not_linked`** que endereço desconhecido sempre deu. Uma resposta distinta diria a um chamador anônimo quais instâncias são abertas, ou permitiria enumerar a allowlist um palpite por vez.
- **A allowlist gateia os caminhos que CRIAM acesso** (conversão e provisionamento) e deliberadamente não o login já vinculado — aplicá-la a identidade existente deixaria o owner se trancar para fora salvando uma lista que exclui o próprio domínio, e um owner só-Google não teria segunda porta.
- **O domínio é comparado exato**, nunca por sufixo: `example.com` não pode aceitar `notexample.com`, e subdomínio não é o domínio.

A conta provisionada e sua identidade nascem na MESMA transação, porque o trigger deferido da migração 000021 exige que conta ativa tenha ao menos uma credencial e essa conta nunca terá senha: linha e identidade só são legais juntas. O provisionamento desemboca em `oauthComplete` como qualquer login vinculado, o que mantém a política de segundo fator valendo em vez de abrir a única porta que a pula.

### ADR-36 — E-mail durável: outbox transacional e transporte plugável
**Status:** Accepted — fases 1 e 2 implementadas (outbox + relay `inproc` + templates i18n; transporte AMQP + `cmd/mailer` + escada de retry + dead-letter). Detalhado em [`docs/SDD-EMAIL-ASYNC.md`](SDD-EMAIL-ASYNC.md). Migrações `000034_mail_outbox` e `000035_user_locale`.

A entrega era in-process, efêmera e sem retry: fila de 32 slots em memória, 2 workers, e um `Send` que falha é logado e descartado. `Stop()` cancela o que está em voo em vez de drenar. Restart, deploy ou blip de rede no provedor perdem convite, link de reset e código de login em silêncio — e o usuário vê `202` e espera para sempre.

Decisão: a mensagem é gravada em `mail_outbox` na **mesma transação** que a credencial que ela carrega, e um relay drena a tabela para o transporte configurado.

**Outbox e não publish-após-commit.** O `Reserve()/Publish()/Release()` de hoje existe para reservar a vaga na fila ANTES de persistir a credencial: fila cheia significa reset não criado, em vez de token que nunca será enviado. Publicar no broker depois do `COMMIT` reabre exatamente esse buraco, e pior — o cooldown de 60 s já foi cobrado, então o usuário não consegue nem pedir outro. O `INSERT` no outbox participa da transação já aberta: não falha por capacidade, não se perde, e ou os dois existem ou nenhum existe. É **mais forte** que o invariante que substitui, não apenas compatível: hoje fila cheia derruba a operação; com outbox ela sempre acontece e a entrega vira garantida-eventual. Como efeito, a coreografia de `defer Release()` sai dos três handlers que a carregam.

**O payload é cifrado, e isso não é opcional.** O token cru de reset existe hoje apenas dentro do corpo do e-mail — o banco guarda só `sha256`, pela mesma razão que sessões são `sha256`: um `pg_dump` não pode ser um kit de sequestro. Gravar o link em texto destruiria essa propriedade, e o broker é pior ainda, porque persiste mensagem durável em disco num vhost possivelmente compartilhado. `payload_{ciphertext,nonce}` guardam AES-256-GCM de `secrets.Cipher`, sob uma SUBCHAVE derivada de `AUTH_ENCRYPTION_KEY` (`secrets.NewDerivedCipher`, propósito `foldex/mail-outbox/payload/v1`) e não sob a chave mestra — o seed TOTP já cifra com ela, e este é o único domínio cujo volume não é limitado por nada, então compartilhar a chave transformaria duas distâncias independentes até o limite de aniversário do GCM num orçamento só. É o mesmo padrão que os MACs de código já usam; o MESMO blob é o corpo AMQP, então o broker nunca vê credencial em claro. GCM e não CTR pela tag de autenticação: sem ela, escrita no banco ou no broker vira ataque de substituição do link de destino, e a vítima veria apenas um e-mail legítimo apontando para o lugar errado.

**Rabbit é transporte, não fila de origem.** O outbox faz o que o broker não consegue (atomicidade com a transação); o broker faz o que o outbox não faz bem (retry com backoff, dead-letter, consumidores escaláveis independentes do processo que serve HTTP). `MAIL_TRANSPORT=inproc` faz o próprio relay renderizar e enviar em processo (o `mailer.Dispatcher` que o SDD propunha como sink NÃO foi implementado — ver o parágrafo seguinte) e é o default, então o self-hosted de binário único continua subindo sem broker nenhum; `amqp` liga a topologia e o worker `cmd/mailer`. Backoff por TTL de fila e não por `sleep` no consumidor — `sleep` seguraria o prefetch e trocaria latência por perda de vazão. O worker não recebe credencial de Postgres: ele é o processo que decifra credenciais, e não precisa de banco para isso.

**TLS para broker privado se configura NA URL, e isso nunca esteve documentado.** O guard que recusa `amqp://` contra host não-loopback empurra o operador para `amqps://`, e num broker de intranet — endereçado por IP, com cadeia autoassinada ou de CA interna — nenhuma raiz pública o avaliza. A conclusão natural é que o transporte AMQP é inalcançável nesse cenário, que é justamente o que o ADR-12 chama de uso primário do foldex. **Não é.** A URI AMQP carrega parâmetros de TLS que o `amqp091-go` honra quando `TLSClientConfig` é `nil` (`connection.go:324`): `?cacertfile=` substitui as raízes do sistema, `?certfile=`/`?keyfile=` fazem mTLS e `?server_name_indication=` ajusta o SNI. Um `AMQP_URL=amqps://user:senha@10.0.0.5:5671/foldex?cacertfile=/etc/foldex/certs/ca.pem` funciona sem uma linha de código. O que faltava era dizer isso em algum lugar. **E há uma armadilha para quem tentar 'consertar' com uma variável de ambiente:** passar um `*tls.Config` não-nil faz a biblioteca PULAR `tlsConfigFromURI` inteiro, desligando em silêncio o mTLS e o SNI de quem já os usava — uma variável `AMQP_CA_FILE` bem-intencionada duplicaria `?cacertfile=` e quebraria os outros dois. Sob Docker os caminhos resolvem DENTRO do container, então o compose monta `./certs` em `/etc/foldex/certs` no backend e no mailer; um certificado emitido para um IP precisa de SAN de IP, e o SNI muda qual nome é conferido, não se o endereço está no certificado.

**O guard de texto claro passou a distinguir a internet de uma LAN.** A recusa original de `amqp://` contra host nao-loopback tratava "broker remoto" como uma coisa so, e o custo disso caiu inteiro sobre o caso mais comum do foldex: um RabbitMQ na rede local, sem TLS, onde o operador tem razao em dizer que nao ha problema. `AMQP_ALLOW_PLAINTEXT` (default `0`, comportamento anterior intacto) abre essa porta -- **mas verifica a afirmacao em vez de aceita-la**. O flag e uma declaracao sobre a REDE, entao e conferido contra a rede: cobre RFC1918, CGNAT (100.64/10, onde Tailscale e similares operam), loopback e link-local, e um endereco PUBLICO literal segue recusado mesmo com ele ligado. Nenhuma declaracao torna `203.0.113.4` interno, e a forma realista de acabar apontando para um endereco publico e errar um octeto -- que sem esse ramo publicaria a senha do broker para um estranho, em silencio, a cada mensagem. A primeira versao aceitava um HOSTNAME na palavra do operador, e a revisao de seguranca derrubou isso: nome e a forma MAJORITARIA de enderecar broker (o proprio nome de servico do compose), sua resolucao pertence a uma infraestrutura que o operador nem sempre controla, e a decisao de confianca era tomada no boot enquanto o dial acontece indefinidamente depois. A verificacao passou a ter DUAS pernas — no boot para literal de IP, e no dial contra o peer efetivamente alcancado (`requirePrivatePeer`), fechando a conexao antes de o handshake AMQP escrever a credencial SASL PLAIN e falhando fechada quando o endereco nao e um `*net.TCPAddr`. E a mesma forma que `preview.safeDialer` usa para SSRF, pelo mesmo motivo: um nome nao e um endereco. De quebra cobre o literal que `net.ParseIP` recusa mas o resolvedor aceita (`3221225985` disca `192.0.2.1`). **O que o relaxamento expoe**, e que a doc do operador precisa dizer inteiro: a credencial do broker, os metadados de roteamento, e o DESTINATARIO mais o TEMPLATE de cada mensagem — isto e, quem esta recebendo qual credencial e quando; o corpo permanece selado. **O predicado e proprio, e nao `netpolicy.IsPrivateIP`** -- aquele existe para bloquear SSRF e por isso trata o registro de uso especial da IANA inteiro como proibido, faixas de documentacao incluidas; sob ele `203.0.113.4` lê como privado, o que e correto para um fetcher que se recusa a visita-lo e errado aqui, onde a pergunta e se a credencial pode cruzar o link em claro.

**O que a fase 1 mudou em relação ao desenho original.** O SDD propunha o `mailer.Dispatcher` como sink do transporte `inproc`. Isso não foi implementado, e a razão é a promessa da própria fase: o dispatcher **descarta** um `Send` que falha, então um PR1 apoiado nele entregaria durabilidade apenas até a primeira recusa de SMTP. O relay passou a ser o próprio worker do transporte `inproc` — ele envia, marca `published` só no sucesso, e no fracasso reagenda por `next_attempt_at` com backoff escalonado (1 min → 5 → 15 → 30 → 60) até esgotar `max_attempts`. O resultado é que **retry, backoff e dead-letter existem sem broker nenhum**, que é exatamente o que a fase prometia entregar. O `Dispatcher` foi removido: mantê-lo ao lado do relay seria código morto com aparência de camada. `MAIL_TRANSPORT` continua sendo assunto da fase 2.

Duas garantias operacionais vêm de colunas, não de disciplina. O `claim_token` (CAS na liquidação) impede que um relay que dormiu além do TTL de claim sobrescreva o resultado de outro que já refez o trabalho; e o backoff é uma coluna porque um `sleep` no worker seguraria o slot e transformaria um destinatário lento em fila parada para todo mundo. Falha permanente — `unknown_template`, `undecryptable_payload` — liquida na hora em vez de gastar seis tentativas no que não pode passar a funcionar.

**O force-reset administrativo passou a ser assíncrono (§12.1, aprovado).** O `503 mail_unavailable` deixou de existir: token e mensagem commitam juntos e a entrega é garantida-eventual como todo o resto. A propriedade que motivava o envio síncrono — *um administrador nunca instala uma credencial que o alvo não recebe* — é preservada por **durabilidade** em vez de acoplamento. Junto saiu a re-verificação de elegibilidade pós-envio, e a ausência dela é o ponto: ela defendia uma janela que só existia porque o envio bloqueava dentro da transação, e a linha de `app_user` agora fica travada `FOR NO KEY UPDATE` da leitura até o commit. A exigência de `MAIL_DRIVER=smtp` permanece, porque o driver `log` imprimiria essa credencial no stdout.

**Fase 2 — o que a topologia AMQP resolve, e o que ela custa.** O sink `amqp` publica o **mesmo blob selado** que a linha guarda, com publisher confirms obrigatórios: sem confirm, publicar é fire-and-forget fantasiado de durabilidade, e o relay marcaria `published` com base numa escrita em socket. A escada de retry é **uma fila por degrau** (`.retry.1m`/`.5m`/`.30m`) e não um TTL por mensagem numa fila só, porque o RabbitMQ expira apenas a partir da CABEÇA: uma mensagem de 30 minutos na frente seguraria todas as mais curtas atrás dela. E o worker **republica explicitamente** em vez de dar nack, porque o nack roteia pela `dead-letter-routing-key` fixa da fila, que não sabe dizer "espere um minuto desta vez, meia hora na próxima"; o custo é que um crash entre o publish e o ack reentrega uma mensagem que já havia FALHADO, que é a direção inofensiva.

O custo real da fase 2 é **semântico**: entregar ao broker move a verdade sobre a entrega para fora do banco, e `published` passaria a significar apenas "o broker aceitou". Sem nada a mais, um link de reset que morresse no último degrau deixaria a linha lendo `published` para sempre. Quem fecha isso é o `DeadLetterWatcher`, e ele roda no **backend**, não no worker: consome `foldex.mail.dead`, lê um id e uma razão normalizada — ambos viajam FORA do blob cifrado — e chama `MarkDead`. Assim o relatório nunca precisa da chave, e o worker continua sendo o único processo que decifra, sem nenhuma credencial de Postgres (`config.LoadMailer` existe exatamente para ele subir sem `DB_URL`).

**Fase 2, o que faltou: a metade que consome não tinha voz.** O transporte foi dado por pronto com uma evidência que provava só metade — o exchange `foldex.mail` passou a existir no broker. Quem declara o exchange é o **publisher**; aquilo atestava que o backend publicava, nunca que alguém consumia. Numa instância real o serviço `mailer` (que carrega `profiles: ["amqp"]`) nunca subiu, e o resultado é o pior formato possível de falha: publish roteável e confirmado, linha do outbox liquidada como `published`, fila enchendo, e o primeiro sinal sendo um usuário dizendo que o e-mail não chegou. O que tornou o diagnóstico impossível é que o worker **não logava nada** no sucesso: `mailer ready` e mais nada é indistinguível entre ter drenado cem mensagens e ter ficado parado o dia inteiro.

A correção é de observabilidade, e é deliberadamente barata. `Topology.Declare` passou a devolver o `SendQueueState` da fila de envio — o broker informa a contagem de consumidores de graça em todo declare — e o `AMQPSink` avisa quando conecta numa fila que ninguém lê. Ler do declare, e não de um `QueueDeclarePassive` por publish, é o que mantém o custo em zero round-trips; o limite honesto disso é que a contagem é um **retrato tirado na conexão**, não uma vigilância: um worker que morre enquanto o sink mantém uma conexão saudável só aparece na próxima reconexão. Um probe contínuo custaria um round-trip por mensagem para observar algo que a profundidade da fila também mostra, e o caso que ele cobriria mal é justamente o menos frequente. Do outro lado, `mailworker.handle` registra um INFO por envio bem-sucedido com `outbox_id` e `template` — e **nunca o destinatário**, porque o `logsafe` redige a chave `email` e não `recipient`, e este é o único processo que também segura um link de reset vivo.

**§12.3, o que o `app_user.locale` não cobre.** A migração 000035 resolveu o caso de uma mensagem disparada por OUTRO ator (o convite que saía no idioma do admin). Ela não cobre a conta que nunca escolheu: aí a resolução cai no `Accept-Language`, e esse header é uma configuração do browser SEPARADA da que decide o idioma da interface (`navigator.language`, mais a escolha do seletor, guardada por dispositivo). Um Chrome exibindo o foldex em português e enviando `Accept-Language: en` é configuração comum, e mandou um link de redefinição em inglês para quem via tudo em português.

Escrever o idioma exibido NA conta foi implementado e descartado: `""` é valor selecionável no Perfil (*seguir meu navegador*) e o schema não o distingue de *nunca escolheu*, então uma escrita não solicitada no mount tornava a opção inoperante no instante em que era salva. O que ficou é menor: os fluxos ANÔNIMOS carregam uma dica explícita — `POST /api/auth/password/forgot` aceita um `locale` com o idioma que a SPA está exibindo — e `localeForHinted` a classifica exatamente onde o header já estava, ABAIXO da preferência do destinatário. Essa ordenação é todo o argumento de segurança num endpoint não autenticado: nomear um idioma só escolhe a redação de uma mensagem que o próprio chamador causou, para um endereço que ele não controla, numa conta que nunca declarou preferência; valor não reconhecido cai no header, sem ser guardado nem ecoado.

Da tentativa descartada sobrou uma correção que vale por si: `UpdateOwnProfile` escrevia `name` incondicionalmente, então qualquer escrita de idioma replayava o nome que aquele cliente tinha em cache e revertia um rename feito em outra aba — o espelho exato do risco que o campo `locale` já evitava viajando só quando muda. `name` virou tri-state e o cliente ganhou `auth.updateLocale`, que não manda nome nenhum.

**Um layout por mensagem, e as três formas saíram do conteúdo.** A primeira implementação colapsou as onze mensagens num `layout.html.tmpl` com campos opcionais — funcionava, e não diferenciava nada: um código de entrada e um alerta de sessão encerrada chegavam com a mesma cara. O SDD já previa arquivos por mensagem; o código é que tinha divergido. Agora `chrome.html.tmpl` guarda só os blocos sem significado próprio (casca, marca, botão, caixa de URL, bloco de código, nota) e cada mensagem tem o seu arquivo, em três formas: **ação** (botão mais a URL por extenso, que é o que deixa conferir o host antes de clicar e o que mantém a mensagem utilizável num cliente que não renderiza botões), **código** (os dígitos ANTES do texto — um código de entrada é lido e redigitado em dez segundos, e obrigar a varrer um parágrafo antes cobra do leitor a única coisa que ele abriu a mensagem para pegar) e **aviso** (sem slot de botão).

A forma "aviso" transformou uma disciplina em estrutura — **nos dois braços**, e a primeira tentativa acertou só um, o que é pior do que não tentar. O HTML foi quebrado em formas enquanto o arm de texto continuou um layout único com slots opcionais: um `ActionURL` injetado saía como URL crua, sob um dois-pontos órfão sem rótulo, duas linhas acima da nota de rodapé que promete que a mensagem não traz link. Todo cliente de texto transforma um `https://` nu em clicável, e quem recusa HTML vê só esse braço. Condicionar cada slot no seu próprio campo também foi tentado e recusado: conserta o campo em que você pensou, e aquela passagem deixou o `Code` vazando. **A FORMA é a garantia** — `chrome.shape_notice` e `text.shape_notice` não têm botão, caixa de URL nem bloco de código, e uma forma sem slot não vaza o que não tem. O teste injeta `ActionURL` E `Code` nos três idiomas e afirma sobre os dois braços; o contraponto positivo é obrigatório junto, porque sem ele `NotContains(HTML, "<a ")` é satisfeito por uma árvore onde nenhuma mensagem renderiza âncora — e apagar o botão do `password_reset` fazia exatamente isso, verde.

**Um layout nunca pode `{{define}}` um bloco compartilhado.** O `Parse` do `text/template` SUBSTITUI em silêncio e o `ParseFS` percorre o glob em ordem, então um `chrome.button` colado em qualquer arquivo que ordene depois do `chrome.html.tmpl` reescreve o bloco para as onze mensagens: o raio de alcance medido foi um e-mail de reset sem âncora nenhuma, suíte verde, e a credencial existe naquela mensagem e em lugar nenhum mais. `refuseDuplicateDefinitions` parseia cada arquivo SOZINHO e recusa o boot nomeando os dois. A checagem de paridade não enxerga isso — o `Lookup` continua resolvendo. Os blocos são prefixados `chrome.` pelo motivo espelhado: sem prefixo, uma mensagem futura chamada `body` ou `heading` passaria na paridade e sairia como um fragmento `<tr>` no lugar do e-mail inteiro.

Os onze arquivos DELEGAM às três formas em vez de reescrevê-las — três eram byte-idênticos antes disso — e acento/tint são dados, não estrutura. Uma mensagem diverge deixando de delegar.

`render` seleciona com `ExecuteTemplate(env.Template, doc)` e `loadAssets` **recusa o boot** quando uma entrada do catálogo não tem layout: os assets são embutidos, então divergência é binário publicado errado e não condição de runtime. O arm de texto permanece ÚNICO — texto puro não tem layout para diferenciar — e continua obrigatório. Nenhum arquivo do diretório pode começar com `_`, porque a forma de diretório do `//go:embed` descarta esses nomes e levaria o chrome junto com as onze mensagens. Rótulo (`eyebrow`) e assinatura são COPY, nos catálogos, a assinatura sob a chave reservada `_footer`.

O acoplamento entre `MAIL_TRANSPORT=amqp` e o profile do compose virou `COMPOSE_PROFILES` no `.env`, imediatamente acima de `MAIL_TRANSPORT`, porque nada no sistema fazia os dois concordarem.

### ADR-37 — E-mail como segundo fator permanente, e não escape de um desafio TOTP
**Status:** Accepted — detalhado em [`docs/SDD-EMAIL-ASYNC.md`](SDD-EMAIL-ASYNC.md). Migração `000036_email_second_factor`.

OTP por e-mail existia só como escape dentro de um desafio que já era TOTP: `emailFactorAvailable` exige `purpose == PurposeTOTP`, e o desafio só nasce `totp` quando a conta já tem autenticador confirmado. Conta sem TOTP nunca recebe desafio, então `/2fa/email` era inalcançável para ela. Quem não quer instalar um autenticador não tinha segundo fator algum.

Decisão: `email_factor` com o mesmo formato de `totp_secret`, e `has_second_factor = totp_enabled OR email_2fa_enabled` substituindo `totp_enabled` como a noção de "esta conta tem segundo fator".

A forma espelha `totp_secret` deliberadamente: o binding de época (`enrollment_token_version`, `enrollment_session_id`) e o `CHECK ... NOT VALID` são os mesmos que a migração 000025 aplicou ao TOTP, então os padrões de confirmação sob lock transferem sem invenção — um fator novo com esquema próprio de binding seria um fator novo com bugs novos. Não há segredo a guardar: o "seed" do fator e-mail é o endereço, que já está em `app_user.email`; a tabela é marcador de cadastro, não cofre. As três noções permanecem derivadas por `EXISTS` pelo motivo de sempre — booleano armazenado precisaria de atualização em quatro lugares, e a direção da discordância decide se o login exige um código que o usuário não consegue produzir.

O purpose continua se chamando `totp` (o CHECK é fechado e renomear custaria migração por cosmética) mas passa a significar "deve um segundo fator"; qual método satisfaz é decidido pela lista `methods`, que o frontend já consome.

**O invariante que NÃO muda é o que mais importa: um canal nunca satisfaz os dois fatores.** `mailbox_already_proven` continua sticky e continua recusando o fator e-mail em desafio nascido de reset de senha, senão controlar a caixa postal viraria takeover completo. A consequência nova é de disponibilidade, não de segurança: uma conta cujo único fator é e-mail, entrando por link de reset, fica sem método de e-mail. Por isso o cadastro do fator e-mail **obriga a emissão de códigos de recuperação**, igual ao TOTP — sem eles o guard de segurança viraria um bug que tranca o usuário fora da própria conta.

Para administrador, `RequireAdmin` passa a checar fator confirmado de qualquer tipo. Um admin cujo único fator é e-mail é mensuravelmente mais fraco, porque a caixa postal já é o canal de recuperação e concentrar os dois reduz a superfície que o atacante precisa comprometer — então isso não vira constante, vira `instance_policy.admin_second_factor ∈ {any, totp_only}` com piso no comportamento permissivo e o owner podendo apertar, no mesmo formato dos demais pisos do ADR-35. A chave ausente num documento salvo antes desta release decodifica para `""` e recebe o piso: validação estrita ali teria recusado **toda** escrita de política numa instância existente, por um campo que o owner nunca tocou.

**Remover um fator é decisão de UM lugar.** `mayRemoveFactor` responde "a conta continua conforme sem este fator?" e é consultada pelos dois endpoints de disable **e** pelo `GET /2fa`, que devolve `can_disable_totp`/`can_disable_email`. Duas cópias da regra divergem na direção que ninguém percebe: tela que esconde um botão que o servidor permitiria parece funcionalidade faltando, e tela que mostra um botão que o servidor recusa parece defeito na conta. Sob `admin_second_factor = any`, um admin com os dois fatores pode largar um — recusar de saída trataria "tem dois fatores" como mais restrito que "tem um". Desligar o TOTP só apaga os códigos de recuperação quando **nenhum** fator sobra, lido sob o lock de `app_user` que a transação já segura; incondicional, deixava uma conta com fator e-mail e sem saída de lockout — porque o guard do link de reset recusa justamente o e-mail.

**Step-up de sessão aceita os três métodos, e o proof é VERIFICADO sem ser gasto.** Os quatro caminhos autenticados por sessão (desligar fator, regerar códigos, definir senha, vincular identidade) aceitavam só TOTP: uma conta cujo único fator é e-mail não conseguia sequer definir uma senha. `POST /2fa/email/send` emite um código de step-up sob purpose próprio (`step_up_2fa`) — separado de `enroll_email_2fa` porque um prova uma caixa que ninguém aceitou ainda e o outro é um fator já aceito se apresentando; purpose compartilhado deixaria o primeiro ser gasto como o segundo. O discriminador de seis dígitos **cai através** de TOTP para e-mail em vez de encadear `else if`: as duas formas são indistinguíveis, e comprometer com o ramo TOTP tornaria o fator e-mail inútil exatamente para quem não tem autenticador. O ramo de e-mail exige `Email2FAEnabled` — aceitar código enviado a uma conta que nunca cadastrou o fator deixaria o controle da caixa postal sozinho autorizar a remoção de um autenticador. E `SecondFactorProof` é consumido **dentro da transação** da operação que autoriza, para os três métodos: um código de recuperação queimado por uma escrita que falhou custa ao usuário uma saída de lockout para uma operação que não aconteceu.

### ADR-38 — /metrics Prometheus atrás de bearer token, com labels imunes a cardinalidade

O backend passa a expor `GET /metrics` (pacote `internal/metrics`): contadores e histograma de requisições HTTP, gauge de in-flight, estatísticas do pgxpool e coletores de runtime. Três decisões carregam o ADR:

1. **Token obrigatório, sem modo aberto.** Métricas entregam o mapa de rotas, o volume de tráfego e o dimensionamento do pool — reconhecimento pronto numa instância multiusuário. `METRICS_TOKEN` vazio responde 503 (desabilitado); comparação em tempo constante; scrapes concorrentes limitados a 1 com timeout de 10s. O nginx não ganha `location /metrics` de propósito: o scrape é da stack central de observabilidade, direto na porta do backend.
2. **Labels só de valores limitados.** O middleware roda ANTES do auth stack (para contar 401/403 de verdade), então todo label é hostil por padrão: a rota é o padrão registrado do chi (nunca a URL crua — slug/id de tenant não vira série), método fora do conjunto padrão colapsa em `other` (client_golang nunca poda séries; método é controlado pelo cliente), não-roteado vira `unmatched`.
3. **O wrapper de status preserva a cadeia `Unwrap()`.** `Instrument` usa o `WrapResponseWriter` do chi, o mesmo do `slogRequest` — um wrapper caseiro sem `Unwrap` quebraria silenciosamente o `http.NewResponseController` que `extendArchiveDeadlines` (backup) usa para esticar deadlines de streams multi-GB, cortando-os no WriteTimeout default. Achado pelo sweep de agentes antes do merge; travado por teste (`TestInstrumentPreservesResponseControllerDeadlines`) e pelo tripwire de ordem de middleware (`TestRecoveredPanicIsCountedAsInternalServerError` — Instrument fora do Recoverer, senão panic recuperado não conta como 500).

`/metrics` responde `http.Error` em texto plano, não o envelope JSON do §7 — desvio deliberado: o consumidor é o Prometheus, e `healthz` já é igualmente ad-hoc.

### ADR-39 — Tracing distribuído OpenTelemetry opt-in por env, com nomes de span imunes a cardinalidade

**Contexto.** A stack de observabilidade central (app-deployments/observability) ganhou visão APM alimentada pelo metrics-generator do Tempo — mas só apps que EMITEM traces OTLP aparecem lá. O backend expunha métricas (ADR-38) e logs, porém nenhum trace: sem spans não há service graph, nem correlação log↔trace, nem RED por endpoint derivado de traces.

**Decisão.** Pacote `internal/tracing` com três peças, todas opt-in por `OTEL_EXPORTER_OTLP_ENDPOINT` (vazio = provider global fica o no-op do OTel; nenhum exporter, buffer ou goroutine):

1. **`Setup`** instala o tracer provider global exportando OTLP/gRPC (esquema `http://` = plaintext, `https://` = TLS; path/barra final é aparado — hábito do OTLP/HTTP que faria o dial gRPC falhar em silêncio para sempre). A conexão gRPC é lazy e falha de setup vira `Warn`, nunca fatal — telemetria fora do ar não derruba a aplicação. `Setup` roda ANTES de `db.New`, porque o tracer do pgx resolve o provider por query. O sampler é `ParentBased(RootServerOnly)`: root span só é amostrado se for SERVER — sem isso, cada `pool.Ping` do healthz (probe do compose, a cada poucos segundos, para sempre) e cada query de worker de background viraria um trace-órfão de um span.
2. **`Middleware`** cria um span SERVER por request, nomeado pelo PADRÃO de rota do chi resolvido DEPOIS do handler (`GET /api/links/{id}`) — mesma regra anti-cardinalidade/anti-vazamento do ADR-38: URL crua com ids/slugs nunca vira nome ou atributo de span (testado por asserção negativa), e método fora do conjunto padrão colapsa em `_OTHER` (r.Method é token controlado pelo cliente — mesma regra do `metricMethod`). 5xx marca status Error; 4xx não (problema do cliente). `/healthz` e `/metrics` não geram span. **Contexto de trace de entrada é DESCARTADO, não honrado**: este serviço é a borda (nada acima traceia), então um `traceparent` do cliente só serviria para escolher nossos trace ids e o flag de sampling — permitindo a um atacante excluir os próprios requests da telemetria (`sampled=0`) ou enxertar spans no trace de uma vítima, poluindo investigação forense. Todo request re-origina o trace aqui; reavaliar se um gateway confiável com tracing um dia ficar na frente.
3. **Correlação**: `slogRequest` acrescenta `trace_id` à linha de acesso quando há span válido (elo do derived field Loki→Tempo no Grafana), e `db.New` instala `otelpgx` — spans CLIENT por query (nome = primeira palavra-chave do SQL; parâmetros nunca; **texto SQL desabilitado por inteiro** via `WithDisableSQLStatementInAttributes`, porque schema/formas de WHERE não devem cruzar o fio até um collector possivelmente plaintext na LAN) que desenham a aresta backend→Postgres no service graph.

O middleware monta via `Deps.Trace` (nil = off, o zero-value de todos os testes de router) — espelho exato do padrão `Deps.Metrics`. A ordem Trace-fora-do-Recoverer e o `span.End` deferido são travados por teste de caracterização de panic (`TestPanicIsRecordedAsErrorSpanWithRoute`), e o contrato de export de `Setup` (formas de endpoint, modo TLS, identidade do resource, sampler, flush no shutdown) é provado contra um collector OTLP/gRPC fake in-process — o sweep de mutação mostrou que asserções que param em `err == nil` deixam TODOS esses mutantes vivos.

**Consequências.** Com a env apontada para o Tempo da stack central, o backend aparece no APM Overview (RED por rota), no service graph (incluindo a aresta para o Postgres) e cada linha de log de acesso vira porta de entrada para o trace correspondente. Sem a env, o custo por request são dois lookups de contexto no no-op. Sem propagação de entrada, chamadas de um futuro cliente instrumentado não se juntam ao trace dele — trade-off deliberado registrado acima. O mailer segue sem traces — consumidor AMQP sem request HTTP; instrumentá-lo é trabalho futuro se a fila de mail precisar de spans.

**Emenda — identidade no span (`span.user.id`).** Traces por rota respondem "o que está lento"; não respondem "para quem". `tracing.AnnotatePrincipal` carimba no MESMO span SERVER o `user.id` (id numérico opaco — `span.user.id` no TraceQL), `user.roles` e `foldex.auth.via` (`session` vs `api_token`).

**É uma FUNÇÃO chamada nos três seams onde um principal nasce** — `auth.Middleware.Authenticate`, `auth.Middleware.Optional` e o bootstrap de `AUTH_ENABLED=0` — e o primeiro rascunho é a razão de ser assim. Ele era um MIDDLEWARE montado no grupo `/api`, e perdeu em silêncio toda a metade autenticada de `/api/auth` (sessões, troca de senha, 2FA, tokens de API): exatamente a superfície de gestão de credencial que um operador mais quer atribuída. Nada falhou — sem erro de build, sem panic, resposta idêntica. Um mount anota o grupo em que foi montado; um seam anota toda identidade que existe. O span abre vários middlewares acima e seu `End` é deferido lá, então ele continua mutável em todos os três pontos de chamada.

Isso põe `auth` → `tracing` → `authctx` no grafo, acíclico. A alternativa estrutural — anotar dentro do próprio `authctx.WithPrincipal`, impossível de esquecer — foi recusada: `authctx` é uma folha que importa só `context`, e puxar OTel para lá o colocaria em todo pacote de repositório.

O conjunto de atributos de identidade é FECHADO e testado por asserção negativa: nada de e-mail, nada de nome de exibição, nada de `session_id`. Um store de traces é outro domínio de retenção que o banco — controle de acesso próprio, cópia em todo backend que o consome — e um id opaco não vale nada para quem já não consegue ler `app_user`. É o mesmo raciocínio que mantém path cru fora de nome de span. Request sem autenticação não carrega `user.id` nenhum, nunca `"0"`.

Os guards são de integração através de sessão real (`TestAuthenticate_StampsUserIDOnSpansOfTheAuthSurfaceItself`, `TestOptional_...`) e do `server.New` real (`TestUserIDIsStampedOnRequestSpansThroughTheRealRouter`), porque teste unitário compondo a cadeia à mão sobrevive tanto a desmontar quanto a mover o ponto de anotação. Três mutantes foram mortos por eles.

Cardinalidade alta é CORRETA num atributo de span e errada como dimensão de métrica: `user.id` no processor de span-metrics do Tempo cunharia uma série temporal por conta. O RED dos dashboards continua derivando de `http.route`.

**Consequência de segurança registrada em INV-170:** o exporter fala gRPC em texto claro e sem autenticação salvo endpoint `https://`, e o sampler grava todo span SERVER — então identidade atravessa o fio a cada request. Acesso de LEITURA ao store de traces passa a ser privilégio administrativo da instância: enumera quem é `owner`/`admin` e perfila atividade por conta, sem nenhuma permissão do Foldex.

## Future considerations

- ~~**Auth + multi-user.**~~ → em execução: ADR-30/31/32 + [`docs/SDD-AUTH-RBAC.md`](SDD-AUTH-RBAC.md).
- **Sync entre máquinas.** Hospedar Postgres remoto, ou criar `foldex-sync` que replica via litestream.
- **AI suggestions.** Sugerir tags ao criar (LLM lê título + descrição), agrupar duplicatas.
- **Favicon cache local.** Worker baixa e armazena em volume; resolve broken icons offline/VPN.
- **Public sharing.** Sub-set de links visível sem auth (read-only link de partilha).

### ADR-40 — Conta criada por administrador, com senha escolhida por ele

**Contexto.** O dono da instância pediu "adicionar usuário" na tela de administração e,
apresentada a alternativa, escolheu explicitamente a criação direta **com senha
temporária** em vez do convite. Isso contraria o invariante que o resto de `internal/auth`
sustenta e que o CLAUDE.md §4 declara: *um administrador nunca escolhe, instala ou recebe
a credencial de outro usuário*.

**Decisão.** `POST /api/admin/users` cria a conta ATIVA com `password_hash` derivado da
senha digitada pelo administrador. A rota é montada sob `PermRolesAssign` — a mais estrita
das duas permissões de escrita — porque atribui papel na mesma requisição, igual ao PATCH.

**O que a exceção custa, dito por extenso.** Entre a criação e a primeira troca de senha
pelo dono da conta, duas pessoas conhecem uma credencial, e **nenhuma entrada de auditoria
consegue distinguir** um acesso feito pelo dono da conta de um feito pelo administrador que
digitou a senha dela. O convite, o link de redefinição e o `force-password-reset` existem
justamente para não abrir essa janela e continuam sendo o caminho recomendado — com uma
ressalva que o próprio sweep pegou nesta ADR: **`force-password-reset` NÃO alcança as contas
criadas por aqui**, porque ele exige `email_verified_at` não-nulo (`repository_2fa.go`), e
esta rota deixa o endereço não verificado de propósito. O caminho de recuperação que
funciona nelas é o `/password/forgot` que a própria pessoa dispara. O diálogo avisa o
administrador sobre a senha compartilhada: o dono aceitou a troca sabendo dela, o próximo
administrador que abrir a tela não.

**Duas consequências que ficam em aberto, declaradas em vez de escondidas.** (1) **Nada
fecha a janela.** Não há `must_change_password`, expiração de senha nem aviso no primeiro
login, então "até a primeira troca" é indefinido na prática — uma rotação forçada no
primeiro acesso é ortogonal a "entregar a credencial em mãos" e continua sendo a melhoria
óbvia. (2) **Um erro de digitação no endereço cria uma conta ativa cujo dono legítimo nunca
fica sabendo**, porque nenhuma mensagem é enviada; quem controla a caixa digitada por engano
pode tomá-la pelo `/password/forgot` sem jamais conhecer a senha que o administrador
escolheu. O `email_verified_at` nulo impede que o endereço seja tratado como confirmado, mas
não bloqueia o e-mail de redefinição.

**O que NÃO foi abandonado.**

- O **piso de senha configurado** (ADR-35) se aplica. `AdminHandler.validatePassword` é um
  MÉTODO pelo motivo de sempre — como função de pacote, continuaria aplicando a constante
  em silêncio enquanto o dono acreditasse que seu piso valia. `WithPolicy` injeta o leitor;
  política nula roda o piso compilado, que é a direção segura.
- O endereço nasce **NÃO VERIFICADO** (`email_verified_at` NULL). Marcá-lo como verificado
  faria um erro de digitação virar uma caixa confirmada que ninguém controla.
- O papel é recusado no handler **e** no repositório e nunca pode ser `owner` — o único
  papel que não pode ser rebaixado segue alcançável só por transferência explícita.
- A senha nunca chega à trilha de auditoria: `user.created` grava endereço e papel. O
  `logsafe` redige por CHAVE de atributo, então um valor passado como detalhe de auditoria
  seria escrito em claro.

**Alternativas descartadas.** Criar a conta `pending` e disparar o e-mail de definição de
senha respeitaria o §4 na íntegra — foi oferecido e recusado, porque exige SMTP funcionando
e não serve ao caso de entregar a credencial em mãos. Renomear o convite para "adicionar
usuário" também foi oferecido e recusado pelo mesmo motivo.

**Travas.** Nove testes de integração `TestAdminCreateUser_*`: login imediato, endereço não
verificado, recusa de `owner`, default de menor privilégio, piso de senha, endereço
duplicado, e-mail inválido, auditoria sem a senha, e 404 para não-administrador.

### ADR-41 — Username opcional como segundo identificador, e troca de e-mail em duas etapas

**Contexto.** Duas coisas que a conta não podia fazer: entrar com qualquer coisa que não
fosse o e-mail, e trocar o e-mail. A segunda era um invariante declarado — o CLAUDE.md §5
dizia *"e-mail é identidade e nunca é editável inline"* — mas a razão daquela frase é que
trocar o identificador exige um fluxo de verificação próprio, não que a troca seja proibida.
O dono da instância pediu as duas.

**Decisão 1 — username OPCIONAL (mig 000037).** `app_user.username` +
`username_normalized`, nuláveis, com índice único parcial sobre a coluna normalizada. Não há
backfill: gerar `valmir.justo` a partir de `valmir.justo@…` publicaria metade da caixa
postal sob um nome que o dono nunca escolheu. Uma conta sem username entra exatamente como
antes.

**O `@` proibido é a metade que sustenta a decisão.** O login resolve UM identificador
contra as duas colunas na mesma instrução (`email_normalized = $1 OR username_normalized =
$1`), então um username com forma de endereço viveria no mesmo espaço de nomes das caixas de
todo mundo: bastaria reivindicar `vitima@example.com` como username para que as tentativas
de senha daquela conta chegassem na sua. O `CHECK app_user_username_shape` recusa no banco e
`NormalizeUsername` recusa no handler — as duas pontas, porque um handler é um caminho de
código e o próximo a escrever nessa coluna teria de lembrar.

**Uma instrução, não um ramo.** Decidir "parece um e-mail?" antes de consultar produziria
dois tempos de resposta diferentes, que é exatamente o oráculo de enumeração que o resto do
caminho de login existe para fechar (bcrypt sempre roda, um único 401, piso de 250 ms, e o
contador incrementa também para endereços desconhecidos).

**O orçamento de tentativas passou a ser resolvido antes de ser cobrado.** Chaveado pela
string digitada, um atacante alternaria endereço e username da mesma conta e teria o dobro
das tentativas enquanto o teto por conta continuaria marcando cinco.
`Repository.loginBucketKey` resolve o identificador primeiro e cobra a chave canônica; um
identificador que não resolve mantém a própria chave, que é o que preserva o incremento para
nomes inexistentes.

**Decisão 2 — troca de e-mail em DUAS ETAPAS (mig 000037, tabela `email_change`).** O
endereço só muda quando o link enviado ao NOVO endereço é aberto. Escrever direto faria de
um erro de digitação o login E o canal de recuperação da conta, com o aviso indo para o
endereço digitado errado — a mesma propriedade que a ADR-40 preserva ao criar contas não
verificadas. A troca imediata foi oferecida ao dono e recusada por ele.

**Duas mensagens, para duas caixas, e as duas são obrigatórias.** O endereço NOVO recebe o
link. O endereço ATUAL recebe um aviso **sem link nenhum** (`chrome.shape_notice` +
`text.shape_notice`, os dois braços sem slot de URL): quem o lê pode ser alguém cuja conta
está sendo tomada por uma pessoa que já tem a sessão, e "clique aqui para impedir" é
literalmente a frase que a falsificação usaria. Mesma regra do `session_revoked`.

**O que é verificado no COMMIT, não no pedido.** Sob o lock da linha da conta:
a época de credencial ainda bate (`token_version`, igual a desafios e resets — uma troca de
senha, um reset ou um logout-all entre o pedido e o clique mata a troca pendente), o
endereço ainda está livre (o índice único é a única defesa contra alguém reivindicá-lo no
intervalo), e a linha ainda não foi gasta. Gastar o token e mover o endereço são UMA
instrução: separados, uma falha entre os dois queima o token deixando o endereço parado.

**O que consumir custa.** `token_version` é incrementado e **toda** sessão é revogada, com
a razão própria `email_changed` (reusar `password_changed` poria uma frase falsa na trilha
que a ADR-34 faz sobreviver às contas). O identificador mudou; uma sessão emitida contra o
antigo é credencial para uma conta que não atende mais por aquele nome — e quem clica no
link muitas vezes está num aparelho que nunca entrou.

**Endpoint de consumo sem sessão** (`POST /api/auth/email-change/confirm`), como o
`/email/verify`, e por um motivo mais forte: aqui o link chega na caixa para a qual a conta
está indo. Todo fracasso responde o mesmo 404 — exceto `email_taken`, que é distinguido de
propósito: quem o recebe tem um token que prova controle da caixa de destino e pode resolver
escolhendo outro endereço; "link inválido" o mandaria para o suporte.

**Alternativas descartadas.** Username obrigatório com backfill (expõe o e-mail e força um
valor herdado). Trocar o e-mail sem senha atual (uma sessão roubada moveria o canal de
recuperação).

**Revisão de 23/08/2026 — a disponibilidade em tempo real, que esta ADR havia descartado.**
O descarte original dizia "mais um oráculo, sem ganho: o save já responde 409", e a segunda
metade estava errada: o ganho é o usuário descobrir antes de apertar o botão, e o dono da
instância pediu justamente isso. A primeira metade continua certa, e é ela que decide o
alcance. Um endpoint de disponibilidade responde sobre o identificador de OUTRA conta sem
pedir senha — é uma API de enumeração por construção, e o que muda em relação ao 409 do save
não é a existência do oráculo, é o **preço de cada pergunta**: hoje custa uma senha válida ou
ser admin. Então ele foi ligado onde esse preço não importa e desligado onde importa.

- `GET /api/auth/username-available` — **só sessão**, então o anônimo não ganha nada e o
  401 único, o bcrypt-sempre-roda e o piso de 250 ms do login seguem intactos. **Só
  username**, nunca e-mail: um endereço é também uma caixa e existe fora daqui, então
  confirmar que está ocupado diz *esta pessoa tem conta aqui*; um username só existe aqui e
  diz apenas *alguém aqui usa esse apelido*. **Teto de 60 por 5 min por usuário**,
  dimensionado para quem DIGITA num campo com debounce — custa a um script quatrocentas
  consultas por hora em vez de milhares por segundo. O orçamento é cobrado antes da consulta
  e também para formas recusadas, senão basta acrescentar um caractere inválido para sondar
  de graça (mesma razão pela qual o balde do login incrementa para endereços inexistentes).
  Probe vazia é gratuita: não consulta nada e não revela nada.
- `GET /api/admin/users/email-available` — a contrapartida de e-mail existe **só sob
  `/api/admin`**, e a colocação é o argumento inteiro: depois do `RequireAdmin` o chamador já
  lista todas as contas com endereço, então não revela nada que ele não leia direto, e por
  isso não tem teto. Uma troca **pendente** conta como ocupado, porque o índice único guarda
  só a coluna viva e um endereço que alguém já está migrando passaria no teste para depois
  perder a corrida na confirmação — tendo sido anunciado como livre.
- **O custo que o argumento de alcance NÃO precifica, e que ficou registrado
  por insistência da revisão de segurança:** usernames são opcionais e não há
  nenhuma outra superfície onde uma conta aprenda o handle de outra — conteúdo é
  privado por conta e não existe menção nem perfil público. Então este endpoint
  é uma revelação genuinamente nova para qualquer sessão, inclusive um `viewer`,
  o papel menos privilegiado. Com um handle na mão, cinco senhas erradas
  deixam aquela conta 15 minutos sem login por senha, renovável — a trava é
  antiga (`loginByEmail`, chaveada pelo identificador RESOLVIDO) e antes exigia
  saber endereços. O que muda é que a lista de alvos passou a ser obtenível.
  Duas coisas limitam: a sondagem CONFIRMA um palpite, não enumera (é preciso
  adivinhar o handle para saber que existe), e `loginByIP` limita a ~4 vítimas
  por IP por janela, o que só um pool de proxies contorna. Não quebra o alcance
  escolhido — mas é o preço dele, e estava faltando escrito.
- **A troca de e-mail continua sem verificação ao vivo.** Ali a resposta fica no envio, onde
  a senha atual é o custo de cada tentativa. Foi a escolha explícita do dono quando o custo
  foi posto: incluí-la daria a qualquer pessoa convidada um enumerador rápido e sem senha das
  caixas com conta na instância.

**`reason: "pending"` anda junto de uma resposta DISPONÍVEL, e essa separação é
carregada.** O `AdminCreateUser` conflita só em `app_user`, então reportar uma
troca pendente como *ocupado* apagaria um Create que o servidor aceitaria — o
caso que o §5 nomeia ao pé da letra ("uma tela escondendo um botão que o
servidor permitiria lê como funcionalidade faltando") — e sem saída, porque a
lista de usuários não mostra conta nenhuma naquele endereço. A linha pendente
ainda pode expirar ou ter a época de credencial morta por uma troca de senha,
liberando o endereço. Então ela AVISA e o administrador decide.

A cobrança do orçamento acontece mesmo quando o cliente ABORTA a requisição, e
isso é deliberado: cobrar depois da consulta faria quem desliga antes da
resposta não pagar nada — que é o orçamento inteiro, já que nada obriga um
atacante a ler o corpo para aprender pelo tempo da conexão.

A sondagem **bloqueia o envio só na recusa, nunca durante a consulta e nunca
quando a checagem falha** (a mensagem diz "você ainda pode salvar", e o código
tem que cumprir isso): travar no debounce
deixa o botão morto por 450 ms sem causa visível, e um clique que corre com a checagem chega
ao servidor, que recusa do mesmo jeito — a mesma resposta, vinda de quem sempre foi a
autoridade.

**Travas.** Vinte e um testes de integração `TestUsername_*` / `TestEmailChange_*` /
`TestUsernameAvailable_*` / `TestEmailAvailable_*`, entre eles: o
aviso ao endereço antigo sem link em nenhum dos dois braços, o orçamento compartilhado entre
os dois identificadores, a época de credencial matando a troca pendente, o link substituído
por um pedido mais novo, e a recusa a tokens de API. Da revisão de 23/08: a rotação de
refresh NÃO mata uma troca pendente (direção positiva, que faltava), o irmão da janela de
graça também não, a sondagem de username é fechada ao anônimo, o e-mail é 404 para não-admin,
uma forma recusada consome orçamento e uma troca pendente conta como endereço ocupado.

### ADR-42 — A matriz RBAC vira configurável, com um piso que a configuração não alcança

**Contexto.** A matriz do ADR-33 era um `map` compilado em `internal/pkg/authctx`. Isso a
tornava auditável e à prova de deriva, mas também intocável: um dono que quisesse um Editor
sem `import.run`, ou um Leitor que pudesse restaurar backup, precisava de um fork. A tela
`Papéis e permissões` mostrava quatro linhas de chips — e uma lista de chips só consegue
mostrar o que um papel TEM, então "negado" e "não existe" eram indistinguíveis nela.

**Decisão.** Uma tabela `role_permission` (mig 000039) guarda a metade configurável, semeada
com exatamente o que a matriz compilada tinha no dia. A resolução (`internal/roleperm`) é a
UNIÃO do que a tabela diz com um piso que ela não alcança, e cada regra desse piso existe
porque sem ela a própria configurabilidade seria insegura:

- **O `owner` nunca lê a tabela.** Suas permissões vêm do mapa compilado em toda resolução.
  É isso — e não disciplina de handler — que garante que nenhum estado da tabela, truncamento
  incluído, produza uma instância sem ninguém capaz de consertá-la. A CHECK constraint recusa
  uma linha `owner` no banco, então nem o caminho SQL direto chega lá.
- **`roles.assign` é intravável.** É a meta-permissão: um papel que pudesse RECEBER o poder de
  conceder concederia a si mesmo todo o resto num segundo passo, o que tornaria o travamento
  das outras decorativo.
- **`policy.write` e `instance.transfer` são intraváveis** pelo motivo do ADR-35: um admin que
  tomasse `policy.write` baixaria o piso de senha e entraria por ele.
- **`content.read` é travada na direção oposta** — não pode ser REMOVIDA. Uma conta que não lê
  a própria biblioteca não é restrita, é quebrada, e o dono dela não tem como saber qual das
  duas é.

Uma permissão travada é lida do mapa compilado **qualquer que seja a linha**, o que faz do
travamento uma garantia e não "a tela atual não oferece": um `INSERT` escrito à mão é ignorado
na resolução (`TestLoad_ARowThatGrantsALockedPermissionIsIgnored`).

**A escrita é limitada pelo CHAMADOR, não por uma lista.** `ValidateWrite` recusa conceder o
que o próprio papel de quem escreve não tem. Isso é o que responde "um administrador não pode
se auto-adicionar itens de nível Proprietário", e está enunciado em termos do chamador de
propósito: uma permissão destravada no futuro fica coberta por construção, enquanto uma lista
de permissões-de-owner teria de ser lembrada. **Revogar não é limitado assim** — senão um
admin nunca poderia desfazer uma concessão feita pelo dono.

**O gate recebe a matriz como PARÂMETRO.** `authgate.RequirePermission(grants, p)` deixou de
ser `RequirePermission(p)`, e isso é a segurança inteira do ADR: como valor padrão de pacote,
um mount site que esquecesse de passar a matriz configurada continuaria aplicando a compilada
em silêncio, e a revogação do dono pareceria salvar sem mudar nada. Como parâmetro, esquecer é
erro de compilação — foi o compilador que listou os sete pontos a atualizar. Uma matriz `nil`
no gate NEGA; `nil` num construtor significa "a matriz compilada", e a substituição fica
visível no construtor em vez de escondida no fundo do gate.

**O snapshot.** `Can` roda no caminho de autorização de TODA requisição, então uma query por
verificação poria um round trip na frente de cada chamada de API. `roleperm.Repository` guarda
uma resolução imutável sob `RWMutex`, trocada inteira na escrita. Uma leitura que falha
mantém o snapshot anterior e é logada: substituí-lo pela matriz compilada em cada falha
restauraria em silêncio permissões que o dono revogou deliberadamente. Uma segunda réplica só
vê a mudança no próximo `Load`.

**Consequências.** `GET /api/admin/roles` passa a devolver a matriz EFETIVA (não a compilada —
uma tela cujo trabalho é mostrar o que o servidor aplica não pode descrever a regra que o
servidor parou de aplicar), mais `locked`, `caller_role`, `can_edit` e `editable_disabled`,
para que a tela renderize da resposta do servidor em vez de re-derivar a política. `PUT
/api/admin/roles/{role}/permissions` envia o CONJUNTO INTEIRO — ausente significa revogado,
a única codificação em que dois administradores editando ao mesmo tempo não fundem
silenciosamente suas intenções num papel que nenhum dos dois escolheu. As quatro recusas são
códigos distintos porque são frases diferentes para quem lê: `role_not_editable` fala da
instância, `permission_locked` da entrada, `permission_escalation` do chamador.

`testdb.Reset` **ressemeia** a tabela: truncá-la e parar seria deixar não um banco limpo, mas
uma instância com todo papel editável reduzido ao piso travado — e o próximo teste a construir
um repositório real veria escritas de conteúdo comuns responderem 403 sem nada apontando a
causa.

**Alternativa rejeitada:** guardar o delta como documento JSON em `app_setting`, no padrão do
ADR-35. Seria mais simples e degradaria melhor, mas a tabela normalizada é auditável por SQL —
e o risco que ela introduz ("tabela vazia = ninguém pode nada") foi fechado estruturalmente
pelo piso acima, não por cuidado.

#### ADR-42, emendas do sweep

A rodada de revisão encontrou quatro defeitos, e os dois piores são a MESMA falha em
níveis diferentes — vale registrar o padrão, não só as correções.

**`Deps.Grants` nunca era preenchido.** Os mount sites tinham o parâmetro (o compilador
exigiu), mas `cmd/server/main.go` construía `server.Deps` sem o campo, então o router caía
em `roleperm.Default()` e os gates de `/links`, `/notes`, `/tags`, `/import` e
`/backup/restore` seguiam aplicando a matriz compilada. Uma revogação comitava, era
auditada, aparecia desmarcada na tela — e não valia ali. Pior: os gates que `main` conecta
à mão (admin, folders, policy) **honravam**, então a revogação ficava PARCIALMENTE
aplicada, o que se lê como instabilidade e não como bug. O default `nil = compilada`, que
existe para os testes, foi exatamente o que permitiu esquecer. `TestServerDepsCarriesTheLiveGrants`
percorre o AST de `main.go` e recusa o literal sem o campo — nenhum teste de unidade
consegue ver isso, porque o defeito é um campo ausente num composite literal que compila.

**`AdminHandler.Mount` capturava a matriz como valor**, congelando os gates de `/api/admin`
no snapshot do boot. Mesma direção de falha (permissão a mais), mesma causa (uma indireção
a mais entre o parâmetro e a fonte viva). `liveGrants()` devolve o repositório;
`grantsSnapshot()` continua existindo, por requisição, e os nomes agora dizem qual é qual.

**Três permissões eram oferecidas e nenhuma rota aplicava** — `backup.export`,
`invites.read`, `invites.write`. Enquanto a matriz era compilada, lacuna de documentação;
com ela editável, um toggle que salva, audita e não faz nada. Agora estão montadas, e
`TestEveryPermissionIsEnforcedSomewhere` varre o AST atrás de argumentos de
`RequirePermission`/`RequireWrite`. `content.read` é a única isenta, e a isenção é
VERIFICADA: o teste exige que ela continue travada e presente em todo papel — se for
destravada, vira um toggle e o guard passa a cobrá-la no mesmo commit.

**`ValidateWrite` não checava o chamador quando `want` era vazio.** Todas as outras regras
estavam dentro do laço sobre `want`, então `Set(viewer, admin, nil)` zerava os admins e
retornava nil. Inalcançável por HTTP (a rota gateia em `roles.assign`), mas a função é
documentada como o ponto que uma segunda porta de entrada não consegue contornar — e não
era. A checagem subiu para o topo.

**Lost update.** DELETE-then-INSERT sob READ COMMITTED perdia a intenção de quem revoga:
a segunda transação tira snapshot antes do commit da primeira, então as linhas que a
primeira inseriu são invisíveis ao DELETE dela e sobrevivem. Um `pg_advisory_xact_lock`
por PAPEL serializa apenas escritas da matriz — advisory em vez de SERIALIZABLE porque não
há o que repetir aqui: são escritas raras, de ritmo humano, e a segunda esperar é o
comportamento desejado, enquanto uma falha de serialização chegaria ao dono como um 500
que ele simplesmente repetiria.

**Não existia o "próximo Load periódico"** que o comentário do `Set` e o INV-167 citavam.
`StartReloading` (30 s) passa a existir: ele limita quanto tempo uma revogação leva para
chegar a uma réplica que não a executou, e — no processo que executou — quanto tempo um
refresh que falhou deixa a matriz velha no ar, que antes era para sempre. Devolve um canal
fechado na saída, para que o encerramento seja OBSERVADO e não inferido de uma contagem de
goroutines que o pool também mexe.
