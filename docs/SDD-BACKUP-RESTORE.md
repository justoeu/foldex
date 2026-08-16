# SDD — Backup & Restore (DB + RustFS)

> Software Design Document. Status: **Approved · v1.9 · 2026-08-16**
>
> **v1.1 (ADR-30, migration 000017 — multi-user).** Backup became per-user and
> stopped carrying auth material. Three contracts in this document changed and
> the affected sections carry an inline note: (a) **`wipe` no longer preserves
> ids** — §7.5 is superseded; (b) `wipe` deletes only the CALLING user's rows and
> object keys, never a TRUNCATE or a bucket-prefix delete; (c) `user_id` is never
> read from the ZIP, so a hand-crafted backup cannot plant rows in another
> account. See `docs/SDD-AUTH-RBAC.md` §10.
>
> **v1.2 (migration 000022 — note media ownership).** `notes/<uuid>` remains
> publicly readable, but its URL is no longer authority to mutate the object.
> Export/wipe use `note_media`/`note_media_ref`; restore always generates a new
> UUID, rewrites note HTML/cover, and rejects `user_id` anywhere in the snapshot.
> Referenced note media is validated before wipe, capped at 16 MiB of ZIP entry
> output, decoded under the 50 MP guard, and re-encoded before publication.
>
> **v1.3 (LEAK-HYD-001 / LEAK-HYD-002).** Validate and restore now share an
> archive preflight with explicit expansion/cardinality limits and exact-name
> duplicate rejection. One shared admission slot is acquired before upload
> spooling, so a concurrent validate/restore returns `429 backup_busy` without
> creating or copying a second temp file.
>
> **v1.4 (migration 000027).** Skip restore now checkpoints the exact archive
> digest and every old-to-new entity/note-file mapping in the same transaction
> as the content rows. A successful repeat performs no DB mutation or object
> I/O; a retry after post-commit upload failure resumes the persisted mapping.
> Entity/slug writes use staging + `CopyFrom`, and object writes/deletes use a
> cancellation-aware pool capped at eight operations.
>
> **v1.5 (LEAK-HYD-006 / N1-NEX-002 / CC-DAE-010).** Export no longer
> materializes an all-row `Snapshot`: every query row is encoded into a 0600
> `database.json` temp spool capped at 256 MiB inside the repeatable-read
> transaction, with count and SHA-256 computed inline; after commit the spool is
> streamed into the ZIP and removed. Skip resolves all explicit destination keys
> through namespace LISTs instead of one HEAD per key, while wipe submits only
> the caller-owned explicit keys through S3 multi-delete. The browser manifest
> fallback now uses typed bounded ZIP readers and rejects truncated, Zip64,
> malformed-signature, compressed-manifest, overflow and malformed-JSON inputs.
>
> **v1.6 (LEAK-HYD-006 acceptance closure).** Export now acquires the same
> fail-fast operation slot as validate/restore before any query or spool. RustFS
> LIST uses a synchronous callback and retains only owner-selected metadata;
> retained file/checksum state is capped by entry, key, manifest, per-file and
> total-expanded budgets. Note media is optimized once into a bounded operation
> spool and published from that spool after commit instead of reading/optimizing
> the ZIP twice.
>
> **v1.7 (final restore review).** Direct `Restore` now runs the same manifest
> major-version, checksum, file-name and reference-integrity preflight as
> `Validate` before consulting the durable ledger or touching DB/object state.
> A local note-media URL in body HTML or `cover_url` without its `files/notes/`
> entry is fatal; external URLs remain unchanged. Restore orchestration is split
> into bounded preflight, ledger, transaction, file-plan and single-object stages.
>
> **v1.8 (LEAK-HYD-008).** Browsers without the File System Access API no longer
> fall back to an Axios Blob. A CSRF-protected POST mints a 60-second opaque
> one-time ticket bound to the current user and session; a same-origin native GET
> consumes it before entering the existing export slot and streams the ZIP once.
> Authenticated status polling preserves accurate local history without reading
> archive bytes in JavaScript and backs off from 1 to 10 seconds within the same
> 30-minute client deadline. The HTTP server keeps a one-minute safety margin on
> its read/write deadlines while retaining 5-second headers and 60-second idle.
> Owner: foldex
> Related ADR: **ADR-20** (`docs/ARCHITECTURE.md`)

---

## 1. Visão geral

### 1.1 Problema

O foldex hoje tem dois caminhos de export/import — JSON v2 e Netscape HTML — mas ambos são **parciais**:

