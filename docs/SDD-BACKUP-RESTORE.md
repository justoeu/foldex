# SDD — Backup & Restore (DB + MinIO)

> Software Design Document. Status: **Approved · v1.0 · 2026-05-13**
> Owner: foldex
> Related ADR: **ADR-20** (`docs/ARCHITECTURE.md`)

---

## 1. Visão geral

### 1.1 Problema

O foldex hoje tem dois caminhos de export/import — JSON v2 e Netscape HTML — mas ambos são **parciais**:

- Só exportam `tag`, `folder`, `link` (e relações via campos embarcados).
- **Não** exportam `click_log` (histórico de cliques que alimenta o dashboard de estatísticas).
- **Não** exportam os arquivos do MinIO (screenshots automáticos + uploads manuais de OG image).

Quando o usuário precisa migrar de máquina, restaurar após corrupção, ou só quer um snapshot pra dormir tranquilo, o caminho atual perde dados. Pior: a relação `link.og_image_url → /api/files/<key>` é silenciosamente quebrada quando o bucket é zerado.

### 1.2 Decisão

Introduzir um par de endpoints `POST /api/backup` / `POST /api/backup/restore` que produzem e consomem **um único arquivo ZIP** contendo:

1. `manifest.json` — versão do schema, contagens, checksums, timestamp.
2. `database.json` — snapshot completo de **todas** as tabelas (`tag`, `folder`, `link`, `link_tag`, `click_log`).
3. `files/screenshots/{id}.png` e `files/images/{id}.{ext}` — todos os objetos do bucket MinIO.

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
- **Atomicidade DB+MinIO via 2-phase commit**. Workaround: writes idempotentes + ordem (DB primeiro, files depois).

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
            │ Postgres│              │  MinIO   │            │   ZIP    │
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

Transação `REPEATABLE READ` garante que as 5 SELECTs vejam um snapshot consistente. SHA-256 é calculado **inline** durante a escrita do zip via `io.TeeReader`, evitando segunda passada.

### 2.2 Fluxo de restore

```
Browser uploads zip ──▶ /api/backup/validate ──▶ {ok, manifest, conflicts}
                                                       │
                                User picks mode (wipe/skip/dup)
                                                       │
                                                       ▼
                       POST /api/backup/restore?mode=… ──▶ apply
```

Restore aplica em duas fases:

1. **DB phase** (transação): WIPE | INSERT-ON-CONFLICT | INSERT-WITH-REKEY conforme modo.
2. **Files phase** (post-commit): para cada arquivo no zip, PUT no MinIO (skip se já existe quando mode=skip; sempre overwrite quando mode=wipe).

Se o servidor crashar entre as duas fases, re-rodar com o mesmo ZIP **converge** (files faltantes serão escritos; nenhuma duplicação porque key = `{id}.{ext}` é idempotente).

---

## 3. Formato do ZIP

### 3.1 `manifest.json`

