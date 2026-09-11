# Foldex

<p align="right"><sub><a href="./README.md">🇺🇸 English</a> · <strong>🇧🇷 Português</strong></sub></p>

<p align="center">
  <img src="docs/assets/login.png" alt="Login do Foldex — Welcome back, e-mail e senha, bandeiras de idioma" width="100%"/>
</p>

<p align="center">
  <img src="docs/assets/home-empty.png" alt="Home do Foldex — biblioteca vazia, sidebar de tags, busca, Nova pasta / Novo link" width="100%"/>
</p>

Gerenciador de bookmarks self-hosted: **tags (M:N)**, **pastas aninháveis**, cliques via `/go/{slug}`, previews visuais, **notas** em rich-text, **detecção de mudança + Web Push** opcional e backup ZIP. Roda na sua máquina (Postgres + RustFS + Go + React). UI em **en / pt / es**.

> Stack: **Go 1.26 (Chi · pgx) · PostgreSQL 18 · RustFS · Vite 8 + React 19 + TypeScript 7 + bun**. Invariantes: [`CLAUDE.md`](CLAUDE.md).

---

## Quickstart

```bash
make up              # puxa justoeu/foldex-{backend,web}:latest
make migrate-up      # migrations SQL
make seed            # dados de exemplo (opcional)
open https://localhost:9444
```

`make up` e `make db-up` executam `make env` automaticamente, gerando credenciais do Postgres e RustFS no `.env` (modo `0600`, gitignored) sem imprimi-las. Chamadas diretas ao Compose precisam de `POSTGRES_PASSWORD`: não há fallback para senha conhecida. Volumes existentes mantêm sua senha; faça a rotação no Postgres e atualize os clientes em conjunto. Previews bloqueiam endereços privados/loopback por padrão. Use `PREVIEW_STRICT_SSRF=0` somente para previews de intranet de usuários confiáveis; screenshots e Web Push continuam limitados à internet pública. A primeira visita é a tela de **setup** (criar o admin); depois, **login**.

**Upgrade de um volume existente:** antes de atualizar, confirme que o `.env` contém explicitamente a senha aceita pelo Postgres. Instalações antigas podem não ter a linha `POSTGRES_PASSWORD` e depender do fallback `foldex` removido. Nesse caso, `POSTGRES_PASSWORD=foldex` temporariamente preserva o acesso; faça a rotação do papel do banco e de todos os clientes para um segredo novo assim que possível. Não deixe a linha vazia, gere uma substituta ou apague o volume esperando que a senha do banco existente mude. Se usa previews de intranet, defina `PREVIEW_STRICT_SSRF=0` explicitamente antes do upgrade. Essas mudanças de configuração exigem **release major** quando publicadas.

HTTPS em `:9444` usa mkcert no dev — ver [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md). Pin de release: `FOLDEX_VERSION` no `.env`. Build local: `make up-build`.

## O que vem

- Links e notas no mesmo grid (tags, pastas, pin, busca)
- Previews OG / favicon / screenshot
- Senha por pasta e senha master de recuperação
- Import/export: Netscape HTML, JSON, ZIP completo (DB + imagens)
- Extensão MV3 + paleta (`⌥K`)
- Multi-usuário (sessão, 2FA, Google, RBAC) — ligado por padrão
- Backups agendados fora da máquina (dump, drill de restauração, espelho de objetos, ZIPs por usuário) — artefatos baixáveis pelo owner da instância, opt-in

Bookmark nativo basta para poucas dezenas de links num único browser. Foldex vale a pena com acesso cross-browser, telemetria e organização em duas dimensões.

## Smoke test

Contas ligadas. Crie um token em **Settings → API tokens**:

```bash
AUTH="Authorization: Bearer fx_1_seu-token"
curl -s localhost:9089/healthz
curl -s -X POST localhost:9089/api/tags -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"name":"jira","color":"#1f6feb"}'
open https://localhost:9444
```

Mais fluxos (notas, unlock de pasta, backup): [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

## Atalhos

| Atalho | Ação |
|---|---|
| `⌥K` | Paleta de comandos |
| `⌥N` / `⌥F` / `⌥M` | Novo link / pasta / nota |
| `⌘V` / `Ctrl+V` | Colar URL → diálogo de link |
| `Esc` | Fechar modal / sair da pasta |

## Layout

| Path | O quê |
|---|---|
| `backend/` | API Go, workers |
| `web/` | SPA React |
| `extension/` | Manifest V3 |
| `docs/` | Visão, arquitetura, SDDs |

## Docs

- [Visão](docs/VISION.md) · [Arquitetura](docs/ARCHITECTURE.md) · [Auth / RBAC](docs/SDD-AUTH-RBAC.md)
- [Backup ZIP](docs/SDD-BACKUP-RESTORE.md) · [Backups operacionais](docs/SDD-OPS-BACKUP.md)
- [Senha de pasta](docs/SDD-FOLDER-MASTER-PASSWORD.md) · [E-mail](docs/SDD-EMAIL-ASYNC.md)
- [Extensão](extension/README.md)

## Licença

[MIT](LICENSE) © 2026 Valmir Justo.