- Só exportam `tag`, `folder`, `link` (e relações via campos embarcados).
- **Não** exportam `click_log` (histórico de cliques que alimenta o dashboard de estatísticas).
- **Não** exportam os arquivos do RustFS (screenshots automáticos + uploads manuais de OG image).

Quando o usuário precisa migrar de máquina, restaurar após corrupção, ou só quer um snapshot pra dormir tranquilo, o caminho atual perde dados. Pior: a relação `link.og_image_url → /api/files/<key>` é silenciosamente quebrada quando o bucket é zerado.

### 1.2 Decisão

Introduzir um par de endpoints `POST /api/backup` / `POST /api/backup/restore` que produzem e consomem **um único arquivo ZIP** contendo:

1. `manifest.json` — versão do schema, contagens, checksums, timestamp.
2. `database.json` — snapshot das linhas de conteúdo do usuário (`tag`, `folder`, `link`, `note`, relações e eventos), sem auth nem `user_id`.
3. `files/screenshots/{id}[.{uuid}].{ext}`, `files/images/{id}[.{uuid}].{ext}` e `files/notes/{uuid}.{ext}` — somente objetos cuja ownership/ref pertence ao exportador.

Adicionar um terceiro endpoint `POST /api/backup/validate` pra inspecionar o ZIP **sem aplicar** — usado pelo frontend pra mostrar contagens + conflitos + checksum status antes do usuário confirmar o restore.

### 1.3 Goals

- **Round-trip lossless**: `export → wipe → restore` resulta em estado idêntico (módulo timestamps gerados pelo DB).
- **Idempotente por default**: re-rodar `restore --mode=skip` com o mesmo ZIP converge — nunca corrompe estado.
- **Streaming** — backup e restore lidam com bucket de centenas de MBs sem estourar memória.
- **3 modos de conflito** (wipe / skip / duplicate) cobrindo os cenários "migrar máquina nova", "fundir 2 instalações" e "preservar atual".
- **Validação prévia** — usuário sempre vê o que vai entrar antes de aplicar.

### 1.4 Non-goals (v1)

- **Backup incremental** (delta desde último backup). v1 é full snapshot. Justificativa: o volume de dados de um single-user bookmark manager é pequeno (~MBs), simplicidade > eficiência.
- **Criptografia do ZIP**. Confiamos no threat model do foldex (single-user, local network). Recomendação no README: guardar o ZIP em local seguro (1Password, disco encriptado).
- **Cross-version automatic migration**. Backup gerado em `schema_version=8` só restaura em instância rodando `schema_version=8`. Migração de schemas antigos fica como follow-up.
- **Atomicidade DB+RustFS via 2-phase commit**. Workaround: writes idempotentes + ordem (DB primeiro, files depois).

---

## 2. Arquitetura

### 2.1 Fluxo de export

```
┌──────────┐    POST /api/backup    ┌─────────────┐
│ Browser  │ ─────────────────────▶ │ Backend     │
└──────────┘                        │  Service    │
                                    └──────┬──────┘
                                           │
                  ┌────────────────────────┼────────────────────────┐
                  ▼                        ▼                        ▼
            ┌─────────┐              ┌──────────┐            ┌──────────┐
            │ Postgres│              │  RustFS   │            │   ZIP    │
            │ (RR tx) │              │  ListObj │            │  Writer  │
            └────┬────┘              └────┬─────┘            └────▲─────┘
                 │  SELECT *             │ GetObject              │
                 │  por tabela           │ (stream)               │
                 └───────────────────────┴────────────────────────┘
                                                                  │
                                                  ┌───────────────▼──────────┐
                                                  │ database.json + files/   │
                                                  │ + manifest.json (final)  │
                                                  └──────────────┬───────────┘
                                                                 │
                                                                 ▼
                                                       ┌──────────────────┐
                                                       │ HTTP response    │
                                                       │ application/zip  │
                                                       └──────────────────┘
```

Transação `REPEATABLE READ` garante que as SELECTs vejam um snapshot consistente. Cada row é codificado diretamente num temp spool `database.json` 0600 limitado a 256 MiB; contagens e SHA-256 são calculados inline. LISTs de RustFS entregam metadados por callback e descartam imediatamente qualquer key fora do conjunto owner-scoped. A transação termina antes do download e o spool é então copiado para o ZIP, sem reter as coleções nem metadados globais do bucket no heap e sem segurar WAL durante um cliente lento. O servidor mantém deadline global de 2 minutos para APIs comuns; somente export/download/validate/restore estendem read/write da conexão corrente para 31 minutos via `http.ResponseController`, alinhado ao timeout de 30 minutos do cliente sem ampliar a janela de sockets das demais rotas.

### 2.2 Fluxo de restore

