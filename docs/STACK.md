# STACK — versões pinadas

> Fonte de verdade operacional: `go list -m all` / `bun pm ls`. Esta tabela guarda o **porquê** de cada pin e as pegadinhas de upgrade. Re-verifique latest stable a cada upgrade: `go.dev/dl`, `nodejs.org`, `hub.docker.com/r/oven/bun/tags`, `npm view <pkg> version --registry=https://registry.npmjs.org/` (sempre registry público).

| Stack | Version | Notes |
|---|---|---|
| Go | `1.26.x` | `go.mod` + `golang:X.Y-alpine` base image |
| bun (Docker) | `oven/bun:1.3-alpine` | `web/Dockerfile` |
| RustFS | `1.0.0-rc.2` | S3-compatible object store; upstream still marks every release as prerelease, so pin the newest non-preview RC by immutable digest in compose + storage tests and never follow `latest`/`rc`/`main`. Root (`RUSTFS_ROOT_*`) vs app user (`RUSTFS_ACCESS_KEY=foldex`) provisioned by `cmd/rustfs-bootstrap` / `rustfs-init`; `make env` persists independent random secrets. Esse provisionamento fala a admin API do MinIO, não S3 — ver `madmin-go` abaixo antes de trocar de versão de RustFS OU de madmin |
| minio-go | `v7.3.0` | **SDK S3, não acoplamento a MinIO.** `internal/storage/client.go` usa só verbos S3 padrão — `PutObject`, `GetObject`, `StatObject`, `ListObjects`, `RemoveObject(s)`, `MakeBucket` — contra o RustFS, que é S3-compatível. O nome do pacote sugere um acoplamento que o código não tem; `aws-sdk-go-v2` faria o mesmo com mais peso. Bump de minor/patch é rotina. |
| madmin-go | `v3.0.110` | **Este SIM amarra ao MinIO como SERVIDOR.** `cmd/rustfs-bootstrap` chama `AddUser` / `SetUser` / `AddCannedPolicy` / `AttachPolicy`, que são a admin API proprietária do MinIO (`/minio/admin/v3/…`) — não há padrão por trás, e ela só funciona enquanto o RustFS continuar espelhando aquela superfície. **Verifique contra o RustFS em uso ANTES de qualquer bump, major especialmente:** o MinIO tem histórico de quebrar a admin API entre majors, e a falha não aparece em build nem em teste — aparece no boot de uma instância NOVA, no caminho que cria o usuário da aplicação e anexa a policy, ou seja, a instância não sobe. Trocar por HTTP direto não ajuda: troca uma dependência mantida por código próprio contra uma API igualmente instável. |
| Postgres | `18.4-alpine` | pinned in FOUR places that MUST stay in lockstep: `docker-compose.db.yml`, `docker-compose.services.yml`, `internal/testdb` testcontainers, and the `backup-agent` stage of `backend/Dockerfile` (its base image provides the version-matched pg_dump/pg_restore/initdb the agent and the restore drill run) — so tests mirror prod (a version-specific planner/default change can't hide behind an older test engine); host's Postgres ≥16 also works |
| filippo.io/age | `v1.3.1` | **The dump-encryption format is an operational contract**: operators decrypt artifacts with the standalone `age` CLI during disaster recovery, so this pin changes what `age -d` must be able to open. Streaming, chunk-authenticated; chosen over home-grown AES-GCM exactly for the no-Foldex recovery story (SDD-OPS-BACKUP §8) |
| Chi / pgx / testcontainers / golang-migrate | `v5.3 / v5.10 / v0.44 / v4.17` | |
| webpush-go | `v1.4` | Web Push (RFC 8030) |
| rabbitmq/amqp091-go | `v1.13.0` | AMQP transport for the mail outbox — `internal/mailoutbox` (publisher) and `internal/mailworker` (consumer). `streadway/amqp` is deprecated and MUST NOT be used. Only reached under `MAIL_TRANSPORT=amqp`; the default `inproc` path never dials a broker. |
| bluemonday | `v1.0.27` | Server-side HTML sanitizer for note `body_html` — `internal/pkg/htmlsanitize`. Never bypass; see CLAUDE.md §4 notes invariant. |
| pquerna/otp | `v1.5.0` | TOTP (RFC 6238) — `internal/auth/totp.go`. Pulls `boombuler/barcode` as an INDIRECT dep for the server-side QR PNG. Parameters are pinned to SHA1/6/30 in code: authenticator apps silently ignore non-default values and then produce codes that never validate. |
| golang.org/x/crypto (bcrypt) | `v0.55.0` | Folder-password hashing — `internal/folders/password.go`. Already in `go.sum` as indirect before this landed. |
| Vite / React / TS / Vitest / jsdom | `^8 / ^19.2 / ^6 / ^4.1 / ^29` | |
| MUI | `^9.0` | **only** `createTheme` + `ThemeProvider`. Visual lives in `web/src/styles/foldex.css`. |
| Tiptap | `3.30` (`@tiptap/react` + `@tiptap/starter-kit` + `@tiptap/extension-image` + `@tiptap/extension-placeholder` + `@tiptap/extension-text-align` + `@tiptap/extension-text-style`) | Rich-text editor for notes, with a formatting toolbar (`NoteToolbar.tsx`). `@tiptap/extension-link` and Underline are NOT separate deps — StarterKit v3 bundles both. `@tiptap/extension-text-style` bundles Color + FontFamily. The toolbar's output (text-align/color/font-family styles) MUST stay in lockstep with the server sanitizer allowlist (§4). |
| react-i18next | `^17` (wraps i18next `^26`) | en (source-of-truth) / pt / es. New visible strings MUST go through `t()` and ship in all 3 locales. Plurals use `_one`/`_other` (not legacy `_plural`). |
| TanStack Query | `^5.101` | |
| Testing Library / vite-plugin-pwa | `^16.3 / ^1.3` | |
| Package manager | **bun ≥ 1.3** | bun's resolver handles platform-specific packages more robustly than npm against a misconfigured mirror. |