```json
{
  "kind": "foldex.backup",
  "version": "1.0",
  "schema_version": 8,
  "created_at": "2026-05-13T23:00:00Z",
  "foldex_version": "git-sha-or-tag",
  "counts": {
    "links":       25,
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
**`version`** é o formato do ZIP (semver). `schema_version` é a versão da migration do DB (atual: 8).

### 3.2 `database.json` (v3)

```json
{
  "version":     3,
  "tags":        [ { "id": 1, "name": "ia", "color": "#A78BFA", "icon": "🧠", "created_at": "..." } ],
  "folders":     [ { "id": 1, "name": "Trabalho", "color": "#0EA5E9", "parent_id": null, "created_at": "..." } ],
  "links":       [ { "id": 1, "url": "...", "title": "...", "description": "...", "favicon_url": "...",
                      "og_image_url": "/api/files/images/1.png", "pinned": false,
                      "preview_status": "ok", "preview_error": null,
                      "folder_id": 1, "created_at": "...", "updated_at": "..." } ],
  "link_tags":   [ { "link_id": 1, "tag_id": 3 } ],
  "click_logs":  [ { "link_id": 1, "clicked_at": "..." } ]
}
```

**Versão 3** extends v2: adiciona `link_tags` (M:N explícita) e `click_logs`. Backward-compat: importer atual (v2) lê tudo menos os 2 campos novos.

### 3.3 Layout de `files/`

- `files/screenshots/{id}.{ext}` — espelha exatamente o prefixo `screenshots/` do bucket.
- `files/images/{id}.{ext}` — espelha o prefixo `images/`.

Keys preservadas pra que restore possa fazer 1-1 mapping. Em ModeWipe, o `link.id` original é preservado (ver §5.1) — keys batem direto. Em ModeDuplicate, files são re-uploaded com chaves baseadas no `id` novo do link.

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
- `503 Service Unavailable` se MinIO está fora (sem o bucket, backup é incompleto e enganoso — preferimos falhar).

### 4.2 `POST /api/backup/validate` — inspeção sem efeito colateral

**Request**: `multipart/form-data` com `file=<zip>`. Limit: 2 GB (via `MultipartReader` streaming).

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

**Response 422** (validação falhou — não-fatal pro usuário, mas restore não pode prosseguir):
```json
{ "ok": false, "manifest": { /* parsed */ }, "errors": [
  "checksum mismatch: files/images/7.jpg",
  "missing referenced file: files/screenshots/12.png"
] }
```

**Response 400**: zip malformado, manifest ausente, `kind` errado, schema_version do futuro.

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

**Erros**: `400` (manifest inválido), `422` (checksum mismatch), `500` (DB ou MinIO falhou no meio).

---

## 5. Modos de conflito

### 5.1 Matriz comportamental

| Entidade        | `wipe`                                       | `skip` (default)                                                    | `duplicate`                                                              |
| --------------- | -------------------------------------------- | ------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| `tag` (UNIQUE name) | TRUNCATE; INSERT com IDs originais       | `INSERT ON CONFLICT (name) DO NOTHING`; mapping `oldID→curID`        | colisão de nome → renomeia pra `nome (2)`, `nome (3)`, … (menor N livre) |
| `folder` (sem unique) | TRUNCATE; INSERT com IDs originais     | INSERT new; mapping `oldID→newID`                                    | INSERT new (sempre); mapping `oldID→newID`                               |
| `link` (UNIQUE url) | TRUNCATE; INSERT com IDs originais        | `INSERT ON CONFLICT (url) DO NOTHING`; mapping `oldID→curID`         | colisão de URL → **fallback skip + warning**. (URL unique não permite duplicata real; preferimos não corromper dado existente.) |
| `link_tag` (PK link_id,tag_id) | TRUNCATE; INSERT com IDs originais | INSERT re-key (mapping); `ON CONFLICT DO NOTHING`            | INSERT re-key (mapping); `ON CONFLICT DO NOTHING`                        |
| `click_log` (sem unique) | TRUNCATE; INSERT com IDs originais   | INSERT re-key (mapping); todos os logs adicionados                   | INSERT re-key (mapping); todos os logs adicionados                       |
| Files MinIO     | DELETE prefix; PUT all                       | PUT skip se key já existe; senão upload                              | PUT com keys baseadas no link.id novo                                    |

### 5.2 Justificativa dos defaults

- **`skip` default** porque é o único modo que jamais perde dado existente. Roda em qualquer DB (vazio ou populado) e converge.
- **`wipe` exige confirmação destrutiva** na UI (gradient danger + texto `⚠ AÇÃO DESTRUTIVA`).
- **`duplicate`** é o modo "fundir duas instalações". O fallback-pra-skip em link.url é uma limitação honesta: violar a UNIQUE constraint quebraria a integridade. Reportado em `warnings` pro usuário decidir o que fazer com os duplicados (ex: editar manualmente as URLs antes de re-importar).

### 5.3 Ordem de operações

Dentro da DB transaction, **a ordem é estrita**:

1. tags (cria mapping `oldTagID → curTagID`)
2. folders (cria mapping; folders raiz primeiro, depois descendentes — sort topológico por `parent_id`)
3. links (usa folders mapping; cria mapping de link)
4. link_tags (usa ambos mappings)
5. click_logs (usa link mapping)
6. Files (post-commit)

---

## 6. Validação

`validate` executa em ordem (curto-circuita no primeiro erro fatal):

1. **Magic check**: `manifest.kind == "foldex.backup"` (não-fatal: também aceita versões futuras do `version` se major bate).
2. **Version check**: `manifest.version` parsa como semver; major **bate** com o servidor atual.
3. **Schema check**: `manifest.schema_version <= servidor.schema_version`. Se for menor, emite warning (campos novos default); se for maior, erro fatal (servidor não conhece o formato).
4. **Checksum check**: pra cada entry em `checksums`, reabre o zip entry, recalcula SHA-256, compara. Mismatch = erro fatal.
5. **Reference integrity**: pra cada link com `og_image_url` referenciando `/api/files/<key>`, verifica que `files/<key>` existe no zip.
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

Tanto export quanto restore usam `io.Reader/Writer` em todas as fronteiras — nunca buffer o zip inteiro em memória. `MultipartReader` no restore evita o `ParseMultipartForm` que materializa tudo em RAM/tmp.

### 7.3 `og_image_url` como proxy URL, não bytes inline

Manter o campo como `/api/files/<key>` (e não embarcar bytes em base64 no `database.json`) porque:
- Mantém o invariant atual (`og_image_url` é proxy URL — code paths existentes não mudam).
- Permite que o usuário **inspecione um backup** abrindo o `database.json` num editor de texto.
- O acoplamento `link ↔ file` é via deterministic key (`{id}.{ext}`), então restore re-cria a correspondência sem ambiguidade.

### 7.4 ModeDuplicate: link conflicts fallback pra skip

Já justificado em §5.2. Documentado nos warnings do restore report pra que o usuário saiba o que aconteceu.

### 7.5 Why store `id` in the export?

Preservar `id` permite **wipe restore** ser bit-perfect: depois do restore, `link.id` antigo é igual ao novo, então URLs internas (logs externos que referenciam `/go/N`, bookmarks salvos) continuam funcionando. Skip mode usa o id apenas como key local pra building o mapping — o id real é re-atribuído.

### 7.6 `REPEATABLE READ` no export

Garante que as 5 SELECTs vejam o mesmo snapshot. Sem isso, um INSERT no `click_log` durante o export poderia produzir um log que referencia um link que não está no `database.json` (porque link list foi tirada antes), violando FK no restore.

---

## 8. Trade-offs e limitações

| Limitação                                    | Mitigação                                                                                 |
| -------------------------------------------- | ----------------------------------------------------------------------------------------- |
| Restore não é atômico DB + MinIO             | Writes idempotentes; re-rodar com mesmo zip converge. Warning no UI.                      |
| Sem backup incremental                       | Aceito pra v1. Re-avaliar quando bucket passar de 1 GB.                                   |
| Sem criptografia                             | Threat model permite. README orienta guardar em local seguro.                             |
| ModeDuplicate não duplica links com URL conflict | Reportado em warnings; user pode editar URLs manualmente e re-importar.                  |
| Backup gerado em schema_version=N não restaura em <N | Erro fatal claro; future work: schema migration helper.                                  |

---

## 9. Segurança

- Endpoints gated por `SHARED_SECRET` quando configurado (segue CLAUDE.md §4).
- Backup contém **TODOS** os dados — incluindo URLs privadas, screenshots, etc. Usuário deve guardar em local seguro.
- Importer roda dentro de transação — ataque de SQL injection via campos do JSON é mitigado por uso exclusivo de pgx parameterized queries (não há string concatenation).
- Validação de `kind` previne uso acidental de zips arbitrários como backup (não previne ataques deliberados — o sistema confia no `SHARED_SECRET`).
- Filenames dentro do zip são validados contra path traversal: rejeita entries com `..`, com `/` no começo, ou com paths fora de `files/`.

---

## 10. Testing strategy

### Backend
- **Round-trip integration test** (testcontainers Postgres + MinIO fake):
  1. Seed: 5 tags, 3 folders, 25 links com 3 og_images, 412 click_logs
  2. `Export()` para `bytes.Buffer`
  3. TRUNCATE all
  4. `Restore(buf, ModeWipe)`
  5. Diff: counts iguais, IDs iguais, `og_image_url` continua funcionando (GetObject não falha)
- **Validate rejects**: 5 sub-tests pra cada fail mode (kind errado, version maior, schema_version do futuro, checksum mismatch, missing file).
- **Conflict mode matrix**: 9 sub-tests cruzando tag/folder/link × wipe/skip/duplicate.
- Handler tests pra cada endpoint (resp headers + status codes).

### Frontend
- `BackupCard.test.tsx`: histórico vazio, histórico com 2 entries, click "Gerar" dispara o download.
- `BackupRestoreDialog.test.tsx`: render validation pass / fail, troca de modo, click "Restaurar" dispara mutation.
- `useBackup.test.tsx`: hooks com axios mock.
- Mock server (`src/test/server.ts`) ganha rotas `/api/backup`, `/api/backup/validate`, `/api/backup/restore`.

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
- ~~Atomicidade DB+MinIO?~~ → idempotência (§2.2, §8)