```
Browser uploads zip ──▶ /api/backup/validate ──▶ {ok, manifest, conflicts}
                                                       │
                                User picks mode (wipe/skip/dup)
                                                       │
                                                       ▼
                       POST /api/backup/restore?mode=… ──▶ apply
```

Restore aplica em duas fases, depois do mesmo preflight usado por `validate`. O
preflight termina antes de consultar o ledger de `skip`, abrir a transação ou
planejar qualquer operação de arquivo:

1. **DB phase** (transação): WIPE | INSERT-ON-CONFLICT | INSERT-WITH-REKEY conforme modo. No `skip`, a mesma transação grava `backup_restore` + mappings de entidade/mídia sob `(user_id, archive_digest, mode)`.
2. **Files phase** (post-commit): arquivos de link são remapeados pelo id novo; arquivos de note são gravados somente sob UUID novo registrado para o caller. Nenhuma chave pública `notes/` do ZIP é sobrescrita. Upload usa até 8 workers e cancela siblings no primeiro erro. `skip` descobre destinos já presentes por LIST paginado dos namespaces explícitos, sem HEAD por key; `wipe` usa multi-delete somente com a lista owner-scoped capturada antes do delete da DB.

Se o servidor crashar entre as duas fases, re-rodar o mesmo ZIP em `skip` **converge**: o digest exato encontra o mapping já commitado, objetos existentes são pulados e faltantes são escritos sob as mesmas keys. Depois de `files_completed_at`, repeats retornam o report persistido sem HEAD/PUT. `wipe` apaga os ledgers do caller porque os target ids deixam de existir; `duplicate` continua criando outra cópia por definição.

---

## 3. Formato do ZIP

### 3.1 `manifest.json`

```json
{
  "kind": "foldex.backup",
  "version": "1.0",
  "schema_version": 14,
  "created_at": "2026-05-13T23:00:00Z",
  "foldex_version": "git-sha-or-tag",
  "counts": {
    "links":       25,
    "notes":        4,
    "tags":         7,
    "folders":     12,
    "link_tags":   34,
    "click_logs": 412,
    "files":       24,
    "file_bytes": 12477038
  },
  "checksums": {
    "database.json": "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "files/screenshots/3.png": "sha256:...",
    "files/images/7.jpg": "sha256:..."
  }
}
```

**Campos obrigatórios**: `kind`, `version`, `schema_version`, `created_at`, `counts`, `checksums`.
**`kind`** é o magic discriminator (rejeita zips de outros sistemas).
**`version`** é o formato do ZIP (semver). `schema_version` é a versão lógica do schema de conteúdo (atual: 14, migration 000027).

### 3.2 `database.json` (v7)

```json
{
  "version":     7,
  "owner_email": "owner@example.test",
  "tags":        [ { "id": 1, "name": "ia", "color": "#A78BFA", "icon": "🧠", "created_at": "..." } ],
  "folders":     [ { "id": 1, "name": "Trabalho", "color": "#0EA5E9", "parent_id": null, "created_at": "..." } ],
  "links":       [ { "id": 1, "url": "...", "title": "...", "description": "...", "favicon_url": "...",
                      "og_image_url": "/api/files/images/1.png", "pinned": false,
                      "preview_status": "ok", "preview_error": null,
                      "folder_id": 1, "created_at": "...", "updated_at": "..." } ],
  "notes":       [ { "id": 2, "title": "Nota", "slug": "nota",
                       "body_html": "<img src=\"/api/files/notes/old-uuid.jpg\">",
                       "cover_url": null, "pinned": false, "folder_id": null,
                       "created_at": "...", "updated_at": "..." } ],
  "link_tags":   [ { "link_id": 1, "tag_id": 3 } ],
  "click_logs":  [ { "link_id": 1, "clicked_at": "..." } ],
  "note_tags":   [],
  "note_clicks": []
}
```

**Versão 7** mantém os ids apenas para mapping e adiciona a semântica de re-key de mídia de note. `user_id` não faz parte de nenhum row e é rejeitado como campo desconhecido; `owner_email` é apenas informativo. `note_media`/`note_media_ref` não trafegam: o restore reconstrói ownership/refs para o caller a partir das chaves recém-geradas, nunca a partir do UUID antigo como autorização.

### 3.3 Layout de `files/`

- `files/screenshots/{id}[.{uuid}].{ext}` — espelha exatamente o prefixo `screenshots/` do bucket; o UUID existe nas capturas novas.
- `files/images/{id}[.{uuid}].{ext}` — espelha o prefixo `images/`; o UUID existe nos uploads novos.
- `files/notes/{uuid}.{ext}` — exportado somente quando há `note_media_ref` owner-scoped.

Nenhum modo preserva ids. Chaves de link são remapeadas para o `link.id` novo. Chaves de note **nunca** são gravadas no UUID antigo: um mapping `old notes/<uuid> → new notes/<uuid>` reescreve `body_html`/`cover_url` e direciona os bytes. Entradas sem link/note produzido pelo restore são descartadas.

---

## 4. API surface

### 4.1 `POST /api/backup` — gera e baixa ZIP

**Request**: sem body. Opcionalmente `Accept: application/zip`.

**Response**:
- `200 OK`
- `Content-Type: application/zip`
- `Content-Disposition: attachment; filename="foldex-backup-20260513T230000Z.zip"`
- `Trailer: X-Foldex-Backup-Stats` (counts + duration_ms como JSON)
- Body: o ZIP streaming.

**Erros**:
- `503 Service Unavailable` se RustFS está fora (sem o bucket, backup é incompleto e enganoso — preferimos falhar).
- `429 Too Many Requests` com `backup_busy`/`Retry-After: 1` se export, validate ou restore já ocupa o slot; a rejeição precede queries, spool e bytes de resposta.

#### Fallback de download nativo (Firefox/Safari)

`POST /api/backup/download` passa pelos mesmos gates de sessão, recusa de API
token e `SHARED_SECRET`, além de CSRF, e retorna URLs same-origin para download e
status. O token tem 256 bits, só o SHA-256 fica em memória, expira em 60 segundos
e é ligado a `(user_id, session_id)`. `GET /api/backup/download?id=…&token=…`
consome-o atomicamente antes de adquirir o mesmo slot de export e chama `Export`
uma única vez; cross-user, cross-session, expirado e replay recebem a mesma falha
fechada. O GET mantém `Content-Disposition` e não exige header CSRF por ser safe.

Como navegação nativa não consegue enviar `X-Foldex-Secret`, o guard aceita como
prova delegada somente um ticket ainda pendente que ele próprio protegeu no POST;
sessão e ownership ainda são revalidados depois. Query strings não entram nos
logs shipped (`$uri` no nginx e path-only no `slog`), e o anchor usa
`Referrer-Policy: no-referrer`.

`GET /api/backup/download/status?id=…` exige uma sessão ativa do mesmo owner e
retorna `pending|running|complete|failed`, counts, bytes e duração para o
histórico. O binding do GET que entrega o ZIP continua sendo da sessão exata; o
status é só owner-bound para um refresh do access token aos 15 minutos não perder
os metadados de um export maior. O estado em memória é limitado a 128 tickets,
limpo durante requests e retido por 10 minutos após terminar. Um novo ticket
substitui qualquer pendente da mesma sessão exata, e cada usuário pode manter no
máximo quatro tickets `pending|running`; assim uma conta não esgota o limite
global, enquanto resultados concluídos continuam disponíveis até a retenção ou
evicção por pressão do limite global. O polling começa em 1 segundo e dobra até
o teto de 10 segundos, reduzindo o pior caso de 1.800 requests para menos de 200
sem estender o timeout cliente de 30 minutos; os deadlines de leitura/escrita do
servidor são 31 minutos para não truncar esse contrato. Reiniciar o processo
invalida tickets; em deployment com múltiplas instâncias,
issue/download/status precisam de sticky routing.

### 4.2 `POST /api/backup/validate` — inspeção sem efeito colateral

**Request**: `multipart/form-data` com `file=<zip>`. Limit comprimido: 2 GiB (via `MultipartReader` streaming). Export, validate e restore compartilham um único slot de operação; o slot cobre query/spool/stream do export ou spool/preflight/consumo do upload, e só é liberado depois de fechar/remover os temp files.

**Response 200**:
```json
{
  "ok": true,
  "manifest": { /* ... */ },
  "conflicts": {
    "links":   3,
    "tags":    1,
    "folders": 0
  },
  "warnings": [
    "schema_version do backup (7) é mais antigo que o atual (8) — alguns campos serão default."
  ],
  "errors": []
}
```

**Response 200 com `ok: false`** (validação falhou — não-fatal pro usuário, mas restore não pode prosseguir):
```json
{ "ok": false, "manifest": { /* parsed */ }, "errors": [
  "checksum mismatch: files/images/7.jpg",
  "missing referenced file: files/screenshots/12.png"
] }
```

**Response 400**: o upload não pode ser aberto como ZIP. Depois que o ZIP é aberto, erros de manifest, versão, integridade e limites usam o mesmo envelope 200 com `ok: false`, para que o frontend consiga exibir a lista completa.

**Response 429**: `backup_busy` quando outro export/validate/restore já mantém o slot. A rejeição ocorre antes de ler o body, criar temp file ou chamar o service; `Retry-After: 1` permite retry curto.

### 4.3 `POST /api/backup/restore?mode={wipe|skip|duplicate}` — aplica

**Request**: idem `validate`.
**Default mode**: `skip` (idempotente, mais seguro).

**Response 200**:
```json
{
  "mode": "skip",
  "inserted": { "tags": 0, "folders": 5, "links": 22, "link_tags": 30, "click_logs": 405 },
  "skipped":  { "tags": 7, "folders": 0, "links": 3,  "link_tags": 4,  "click_logs": 7 },
  "wiped":    { "tags": 0, "folders": 0, "links": 0,  "link_tags": 0,  "click_logs": 0 },
  "files":    { "uploaded": 22, "skipped": 2 },
  "warnings": [],
  "duration_ms": 1240
}
```

**Erros**: `400` (manifest inválido), `422` (checksum ausente ou divergente e referência local ausente), `500` (DB ou RustFS falhou no meio).

---

## 5. Modos de conflito

### 5.1 Matriz comportamental

| Entidade        | `wipe`                                       | `skip` (default)                                                    | `duplicate`                                                              |
| --------------- | -------------------------------------------- | ------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| `tag` (UNIQUE user_id,name) | DELETE do usuário; INSERT com **id novo** | `INSERT ON CONFLICT (user_id, name) DO NOTHING`; mapping `oldID→curID` | colisão de nome → renomeia pra `nome (2)`, `nome (3)`, … (menor N livre) |
| `folder` (sem unique) | DELETE do usuário; INSERT com **id novo** | Primeira aplicação: INSERT new + mapping durável; repeats: reuse      | INSERT new (sempre); mapping `oldID→newID`                               |
| `link` (UNIQUE user_id,url) | DELETE do usuário; INSERT com **id novo** | `INSERT ON CONFLICT (user_id, url) DO NOTHING`; mapping `oldID→curID` | colisão de URL → **fallback skip + warning**. (URL unique não permite duplicata real; preferimos não corromper dado existente.) |
| `link_tag` (PK kind,entity,tag) | DELETE do usuário; INSERT re-key | INSERT re-key (mapping); `ON CONFLICT DO NOTHING`            | INSERT re-key (mapping); `ON CONFLICT DO NOTHING`                        |
| `click_log` (sem unique) | DELETE do usuário; INSERT re-key      | INSERT re-key uma vez por archive digest; repeats não reaplicam      | INSERT re-key (mapping); todos os logs adicionados                       |

> **v1.1.** A coluna `wipe` dizia "TRUNCATE; INSERT com IDs originais" até a
> migration 000017. TRUNCATE é table-wide (apagaria os outros tenants) e
> `RESTART IDENTITY`/`setval` mexem em sequências agora **compartilhadas**, então
> `wipe` virou `DELETE ... WHERE user_id` seguido do mesmo caminho mapeado do
> `skip` — que, depois do delete, roda sem conflito nenhum.
| Files RustFS     | DELETE das keys owned; PUT remapeado         | link key remapeada; note sempre UUID novo                             | link key remapeada; note sempre UUID novo                                |

### 5.2 Justificativa dos defaults

- **`skip` default** porque é o único modo que jamais perde dado existente. Roda em qualquer DB (vazio ou populado) e converge.
- **`wipe` exige confirmação destrutiva** na UI (gradient danger + texto `⚠ AÇÃO DESTRUTIVA`).
- **`duplicate`** é o modo "fundir duas instalações". O fallback-pra-skip em link.url é uma limitação honesta: violar a UNIQUE constraint quebraria a integridade. Reportado em `warnings` pro usuário decidir o que fazer com os duplicados (ex: editar manualmente as URLs antes de re-importar).

### 5.3 Ordem de operações

Dentro da DB transaction, **a ordem é estrita**:

1. preload owner-scoped de URLs/tag names relevantes + preload global bounded de slugs ocupados
2. staging temporário de tags/folders/links/notes via `CopyFrom`, com IDs frescos reservados nas sequências compartilhadas
3. INSERTs set-based; parent/folder refs e mappings old→new resolvidos por joins do staging
4. link_tags/note_tags + click_logs/note_clicks (batch/set-based)
5. `note_media` + `note_media_ref` para UUIDs novos (batch/set-based)
6. no `skip`, ledger + mappings persistem na mesma transação; files remapeados rodam post-commit sob pool bounded

---

## 6. Validação

`validate` e `restore` começam pelo mesmo preflight (curto-circuita no primeiro erro fatal):

0. **Archive budget**: no máximo 100.000 entries com nomes exatos únicos; `manifest.json` ≤ 32 MiB; `database.json` ≤ 256 MiB; cada outro entry ≤ 64 MiB; soma expandida ≤ 4 GiB. Export reserva duas entries para manifest/database e, portanto, aceita no máximo 99.998 arquivos; keys seguem o teto S3 de 1.024 bytes e o índice de checksums precisa caber no mesmo manifest de 32 MiB. O preflight abre todos os entries e lê no máximo `limite+1`, validando bytes reais e CRC em vez de confiar só em `UncompressedSize64`. Depois do decode, cada coleção de conteúdo (`tags`, `folders`, `links`, `notes`) fica limitada a 250.000 rows, cada coleção de relações/eventos (`link_tags`, `note_tags`, `click_logs`, `note_clicks`) a 2.000.000, e o campo legado ignorado `app_settings` a 1.000.

1. **Magic check**: `manifest.kind == "foldex.backup"` (não-fatal: também aceita versões futuras do `version` se major bate).
2. **Version check**: `manifest.version` parsa como semver; major **bate** com o servidor atual.
3. **Schema check**: `manifest.schema_version <= servidor.schema_version`. Se for menor, emite warning (campos novos default); se for maior, erro fatal (servidor não conhece o formato).
4. **Checksum check**: `database.json` e toda entry em `files/` precisam aparecer em `checksums`; omissão é erro fatal. O preflight recalcula o SHA-256 de cada entry declarada e compara; mismatch também é fatal. Em `validate`, ambos retornam o envelope 200 com `ok: false`; em `restore`, retornam 422 antes do ledger, DB ou object store.
5. **Reference integrity**: links internos sem entry geram warning. Toda URL local `/api/files/notes/<key>` preservada no HTML sanitizado ou em `cover_url` exige a entry `files/notes/<key>`; ausência é erro fatal antes do ledger/DB/files. URLs externas não entram nessa regra. Referências válidas só ganham ownership depois de serem re-keyed para o caller; a chave pública antiga nunca é persistida pelo restore.
6. **Conflict detection**: SELECTs de uniqueness:
   - `SELECT count(*) FROM link WHERE url = ANY($1::text[])` com array de URLs do backup
   - mesmo pra `tag.name`
   - folder = 0 (sem unique)

Devolve o relatório completo. Frontend mostra checks + counts + conflicts no dialog.

---

## 7. Decisões de design

### 7.1 ZIP vs tar.gz

ZIP escolhido por:
- Suporte nativo do browser pra download (`Content-Type: application/zip` abre o "Salvar como" sem extensão estranha).
- Random access ao manifest (validar manifest sem ler o stream inteiro).
- Lib stdlib (`archive/zip`) sem CGO.

Trade-off: ZIP não tem compressão tão boa quanto tar.zst. Aceitável: backups são pequenos (~10s of MB).

### 7.2 Streaming end-to-end

Tanto export quanto restore usam `io.Reader/Writer` em todas as fronteiras — nunca buffer o zip inteiro em memória. Export não cria `Snapshot`: cada cursor pgx entrega uma row, `json.Marshal` materializa no máximo essa row, e a saída vai para um temp file `0600` limitado a 256 MiB enquanto hash/contagens são atualizados. Depois do commit repeatable-read o arquivo volta ao offset zero e é copiado para a entry comprimida do ZIP; sucesso, callback abortado e qualquer erro fecham/removem o spool. A listagem de cada namespace RustFS também é incremental: o callback filtra contra keys owner-scoped e retém somente os `ObjectInfo` que entrarão no ZIP. `MultipartReader` no restore evita o `ParseMultipartForm` que materializa tudo em RAM/tmp. O upload comprimido vai para um temp file `0600`; note media válido é otimizado uma única vez antes da mutação e concatenado em outro spool 0600, limitado a 16 MiB por imagem e 4 GiB no total, do qual workers usam `SectionReader` depois do commit. Todos os spools são fechados/removidos em sucesso e em toda falha antes de liberar o slot. O preflight faz uma passada expandida completa e bounded antes de qualquer query/mutação; validate reaproveita os hashes calculados nessa passada.

### 7.3 `og_image_url` como proxy URL, não bytes inline

Manter o campo como `/api/files/<key>` (e não embarcar bytes em base64 no `database.json`) porque:
- Mantém o invariant atual (`og_image_url` é proxy URL — code paths existentes não mudam).
- Permite que o usuário **inspecione um backup** abrindo o `database.json` num editor de texto.
- O acoplamento `link ↔ file` é via URL local que contém o id no primeiro segmento da key (`{id}.…`); restore remapeia esse id e preserva o sufixo, inclusive UUID de operação.

### 7.4 ModeDuplicate: link conflicts fallback pra skip

Já justificado em §5.2. Documentado nos warnings do restore report pra que o usuário saiba o que aconteceu.

### 7.5 Why store `id` in the export? — ~~bit-perfect wipe~~ **SUPERSEDIDO (v1.1)**

~~Preservar `id` permite wipe restore ser bit-perfect.~~ Isso valeu enquanto
havia um usuário só. Com multi-tenant o argumento cai por dois motivos
independentes: outro tenant pode já ocupar aqueles ids, e `setval` numa sequência
compartilhada a partir do restore de UM usuário é simplesmente errado.

O `id` continua no export, mas **só como chave local para montar o mapping** —
exatamente o papel que ele já tinha no modo `skip`. Nenhum modo reinsere com o id
original. A identidade estável que o usuário enxerga é o **slug** (`/go/{slug}`),
que atravessa o backup verbatim; `/go/{N}` numérico deixa de ser garantia
atravessável de backup e, pelo ADR-32, vira opt-in de qualquer forma.

### 7.6 `REPEATABLE READ` no export

Garante que as 5 SELECTs vejam o mesmo snapshot. Sem isso, um INSERT no `click_log` durante o export poderia produzir um log que referencia um link que não está no `database.json` (porque link list foi tirada antes), violando FK no restore.

### 7.7 Ledger durável do `skip`

O `archive_digest` é SHA-256 sobre a sequência ordenada de `(exact entry name, preflight hash)`, com o tamanho do nome delimitado. Assim, mudança em manifest, database, bytes de arquivo ou nome produz outra unidade de restore. `backup_restore_entity` guarda mappings de tag/folder/link/note; `backup_restore_file` guarda o re-key de mídia de note. Tudo é owner-scoped e FK-cascades para `app_user`; o ZIP nunca escolhe `user_id`. O checkpoint de DB e os mappings commitam junto com conteúdo/associações/clicks. `file_report`/`files_completed_at` só publicam depois dos objetos, fechando tanto repeat bem-sucedido quanto retry do failure window sem 2PC.

---

## 8. Trade-offs e limitações

| Limitação                                    | Mitigação                                                                                 |
| -------------------------------------------- | ----------------------------------------------------------------------------------------- |
| Restore não é atômico DB + RustFS             | Ledger + mapping durável no `skip`; retry retoma files sem reaplicar DB.                  |
| Sem backup incremental                       | Aceito pra v1. Re-avaliar quando bucket passar de 1 GB.                                   |
| Sem criptografia                             | Threat model permite. README orienta guardar em local seguro.                             |
| ModeDuplicate não duplica links com URL conflict | Reportado em warnings; user pode editar URLs manualmente e re-importar.                  |
| Backup gerado em schema_version=N não restaura em <N | Erro fatal claro; future work: schema migration helper.                                  |

Custos restantes, deliberadamente explícitos:

- **Export heap O(owner-file-count), explicitamente capped**: keys owner-scoped, somente os metadados selecionados e o mapa de checksums permanecem até o manifest. O teto é 99.998 files, key S3 de 1.024 bytes e índice dentro de manifest ≤ 32 MiB; metadados globais das páginas LIST nunca são acumulados. Rows da DB são O(1) por cursor e o spool usa até 256 MiB de disco. Cada arquivo ainda exige um GET e uma entry ZIP.
- **Restore heap O(n), bounded por validação**: `Snapshot`, mappings, índice/hash do archive e fila de file work permanecem proporcionais ao conteúdo aceito (`database.json` ≤ 256 MiB, 100k entries e limites por coleção). Note media usa no máximo uma imagem de 16 MiB no preparo; o resultado vai para spool bounded e os até 8 uploads usam `SectionReader`, sem uma segunda otimização, além do decode cap de 50 MP.
- **Object store não vira O(1)**: `skip` faz no máximo três LISTs lógicos (`images/`, `screenshots/`, `notes/`), cada um paginado conforme o bucket, e um PUT por objeto ausente; `wipe` envia as keys exatas em batches S3 multi-delete (até 1000 por request). Isso remove HEAD/DELETE por key sem trocar ownership explícita por prefix deletion inseguro.

---

## 9. Segurança

- Endpoints gated por `SHARED_SECRET` quando configurado (segue CLAUDE.md §4).
- Backup contém **TODOS** os dados — incluindo URLs privadas, screenshots, etc. Usuário deve guardar em local seguro.
- Importer roda dentro de transação — ataque de SQL injection via campos do JSON é mitigado por uso exclusivo de pgx parameterized queries (não há string concatenation).
- Validação de `kind` previne uso acidental de zips arbitrários como backup (não previne ataques deliberados — o sistema confia no `SHARED_SECRET`).
- Filenames dentro do zip são validados contra path traversal: rejeita entries com `..`, com `/` no começo, ou com paths fora de `files/`.
- ZIP bombs são limitadas por contagem de entries, limites expandido por entry/total e leituras reais `max+1`; nomes duplicados são rejeitados antes de qualquer lookup ambíguo de manifest/database/files.
- Concorrência de export/validate/restore não multiplica queries, spools ou downloads: a admissão única e fail-fast responde 429 antes de chamar o service, ler body ou criar temp file.

---

## 10. Testing strategy

### Backend
- **Round-trip integration test** (testcontainers Postgres + RustFS fake):
  1. Seed: 5 tags, 3 folders, 25 links com 3 og_images, 412 click_logs
  2. `Export()` para `bytes.Buffer`
  3. TRUNCATE all
  4. `Restore(buf, ModeWipe)`
  5. Diff: counts iguais, IDs iguais, `og_image_url` continua funcionando (GetObject não falha)
- **Validate/direct Restore rejects**: kind errado, major incompatível, schema_version do futuro, checksum mismatch e mídia local de note ausente; os casos diretos provam que `wipe` não mutou conteúdo existente.
- **Archive preflight rejects em Validate e Restore**: entry count, nome duplicado, manifest/database/file/total expandido acima do cap, cardinalidade de coleção e leitura real `max+1`.
- **Admission concorrente**: matriz export/validate/restore prova que qualquer operação bloqueada mantém o slot; todas as outras recebem 429 sem service call, body read ou temp create, e cleanup precede release.
- **Conflict mode matrix**: 9 sub-tests cruzando tag/folder/link × wipe/skip/duplicate.
- **Convergência + fail-once**: repeat bem-sucedido e retry após commit/PUT falho não duplicam tags/folders/links/notes/associações/clicks e reutilizam IDs/keys mapeados.
- **Operation bounds**: trace pgx prova round trips constantes quando rows crescem; fake de storage prova concorrência 2..8, cancelamento sibling, um bulk existence lookup por restore incompleto, um exact-key batch delete no wipe e zero LIST/PUT após completion.
- **Export heap shape**: gated row source com 50k clicks exige encode antes do próximo `Next`; fake gera 150k metadados globais por callback e prova retenção/open somente owner-scoped; max+1 trava no teto de 99.998 files. Round-trip prova JSON/checksum/counts sem `Snapshot` de export.
- **Note-media single pass**: aplicação recebe um ZIP entry inválido depois do preparo e ainda publica os bytes do spool válido, provando que não reabre/reotimiza a entry; cleanup remove o spool.
- Handler tests pra cada endpoint (resp headers + status codes).

### Frontend
- `BackupCard.test.tsx`: histórico vazio, histórico com 2 entries, click "Gerar" dispara o download.
- `BackupRestoreDialog.test.tsx`: render validation pass / fail, troca de modo, click "Restaurar" dispara mutation.
- `useBackup.test.tsx`: hooks com axios mock.
- Mock server (`src/test/server.ts`) ganha rotas `/api/backup`, `/api/backup/validate`, `/api/backup/restore`.
- Parser ZIP: EOCD com comment/signature falsa, truncation em cada boundary, signatures local/central, compression diferente de Store, Zip32 sentinel/overflow, JSON inválido e shape de manifest inválido; nenhuma leitura do Blob inteiro.

### Coverage gate (CLAUDE.md §2)
- Backend ≥ 85% (excluindo wiring em main.go).
- Frontend ≥ 85% statements, ≥ 80% branches.

---

## 11. Migração futura

Quando `schema_version` precisar bumper:
- Adicionar entry em `backup/migrations/`: função pura `(v_n_minus_1 *database.json) → (v_n *database.json)`.
- Restore detecta `manifest.schema_version < current` → aplica chain de migrations.
- Manifest do backup imutável; só o snapshot em memória é mutado antes do INSERT.

Quando o `version` do ZIP (não schema_version) bumper: major bump significa quebra de compat. Servidor rejeita zips com major diferente do dele.

---

## 12. Open questions (resolvidas em revisão)

- ~~Embed bytes em base64 vs files/ separado?~~ → files/ separado (§7.3)
- ~~tar.gz vs zip?~~ → zip (§7.1)
- ~~Como duplicar link com URL única?~~ → não duplica, reporta (§5.2)
- ~~Atomicidade DB+RustFS?~~ → idempotência (§2.2, §8)
