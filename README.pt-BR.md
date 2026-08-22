# Foldex

<p align="right"><sub><a href="./README.md">🇺🇸 English</a> · <strong>🇧🇷 Português</strong></sub></p>

<p align="center">
  <img src="docs/assets/home-empty.png" alt="foldex — gerenciador de bookmarks self-hosted (home view com empty state, sidebar de tags, topbar com busca + sort + density, CTAs Nova pasta / Novo link)" width="100%"/>
</p>

> Gerenciador de bookmarks self-hosted com tagging avançado, pastas aninháveis, contagem de cliques, previews visuais de URL, **notas em rich-text estilo pastebin**, **detecção de mudança por link + Web Push**, backup completo, UI em en/pt/es e extensão de navegador.

Foldex é uma "smart bookmarks bar" pessoal — guarda links organizados por **pastas aninháveis + tags M:N**, mostra **o que você de fato clica** (telemetria via `/go/{slug}`), captura visualmente cada URL (OG image / favicon / fallback de screenshot), deixa você anotar **notas em rich-text** (editor Tiptap com imagens inline) que vivem no mesmo grid/busca/tags/pastas dos links, e roda **inteiramente na sua máquina** (Postgres + RustFS + Go + React em containers).

> Stack: **Go 1.26 (Chi · pgx) · PostgreSQL 18 · RustFS · Vite 8 + React 19 + TypeScript + bun · TanStack Query · Tiptap 3 · react-i18next (en/pt/es) · Vitest 4**. Política de versionamento + invariantes em [`CLAUDE.md`](CLAUDE.md).

---

## Por que foldex em vez do bookmark nativo do navegador?

Bookmark nativo é ótimo para "salvar uma página rápida e esquecer". Quando você passa de 50 links, a fricção começa a doer. Foldex resolve cada uma dessas dores:

| Dor do bookmark nativo                                                                  | Como foldex resolve                                                                                                              |
| --------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| **Preso a um navegador.** Chrome ↔ Safari ↔ Firefox = 3 silos. Sync exige conta no fornecedor. | Seu próprio servidor. Acessa de qualquer browser, em qualquer máquina da sua rede. Os dados ficam num Postgres que **você** controla. |
| **Só árvore.** Um bookmark mora em UMA pasta. Quer "trabalho + ia + notebookLLM"? Triplica. | **Tags M:N** (um link pode ter N labels) **+ pastas 1:N aninháveis** (containment iPhone-style). Os dois sistemas coexistem. |
| **Zero telemetria.** Você "favorita" 200 links e usa 8. Não sabe quais.                 | Toda navegação passa por `/go/{slug}` que insere em `click_log`. Página de stats mostra cliques por dia, top hosts, top links (últimos 30d), distribuição por tag. |
| **Preview = favicon 16×16.** Lista cinza com mini-ícones.                               | Card visual com OG image. Se a página não tem, foldex **captura screenshot** automaticamente (Chromium headless → RustFS). Você pode também subir uma imagem manual. |
| **Busca fraca.** Match só no título/URL.                                                | Busca full-text via Postgres `pg_trgm` em título + URL + descrição. Compõe com filtro por tag (AND-multi-tag) e escopo de pasta. |
| **Backup = arquivo Netscape opaco.** Imagens? Cliques? Hierarquia? Tudo perdido.        | ZIP de backup único com `manifest.json` + `database.json` (5 tabelas) + **todas as imagens do RustFS**. Round-trip lossless, verificação por checksum SHA-256, 3 modos de conflito (wipe/skip/duplicate). |
| **Atalhos engessados.** Cmd+D abre o diálogo nativo do navegador.                       | Extensão MV3 + Alt-K (palette), Alt-N (novo link), Alt-F (nova pasta). Drag-and-drop iPhone-style entre cards/pastas. |
| **Lock-in do fornecedor.** Sair do Chrome = exportar HTML + perder metadados.           | Export para **Netscape HTML** (compat universal) **OU** JSON v2 (com pastas + click_count) **OU** ZIP de backup completo. Importer aceita os três (idempotente por URL; `click_count` é limitado na importação pra um arquivo hostil não inflar o log de cliques). |
| **Só em inglês / sem localização.**                                                      | UI totalmente localizada em **English / Português / Español** via `react-i18next`. Seletor de idioma no topbar; autodetecção pelo idioma do navegador no primeiro acesso; escolha persiste no `localStorage`. |
| **Pinned/favoritos = uma pastinha à parte.** Só visual.                                 | `pinned` é coluna real na tabela. `ORDER BY pinned DESC, …` aplica em todo modo de ordenação. Badge gradient sempre visível. |
| **Dados embutidos no navegador.** Trocou de máquina? Reinstalou Chrome? Reza.           | Postgres + RustFS em containers. `make up` numa máquina nova e seu ZIP de backup restaura tudo (DB + imagens) em ~minutos. |
| **Pastebin/app de notas é outra ferramenta.** Snippets e links vivem em lugares diferentes. | **Notas** (`⌥M`) são uma entidade de primeira classe junto com os links: editor rich-text (Tiptap) com **barra de formatação** — negrito/itálico/sublinhado/tachado, títulos, listas com marcadores e numeradas, alinhamento, cor do texto, fonte, citações/código, links e imagens inline —, mesmas tags/pastas/pin/busca dos links, intercaladas no mesmo grid com badge esmeralda, compartilháveis via página pública `/n/{slug}`. |
| **Sem como manter uma pasta privada** numa tela/máquina compartilhada sem criar uma segunda conta inteira. | **Senha por pasta.** Defina uma senha (hash bcrypt) em qualquer pasta — os links/notas dela ficam ocultos (e os thumbnails de preview são redigidos, mesmo no hover) até você desbloquear pra aquela sessão. Aplicado no backend, não só na UI: a API em si recusa entregar o conteúdo de uma pasta trancada sem prova da senha. Excluir uma pasta protegida pede essa senha; excluir uma árvore inteira é recusado se ela contiver subpastas protegidas independentemente, então desbloquear só a raiz nunca as apaga. Adicione uma **palavra-dica** opcional (exibida no popup de unlock; não pode ser a própria senha) e configure uma **senha master** em **Configurações** (com medidor de complexidade, confirmação e um lembrete próprio) pra redefinir a senha de uma pasta caso você esqueça. |

### Cenários reais que viraram a chave (bookmark nativo → foldex)

- **"Quais dashboards eu de fato uso?"** → a página de stats mostra top hosts e top links nos últimos 30 dias. Larga os de 0 cliques.
- **"Quero compartilhar um link curto com a equipe."** → toda URL ganha um alias estável `/go/{slug}` que redireciona + loga o clique.
- **"Trocar de máquina sem perder nada."** → 1 botão na UI gera o ZIP de backup completo. Outro botão na máquina nova restaura com `mode=wipe`.
- **"O mesmo link mora em 3 contextos (trabalho + ia + arquitetura)."** → 3 tags. Aparece nos 3 filtros.
- **"Quero saber visualmente qual link é qual antes de clicar."** → cada card mostra um preview OG/screenshot/upload em 150px.

### Quando foldex é overkill

Se você tem menos de 30 links salvos e usa **um único navegador numa única máquina**, bookmarks nativos são mais simples. Foldex começa a fazer sentido quando você precisa de acesso cross-browser, telemetria ou organização real em mais de uma dimensão.

---

## Quickstart

```bash
make up                 # puxa justoeu/foldex-{backend,web}:latest do Docker Hub
                        # + cria .env com segredos RustFS aleatórios e persistentes
                        # + sobe Postgres em 127.0.0.1 (sem precisar de toolchain Go/bun)
make migrate-up         # aplica as migrations SQL
make seed               # opcional: tags + links de exemplo

open https://localhost:9444
```

`make env` é idempotente: gera segredos RustFS root/app independentes de 256 bits
apenas quando faltam (ou ao migrar dos placeholders públicos antigos), persiste
no `.env` gitignored com modo `0600` e nunca imprime os valores. O uso direto de
`docker compose` precisa fornecer esses valores; bootstrap e backend recusam os
placeholders antigos, exceto quando
`RUSTFS_ALLOW_INSECURE_DEV_CREDENTIALS=1` é definido explicitamente para uma
instância de desenvolvimento isolada e descartável.

No desenvolvimento frontend com toolchain no host, `cd web && bun run dev`
escuta apenas em `127.0.0.1:9088`. Acesso pela LAN exige opt-in explícito:
`VITE_DEV_LAN=1 bun run dev`.

### Escolher entre imagens pré-buildadas e build local

| Quer … | Rode | Notas |
|---|---|---|
| Só rodar Foldex | `make up` | Puxa `justoeu/foldex-{backend,web}:${FOLDEX_VERSION}` do Docker Hub. Tag default é `latest`. |
| Pinar num build específico | setar `FOLDEX_VERSION=sha-3f6cc06` (ou `1.4.1` — as tags de imagem não levam o `v`) no `.env` e `make up` | Tags ficam disponíveis para targets de commit ou semver publicados manualmente. |
| Atualizar pra última tag | `make pull && make up` | `pull` re-baixa sem reiniciar; `up` percebe a imagem nova e reinicia. |
| Desenvolver / buildar do source | `make up-build` | Usa os mesmos `Dockerfile`s mas builda local, ignorando a imagem do registry. Precisa de Docker; NÃO precisa de Go/bun no host (rodam dentro dos build stages). |
| Aplicar mudanças locais | `make restart-backend` / `make restart-web` | Igual ao `up-build` mas só do serviço nomeado. |

Releases de mantenedor são manuais: dispare `release.yml` selecionando `main` e
informe `vMAJOR.MINOR.PATCH` estrito ou um SHA completo de 40 caracteres. Push de
tag nunca publica. O gate aceita apenas commits já em `origin/main`, confere a
semver nos dois manifests, recusa tags preexistentes, cria a tag só depois da
publicação dos dois manifests e faz todo publisher aguardar o GitHub
environment chamado `release`. Configure esse environment com reviewers
obrigatórios e mantenha `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN` como environment
secrets. Exclua cópias no nível do repositório para que um workflow guardado em
uma tag histórica não consiga lê-las.

### HTTPS (dev local) via mkcert

Nginx serve o container web em HTTPS na `:8443` interna, exposto no host em
`WEB_HTTPS_PORT` (default **9444**). O cert é assinado por uma CA local —
para o navegador confiar sem warnings, instale o
[`mkcert`](https://github.com/FiloSottile/mkcert) uma vez no host e emita
o par em `web/certs/`:

```bash
brew install mkcert nss      # nss só é necessário pro Firefox
mkcert -install              # instala a CA local no trust store do sistema
                             # (pede sua senha de sudo + um clique de
                             # confirmação no Keychain Access no macOS)

mkdir -p web/certs
mkcert -cert-file web/certs/cert.pem \
       -key-file  web/certs/key.pem \
       localhost 127.0.0.1 ::1 host.docker.internal

make up                       # reinicia o container web; certs vêm via bind-mount de web/certs
open https://localhost:9444   # 9444 = WEB_HTTPS_PORT; 9088 (WEB_PORT) é redirect HTTP→HTTPS
```

Os arquivos `cert.pem` e `key.pem` são **gitignored** — gere localmente,
nunca commite. O container web faz bind-mount de `./web/certs:/etc/nginx/certs:ro`
no boot, então você só precisa de `make restart-web` (ou `make up`) depois
de re-emitir o par — sem rebuild. A imagem publicada no Docker Hub não
shippa **nenhum** material TLS; se o volume estiver vazio (ex.: `docker pull && docker run`
puro sem mount), o container gera um par self-signed efêmero pra o
navegador conseguir alcançar a SPA.

Re-rode `mkcert ...` quando adicionar um hostname novo (ex.: um
`*.foldex.test` apontando pra `127.0.0.1`) ou depois de reinstalar a
CA local (`mkcert -install`) numa máquina nova.

> **Ainda aparece "Not Secure" no navegador?** Significa que a CA root
> do mkcert não está no trust store dessa máquina (ou está, mas o cert
> foi assinado por outra CA — comum quando se move o projeto entre
> máquinas). Rode `mkcert -install` e reemita os PEM com o bloco acima;
> depois `make up` para rebuildar a imagem nginx com os certs novos.

> **Reusar um Postgres que já roda no host.** Setar `POSTGRES_HOST=host.docker.internal`
> no `.env` (e `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB`
> correspondentes), pular `make db-up` e rodar `make apps-up`
> diretamente. Migrations precisam ser aplicadas contra esse DB na mão
> (ou `make migrate-up` se o usuário/db existirem).

## Arquitetura do stack

Três projetos compose na rede Docker compartilhada `foldex`. O Postgres vive em `docker-compose.db.yml`; o object store RustFS vive em `docker-compose.services.yml`; o `docker-compose.yml` é só a APP — backend, web e o worker `mailer` sob `COMPOSE_PROFILES=amqp` — alcançando os outros pelos nomes `db` e `rustfs`.

O arquivo da app não carrega nenhum serviço que você talvez já rode em outro lugar, e isso é estrutural, não arrumação: o compose interpola **todos** os serviços antes de subir qualquer um, e profiles não isentam o que excluem — então um `${VAR:?...}` de um store que você hospeda fora recusa subir também o backend e o web. O `make up` leva Postgres e RustFS junto só quando `POSTGRES_HOST` / `RUSTFS_ENDPOINT` apontam para os embutidos. Targets úteis (`make help`):

| Target | O que faz |
|---|---|
| `make db-up` / `db-down` / `db-nuke` | gerenciar só o Postgres |
| `make storage-up` / `storage-down` / `storage-logs` | gerenciar só o object store RustFS |
| `make apps-up` / `down` | gerenciar só backend + web (+ `mailer` sob `COMPOSE_PROFILES=amqp`) |
| `make up` / `stop-all` | stack completo — leva Postgres e RustFS junto só quando `POSTGRES_HOST`/`RUSTFS_ENDPOINT` apontam para os embutidos |
| `make migrate-up` / `migrate-down` | aplicar / reverter migrations SQL |
| `make psql` | shell no Postgres |
| `make logs` / `db-logs` | seguir logs |

## Tests + coverage (gate: ≥ 85%)

```bash
make test-backend       # só unit (sem Docker)
make test-integration   # unit + integration (Docker necessário)
make coverage-backend   # garante 85% no backend
make coverage-web       # garante 85% no frontend (Vitest)
make coverage-all       # ambos
( cd backend && make fmt-check )   # gate de gofmt — parte do pre-push gate
```

Regras de coverage, exclusões e o **pre-push gate** completo (gofmt + vet + coverage, rodados localmente antes de cada commit) estão em [`CLAUDE.md`](CLAUDE.md) §6.1. Toda implementação também roda um **sweep obrigatório de 5 agentes** (Code Review · Code Quality · Test Quality · Performance · Security) antes do merge — veja §9. Leia antes de abrir um PR.

Outros targets: `make logs`, `make psql`, `make healthz`, `make down`.
Veja `make help`.

## Varredura de segurança (CI)

Ferramentas em camadas (defense-in-depth) — todas **informativas** hoje (mostram achados sem bloquear merge; vire gate rígido removendo o `|| true` / `continue-on-error` quando houver uma baseline limpa):

| Camada | Ferramenta(s) | Workflow | Gatilho |
|---|---|---|---|
| **SAST** | CodeQL (`security-extended`, Go + JS/TS) | `.github/workflows/codeql.yml` | push · PR · semanal |
| **SAST** | Semgrep (packs OWASP/secrets/linguagem) + gosec | `.github/workflows/sast.yml` | push · PR · semanal |
| **DAST** | OWASP ZAP baseline (passivo) contra a stack viva | `.github/workflows/dast.yml` | **mensal** · dispatch manual |
| **SCA** | govulncheck + `bun audit` | `.github/workflows/ci.yml` | PR |
| **Deps** | Dependabot (gomod · docker ×2 · actions) | `.github/dependabot.yml` | PRs semanais |

Os achados de SAST aparecem na aba **Security ▸ Code scanning** do repositório (upload SARIF). O job de DAST builda a stack do código via `docker compose --build`, espera o `/healthz`, roda o ZAP baseline contra o nginx pela rede compartilhada `foldex` e sobe o relatório HTML/MD/JSON como artefato de 30 dias. Rode sob demanda em **Actions** → *dast* → *Run workflow*.

## Smoke test (sanity check depois de `make up`)

Contas estão ligadas, então as rotas autenticadas de `/api/*` precisam de
credencial. A única leitura da API sem sessão é `/api/files/notes/{uuid}.{ext}`, usada
pelas páginas públicas `/n/{slug}`. Só
chaves UUID canônicas de notas são aceitas; mídia de link continua protegida por sessão
e owner-scoped. Abra
<https://localhost:9444>, conclua a tela de setup, crie um **token de API** em
Configurações → Tokens de API e exporte:

```bash
AUTH="Authorization: Bearer fx_1_seu-token-aqui"
JSON="Content-Type: application/json"
```

```bash
# 1. Backend no ar? (/healthz é público — é o único endpoint que não pede nada.)
curl -s localhost:9089/healthz | jq .

# 2. Cria uma tag.
curl -s -X POST localhost:9089/api/tags -H "$AUTH" -H "$JSON" \
  -d '{"name":"jira","color":"#1f6feb","icon":"🪲"}' | jq .

# 3. Cria um link com essa tag (o preview é enfileirado async).
curl -s -X POST localhost:9089/api/links -H "$AUTH" -H "$JSON" \
  -d '{"url":"https://news.ycombinator.com","title":"HN","tag_ids":[1]}' | jq .

# 4. Espera ~2s pelo worker; então busca — `preview_status` deve ser "ok".
sleep 3 && curl -s localhost:9089/api/links/1 -H "$AUTH" -H "$JSON" | jq '.preview_status, .og_image_url'

# 5. Resolve o link curto (302 + contador). Por SLUG: o /go/1 numérico agora vem
#    desligado, porque essa rota resolve sem sessão e os ids de link são
#    compartilhados entre contas. Veja PUBLIC_NUMERIC_IDS.
curl -sI localhost:9089/go/hn | head -3

# 6. Cria uma nota (HTML rico sanitizado no servidor) e renderiza a página pública.
curl -s -X POST localhost:9089/api/notes -H "$AUTH" -H "$JSON" \
  -d '{"title":"Scratchpad","body_html":"<p>Olá <strong>mundo</strong></p>"}' | jq .
curl -s localhost:9089/n/scratchpad | grep -o '<h1>.*</h1>'

# 7. Cria uma pasta com senha, confirma que o conteúdo fica bloqueado sem o
#    token de unlock e que destrava com ele.
curl -s -X POST localhost:9089/api/folders -H "$AUTH" -H "$JSON" \
  -d '{"name":"Privada","password":"hunter22"}' | jq .
curl -s -H "$AUTH" -H "$JSON" localhost:9089/api/entries?folder_id=1 | jq .   # 403 folder_locked
UNLOCK=$(curl -s -X POST localhost:9089/api/folders/1/unlock -H "$AUTH" -H "$JSON" \
  -d '{"password":"hunter22"}' | jq -r .unlock_token)
curl -s -H "$AUTH" -H "$JSON" -H "X-Foldex-Folder-Unlock: $UNLOCK" \
  localhost:9089/api/entries?folder_id=1 | jq .                              # 200

# 7b. Define a senha mestra (Configurações) e recupera a pasta esquecida.
curl -s -X PUT localhost:9089/api/settings/master-password -H "$AUTH" -H "$JSON" \
  -d '{"password":"master-recover-1"}' | jq .
curl -s -X POST localhost:9089/api/folders/1/reset-password -H "$AUTH" -H "$JSON" \
  -d '{"master_password":"master-recover-1"}' -o /dev/null -w '%{http_code}\n'  # 204
curl -s -H "$AUTH" -H "$JSON" localhost:9089/api/entries?folder_id=1 | jq .   # 200 — pasta destravada

# 8. O escopo do token é real — tudo abaixo tem que ser recusado.
curl -s -H "$AUTH" -H "$JSON" localhost:9089/api/auth/sessions -o /dev/null -w '%{http_code}\n'  # 403
curl -s -H "$AUTH" -H "$JSON" localhost:9089/api/admin/users  -o /dev/null -w '%{http_code}\n'  # 403 (admin) / 404 (user)
curl -s -H "$AUTH" -H "$JSON" -X POST localhost:9089/api/backup -o /dev/null -w '%{http_code}\n'  # 403

# 9. Abre a SPA e testa ⌥K (paleta) / ⌥N (novo link) / ⌥M (nova nota); engrenagem + menu do
#    avatar (perfil e sair) na topbar.
open https://localhost:9444
```

## Atalhos de teclado (SPA)

| Atalho           | Ação                            |
|------------------|---------------------------------|
| `⌥K` / `Alt+K`   | Command palette (busca fuzzy). `⌘K` conflita com o foco da URL bar do navegador. |
| `⌥N` / `Alt+N`   | Novo link (⌘N é hard-claimed pelo navegador para "Nova janela") |
| `⌥F` / `Alt+F`   | Nova pasta (⌥P colidia com outros handlers; "F" de Folder) |
| `⌥M` / `Alt+M`   | Nova nota (⌘M é hard-claimed pelo macOS para "Minimizar janela") |
| `Esc`            | Fecha qualquer modal aberto / sai da view de pasta |
| `⌘Enter` (popup) | Salva (na extensão do navegador) |

> **Convenção**: todo atalho do foldex é Alt-based. Os navegadores
> engolem a maioria das combinações com `⌘` (⌘K = focus URL bar, ⌘N =
> nova janela, ⌘P = imprimir), então atalhos com prefixo Alt são os
> únicos que chegam à SPA com confiança.

## Internacionalização

Toda a UI passa por `react-i18next`. **Inglês é a fonte da verdade**; **Português** e **Español** são mantidos em paridade total (toda chave espelhada nos três).

- **Trocar idioma**: seletor no topbar. A escolha é gravada na CONTA (`app_user.locale`), então ela também decide o idioma de todo e-mail que o foldex te manda; o Perfil também oferece *seguir meu navegador*, que limpa a preferência de novo. No primeiro acesso autodetecta de `navigator.language`. Telas que disparam e-mail com você DESLOGADO — "esqueci minha senha" — mandam junto o idioma que estão exibindo, porque a interface segue `navigator.language` enquanto o fallback do servidor lê o header `Accept-Language`, e são configurações separadas: uma tela em português mandando um link de redefinição em inglês é o que acontece quando as duas discordam. A dica nunca passa por cima de uma preferência gravada.
- **Arquivos de locale**: `web/src/i18n/locales/{en,pt,es}.json`.
- **Adicionar locale**: solte um novo `<lang>.json`, liste em `SUPPORTED_LOCALES` e popule toda chave a partir de `en.json`. Plurais usam o sufixo `_one` / `_other`.

Toda string visível ao usuário precisa passar por `t('key')` e existir nos três locales — invariante no `CLAUDE.md`.

## Extensão de navegador

Uma extensão Manifest V3 vanilla vive em `extension/`. Carregue como
**unpacked** em `chrome://extensions` → Modo de desenvolvedor → Carregar
sem compactação → escolhe a pasta `extension/`.

Abra as opções e cole um **token de API** (Foldex → **Configurações → Tokens de API**).
A extensão não compartilha cookies com o app, então uma sessão não chegaria até ela; o
token é o que identifica sua conta. Depois clica no ícone em qualquer aba e aperta
Salvar. Veja `extension/README.md`.

## Screenshots

O hero do empty-state lá em cima é a Home view numa instalação fresca.
Mais capturas vêm conforme o projeto ganha conteúdo:

- Grid de home populado (cards + densidade 3/5/8 colunas)
- Command palette (`⌥K`)
- Dialog de novo link com tag autocomplete
- Página de import (drag-drop) + preview com mode picker
- Página de stats (KPIs, top hosts, distribuição por tag)
- Popup da extensão

## Layout

| Path           | O que tem |
|----------------|-----------|
| `backend/`     | Serviço Go (Chi + pgx + Postgres 18) — REST API, redirect, workers de preview + change-check + push |
| `web/`         | SPA Vite + React + TypeScript. CSS handoff (`styles/foldex.css`) + `overrides.css` local. |
| `extension/`   | Extensão Manifest V3 para capturar a aba atual |
| `docs/`        | Docs SDD: `VISION.md`, `ARCHITECTURE.md`, `TASKS.md` |
| `scripts/`     | Helpers de seed + backup |

## Backup & Restore

Snapshot completo do DB **e** do bucket RustFS num único ZIP. Endpoints
principais:

```bash
# Gera — streama um ZIP. Headers expõem counts + duração.
curl -OJ -X POST http://localhost:9089/api/backup
unzip -l foldex-backup-*.zip
#   manifest.json
#   database.json
#   files/screenshots/{id}[.{uuid}].{ext}
#   files/images/{id}[.{uuid}].{ext}
#   files/notes/{uuid}.{ext}

# Valida (sem aplicar)
curl -X POST -F file=@foldex-backup-*.zip \
  http://localhost:9089/api/backup/validate | jq

# Restaura — 3 modos de conflito
curl -X POST -F file=@foldex-backup-*.zip \
  'http://localhost:9089/api/backup/restore?mode=skip' | jq
#   mode=wipe       — apaga SUAS linhas e SEUS arquivos, depois restaura (DESTRUTIVO)
#   mode=skip       — preserva existentes (ON CONFLICT DO NOTHING; default)
#   mode=duplicate  — renomeia tags conflitantes pra "nome (2)"; pastas sempre novas;
#                     links com colisão de URL caem para skip + warning
```

Via UI: abre a página **Importar / Exportar** → coluna direita tem o
card **💾 Backup completo**. Chrome streama direto pro arquivo escolhido;
Firefox e Safari usam um download nativo de uso único, curta duração e ligado à
conta/sessão, então nenhum browser monta o ZIP inteiro na memória do JavaScript.
O servidor reporta os metadados de conclusão à parte, preservando o histórico
(últimos 10 backups: data, duração, tamanho, counts) no `localStorage` sem parsear
o arquivo. Arrasta um `.zip` em cima pra revisar o sumário de validação e escolher
o modo no `BackupRestoreDialog`.
Fechar a janela ou trocar o arquivo cancela a validação; depois que uma
importação ou restauração começa, a janela permanece aberta e não pode ser
fechada até o servidor responder.

Uploads têm limite comprimido de 2 GiB. Antes de validação ou restore tocar o banco, o Foldex rejeita nomes duplicados no ZIP, mais de 100.000 entries, JSON de manifest/database acima de 32/256 MiB, arquivos acima de 64 MiB cada ou mais de 4 GiB expandidos no total. Export aplica os mesmos envelopes, com no máximo 99.998 arquivos (duas entries ficam reservadas para manifest/database) e keys de objeto de 1.024 bytes. Só um export, validação ou restore roda por vez; uma request concorrente recebe `429 backup_busy` antes de trabalho no DB, object store, leitura do body ou temp file e pode tentar de novo.

> **O `mode=skip` converge para o arquivo inteiro.** O Foldex persiste por conta o digest exato do arquivo e seus mappings de IDs novos/mídia. Re-rodar um ZIP concluído não reinsere pastas, notas, associações ou cliques e não repete checks/uploads de objetos. Se o upload falhar depois do commit do banco, tente o mesmo ZIP de novo: o restore retoma o mapping commitado e grava só os arquivos faltantes. `wipe` limpa checkpoints anteriores porque substitui as rows alvo; `duplicate` cria outra cópia intencionalmente.

> **O restore não preserva mais os IDs, em nenhum modo.** Os ids das linhas vêm de sequências compartilhadas entre contas, então reaproveitar os ids que estão dentro do backup poderia colidir com linhas que já existem. Todo restore gera ids novos e reaponta as chaves de imagem e o histórico de cliques para eles; o que faz round-trip é o conteúdo e suas relações, não os números. O backup também **não carrega dado de login** — nem senha, nem sessão, nem segredo de 2FA — e restaurar um backup sempre cria conteúdo pertencente a quem está restaurando, nunca a quem exportou. Restaurar o backup de outra pessoa é permitido e só mostra um aviso de que o conteúdo está mudando de dono.

Design completo: [docs/SDD-BACKUP-RESTORE.md](docs/SDD-BACKUP-RESTORE.md).

## Contas e login

Contas vêm **ligadas por padrão** desde a 1.13.0.

**Primeira execução — inclusive numa atualização.** A SPA mostra uma tela de setup. A
conta criada ali vira administradora e **adota todos os links, notas, pastas e tags que
já existiam**: nada se perde e nada precisa ser reimportado. Defina `AUTH_PUBLIC_URL`
com a origem que você realmente acessa, porque é dela que saem os links de convite,
redefinição e verificação.

```bash
AUTH_PUBLIC_URL=https://localhost:9444
```

Prefere o comportamento antigo? `AUTH_ENABLED=0` mantém: toda requisição é atribuída ao
administrador de bootstrap e nada nunca pede senha. É uma opção legítima para uma
máquina de um usuário só numa rede privada — mas nessa configuração qualquer um que
alcance a porta é dono da biblioteca inteira, então mantenha o bind em loopback.

**Adicionando pessoas.** Não existe cadastro aberto: um administrador envia um convite
pela tela **Usuários & convites** dentro do hub de configurações (escopo Administração),
e só o endereço daquele convite consegue aceitá-lo —
com senha ou com a conta Google correspondente. O link aparece uma vez, no momento em
que o convite é criado, e também é enviado por e-mail. Credenciais de convite,
redefinição e verificação ficam depois de `#` nesses links, portanto a requisição HTTP
inicial e o access log do nginx nunca as recebem; a SPA remove o fragmento imediatamente.

**Papéis.** São quatro, e o que cada um pode fazer é uma matriz de permissões que o
servidor aplica — dá para lê-la em **Configurações → Administração → Papéis e permissões**.

| papel | para que serve |
|---|---|
| **Proprietário** | Comanda a instância. Exatamente uma conta o detém, e ele só muda por transferência. Só o proprietário edita a política de senha e de acesso. |
| **Administrador** | Gerencia pessoas, convites e a auditoria — mas não define as regras sob as quais administra. |
| **Editor** | Conta comum: leitura e escrita completas na própria biblioteca. É o que todo `user` de antes dos 4 papéis virou. |
| **Leitor** | Mesma biblioteca, somente leitura. Ainda exporta backup; não cria, edita, importa nem restaura. |

**O conteúdo continua privado por conta, em qualquer papel.** O papel decide se uma
escrita é aceita e se as telas de administração existem — nunca de quem são os links que
você enxerga. Um administrador gerencia contas e continua sem conseguir ler as linhas de
outra conta.

**Uma tela só de configurações.** A engrenagem do topbar abre tudo: um cabeçalho de página
com ações próprias (exportar backup, convidar alguém), um card mostrando com qual conta
você está logado — com um empurrão para ativar a verificação em duas etapas enquanto não
houver nenhuma — e uma grade de cards, um por painel. Administradores ganham um seletor
**Pessoal × Administração** acima disso; os demais veem só o escopo pessoal, porque
`/api/admin` responde 404 para eles e uma aba desabilitada prometeria uma superfície que o
servidor nega.

**Administrando pessoas.** **Configurações → Administração** lista todas as contas com
papel, último acesso e status, e permite trocar papéis, desativar, excluir, encerrar
sessões, enviar recuperação da conta e (sendo proprietário) transferir a instância.
Travas do servidor, não apenas escondidas na interface: você não pode rebaixar, desativar
ou excluir **a si mesmo**; o **último administrador ativo** não pode ser removido por
ninguém; e o papel e o status do **proprietário** não mudam de forma alguma a não ser
transferindo. Transferir encerra as sessões das duas contas.

**Auditoria.** **Configurações → Administração → Log de auditoria** registra logins e
falhas, mudanças de papel e status, convites, recuperações forçadas e edições de política.
Ela sobrevive às contas que descreve: excluir um usuário não apaga o que ele fez.

**Política da instância (só o proprietário).** **Configurações → Administração → Política
de senha e acesso** define o tamanho mínimo da senha, a validade dos códigos enviados por
e-mail e o intervalo de reenvio, além de quais domínios de e-mail podem entrar pelo
Google. Todo valor tem um piso que a configuração não cruza, então dá para deixar a
instância mais rígida, nunca mais fraca que o mínimo embutido. A mesma tela tem a
**criação automática de contas** pelo Google — desligada por padrão, e recusada enquanto
não houver ao menos um domínio permitido, porque ligá-la significa que a instância deixa
de ser apenas por convite. Contas criadas assim chegam sempre como Editor ou Leitor,
nunca como administrador.

**E-mail.** `MAIL_DRIVER` é `log` por padrão, o que imprime o convite — link incluído —
no log do backend em vez de enviá-lo. Isso é proposital: uma instância self-hosted sem
servidor SMTP precisa conseguir convidar alguém mesmo assim. Leia com
`docker compose logs backend`, ou copie o link que a tela de admin mostra. Para envio
real, use `MAIL_DRIVER=smtp` e as variáveis `MAIL_*`; `make up-mail` sobe o
[Mailpit](https://mailpit.axllent.org/) com uma caixa de entrada local em
<http://localhost:8025> para desenvolvimento.

O envio é **durável**: cada mensagem é gravada numa linha de `mail_outbox` na mesma
transação de banco que a credencial que ela carrega, então um restart, um deploy ou
uma queda do provedor não perdem um convite, um link de redefinição ou um código de
entrada. Um envio que falha é retentado com backoff crescente (1 min → 5 → 15 → 30 →
60) e desiste depois de seis tentativas, deixando a linha como `failed` para você
inspecionar. O payload guardado é cifrado com uma subchave derivada de `AUTH_ENCRYPTION_KEY` — uma linha na
fila contém um link de redefinição vivo, e um dump do banco não pode ser um kit de
sequestro de conta. As mensagens são renderizadas na hora do envio, em inglês,
português ou espanhol, seguindo o **idioma escolhido no perfil do destinatário**,
depois o `Accept-Language` de quem disparou o envio, depois inglês. O convite é a
única mensagem que não consegue honrar uma preferência, porque o convidado ainda
não tem conta.

Cada tipo de mensagem tem o **seu próprio layout**, e não um template único com
campos opcionais. São três formas, e elas vieram do conteúdo e não de gosto por
simetria: as mensagens que te levam a algum lugar abrem com um botão e depois
escrevem a URL por extenso, para você conferir o host antes de clicar e para o
e-mail continuar servindo num cliente que não renderiza botões; as que carregam
um código de uso único põem os **dígitos primeiro**, acima do texto, porque um
código de entrada é lido e redigitado em uns dez segundos; e as que só informam
não têm slot de botão nenhum. Essa última é propriedade de segurança, não
estilo: as duas mensagens que jamais podem conter link — "suas sessões foram
encerradas" e "esta conta entra com o Google" — agora não têm como ganhar um,
porque nem a versão HTML nem a de texto puro têm onde colocá-lo. Acertar só uma
das duas teria sido pior que não tentar: a mensagem continuaria afirmando "este
e-mail não contém link" logo acima de um.

**Idioma.** Cada conta escolhe o seu em *Configurações → Perfil*, ao lado do nome
de exibição. Deixar em *Seguir meu navegador* é uma escolha de verdade, não a
ausência de uma: mantém o comportamento atual, em que o idioma é adivinhado por
requisição. O seletor da topbar e o campo do perfil são a mesma configuração —
mudar qualquer um dos dois com a sessão aberta atualiza a conta, para que o idioma
da tela seja o idioma da sua caixa de entrada.

**Envio pelo RabbitMQ (opcional).** `MAIL_TRANSPORT` é `inproc` por padrão, em que
o próprio backend renderiza e envia as mensagens da fila. Isso não exige broker
nenhum e não perde nada: durabilidade, retry e backoff vêm todos da tabela
`mail_outbox`. Use `MAIL_TRANSPORT=amqp` com um `AMQP_URL` para entregar a mensagem
ainda cifrada a um broker e rodar o envio em container próprio:

```bash
# No .env, um ao lado do outro — precisam concordar:
#   MAIL_TRANSPORT=amqp
#   COMPOSE_PROFILES=amqp
docker compose up -d
```

`COMPOSE_PROFILES` não é conveniência. O serviço `mailer` tem
`profiles: ["amqp"]`, então subir a stack sem ele deixa um backend publicando e
nenhum worker consumindo — e essa falha é silenciosa dos dois lados: o publish é
confirmado, a linha do outbox fecha como `published`, e o primeiro sinal é um
usuário dizendo que o e-mail nunca chegou. O backend agora avisa sempre que
publica numa fila sem consumidor, e o worker registra uma linha por mensagem
enviada.

| Var | Padrão | Significado |
|---|---|---|
| `MAIL_TRANSPORT` | `inproc` | `inproc` \| `amqp`. Valor desconhecido recusa o boot |
| `COMPOSE_PROFILES` | — | Use `amqp` junto com `MAIL_TRANSPORT=amqp` para que `docker compose up -d` suba o worker |
| `AMQP_URL` | — | Obrigatório para `amqp`. `amqp://` para host **remoto** é recusado; use `amqps://` |
| _(TLS privado)_ | — | Configurado NA URL, não por variável: `?cacertfile=`, `?certfile=`/`?keyfile=` (mTLS) e `?server_name_indication=`. Sob Docker, coloque o PEM em `./certs` (montado em `/etc/foldex/certs`) e use o caminho do container. |
| `AMQP_ALLOW_PLAINTEXT` | `0` | Permite `amqp://` sem TLS a um broker não-loopback. Verificado contra a rede no boot **e no dial**, então um hostname que resolva fora de RFC1918/CGNAT/loopback/link-local é recusado antes de a credencial ir para o fio. Endereço do destinatário e nome do template viajam em claro; o corpo da mensagem segue selado. |
| `AMQP_EXCHANGE` | `foldex.mail` | Renomeie só para compartilhar um broker entre instâncias |
| `AMQP_QUEUE` | `foldex.mail.send` | |
| `AMQP_ROUTING_KEY` | `send` | Liga a fila ao exchange |
| `AMQP_PREFETCH` | `4` | Clampado em 1..64 |
| `MAIL_OUTBOX_BATCH` | `32` | Linhas que o relay reivindica por passada |
| `MAIL_OUTBOX_POLL_SEC` | `5` | De quanto em quanto tempo ele olha |

O worker recebe `AUTH_ENCRYPTION_KEY` (é o único processo que abre o payload) e
**nenhuma credencial de banco** — essa separação é justamente o motivo de ele
rodar à parte. Envios que falham sobem uma escada de retry em filas dedicadas
(1 min → 5 min → 30 min) e depois caem em `foldex.mail.dead`, que o backend
observa para que a linha do outbox ainda termine marcada como `failed`.

Num broker compartilhado, dê ao foldex vhost e usuário próprios em vez do padrão:

```bash
rabbitmqctl add_vhost /foldex
rabbitmqctl add_user foldex '<senha>'
rabbitmqctl set_permissions -p /foldex foldex '^foldex\.' '^foldex\.' '^foldex\.'
# AMQP_URL=amqps://foldex:<senha>@broker.example:5671/%2Ffoldex
```

**O que cada conta enxerga.** Tudo é privado por conta — administradores inclusive. Um
admin cria, desabilita e apaga usuários, mas nunca vê os links ou notas de outra conta.
A separação está no próprio banco, não em um filtro que a interface aplica.

**Sessões.** O login grava cookies httpOnly: um token de acesso curto e um de refresh de
30 dias que rotaciona a cada uso. Se um token de refresh for reapresentado — a assinatura
de um token roubado — todas as sessões daquela conta são encerradas e o dono recebe um
e-mail. Sair está disponível em qualquer lugar; "sair de todos os dispositivos" revoga
tudo. Mudanças de credencial — trocar/definir senha, desvincular Google ou desligar a
verificação em duas etapas — mantêm o dispositivo atual e derrubam os demais.

**Verificação em duas etapas.** Ative em **Configurações → Verificação em duas etapas**,
com **dois métodos que você pode usar separados ou juntos**:

- **App autenticador** — escaneie o QR com Google Authenticator, Authy, 1Password ou
  Bitwarden e confirme um código. Funciona sem conexão.
- **Códigos por e-mail** — confirme um código enviado para o seu endereço. Não exige
  instalar nada, e só aparece quando a instância tem SMTP configurado
  (`MAIL_DRIVER=smtp`); um código impresso no log do contêiner não seria fator nenhum.

Nos dois casos você guarda os dez **códigos de recuperação** de uso único, mostrados uma
vez só no formato `XXXX-XXXX-XXXX-XXXX`. O servidor guarda apenas digests vinculados ao
usuário e protegidos por chave, então não consegue mostrá-los de novo nem testá-los a
partir de um dump do banco. Guarde-os: uma conta que entra por link de redefinição de
senha tem o método de e-mail recusado de propósito — uma caixa postal nunca pode
satisfazer os dois passos — então numa conta só com e-mail eles são a volta.

No login, o mesmo campo de seis dígitos aceita um código do app, um código de
recuperação ou um código enviado por e-mail. Em Configurações, o campo também oferece
**"Enviar um código por e-mail"** quando o e-mail está configurado, para que mudar uma
configuração de segurança numa conta só com e-mail não custe um código de recuperação.
Remover um dos dois métodos mantém seus códigos de recuperação; remover o último os
apaga, já que não guardariam mais nada.

Administradores são obrigados a ter um segundo fator: com
`AUTH_REQUIRE_2FA_FOR_ADMINS=1` (o padrão), um admin sem fator é conduzido pela
configuração — escolhendo o método — antes da primeira sessão, em vez de ficar trancado
para fora. Isso também cobre a primeira conta do bootstrap e convites de administrador.
Promover um usuário logado a administrador encerra as sessões existentes para que o
próximo login aplique a verificação. Ativar a política numa instância existente bloqueia
imediatamente os recursos administrativos em sessões antigas ou renovadas até um fator
ser confirmado, mantendo as rotas de cadastro disponíveis. Um owner que quiser a regra
mais rígida pode definir **política da instância → `admin_second_factor: totp_only`**,
que impede o fator e-mail de contar para administradores; o padrão `any` aceita
qualquer um. Um administrador sempre pode largar um método enquanto o outro fica, mas
nunca o último.

> A atualização que aplica a migration `000023` invalida folhas de recuperação antigas
> e códigos de e-mail pendentes, pois digests sem chave não podem ser convertidos sem o
> texto original. O autenticador existente continua válido; gere novos códigos em Configurações.

> **A chave que cifra os segredos do autenticador tem que ir no backup junto com o
> banco.** Ou deixe o backend gerar `/data/auth_encryption.key` no primeiro boot, ou
> defina `AUTH_ENCRYPTION_KEY` no `.env` (`openssl rand -base64 32`) — o valor da env
> tem precedência, e se usar ele defina `AUTH_ENCRYPTION_AUTO_GENERATE=0`, para que
> apagar a linha por engano vire falha de boot em vez de uma chave nova que abandona
> em silêncio todo autenticador cadastrado.
>
> Diferente da chave de unlock de pastas, ela **não pode ser regerada** e não existe
> re-cifra: sem ela toda conta cadastrada perde o segundo fator e precisa de um
> administrador para limpar o cadastro.

**Entrar com o Google.** Opcional, e desligado até você configurar. Crie um cliente
OAuth do tipo *Aplicativo Web* no console do Google Cloud, registre
`<AUTH_PUBLIC_URL>/api/auth/oauth/google/callback` como redirect URI **exatamente
assim**, e defina:

```bash
GOOGLE_CLIENT_ID=…apps.googleusercontent.com
GOOGLE_CLIENT_SECRET=…
```

Duas coisas que ele deliberadamente não faz. **Nunca cria conta** — a instância é só por
convite, e auto-provisionar burlaria isso em silêncio. E **nunca pula a verificação em
duas etapas**: entrar pelo Google desemboca na mesma tela de código que o login por
senha.

**Conectar o Google pelas Configurações exige prova recente.** **Configurações → Como você
entra → Conectar o Google** abre uma confirmação da senha atual e, quando a verificação em
duas etapas está ativa, de um código atual do autenticador ou de recuperação. Só então a API
devolve a URL de redirecionamento do Google. O state de vínculo vale cinco minutos e fica
preso àquela sessão e versão de credencial exatas; sair, revogar a sessão ou trocar/redefinir
a senha antes do callback cancela o vínculo. Senha e código seguem apenas no corpo do POST,
nunca na URL.

**Já tem conta com senha no mesmo endereço?** Entrar pelo Google não te loga e não te
recusa — ele pede a **sua senha atual** uma vez, e então vincula o Google e **remove a
senha**. Dali em diante a conta é só-Google. Exigir a senha é o ponto inteiro: um
endereço de e-mail não é segredo, e qualquer pessoa pode colocar um num perfil do
Google, então um endereço coincidente sozinho nunca pode entregar uma conta. A troca é
que *"esqueci a senha, deixa eu entrar pelo Google"* não funciona — redefina primeiro,
converta depois.

Saídas para uma conta só-Google que perde o Google:

1. Um administrador usa **Enviar recuperação** na tela Usuários. O Foldex envia via
   SMTP um link de uso único somente à sua caixa postal verificada; o administrador não
   vê senha nem token, e sua credencial e sessões não mudam até você usar o link e
   escolher sua própria senha. Se o SMTP falhar, nada é alterado.
2. Ainda logado pelo Google, **Configurações → Como você entra → Definir senha**. Só
   depois disso dá para desvincular o Google — na ordem inversa a conta ficaria sem
   nenhuma forma de entrar, o que o banco recusa de saída. Definir a senha ou desvincular
   o Google encerra as sessões dos outros dispositivos.
3. Pedir a redefinição comum de senha numa conta assim envia *"esta conta entra pelo Google"* em
   vez de um link. Isso é proposital: um link ali deixaria a posse da caixa postal
   sem autenticação ressuscitar a senha que a conversão aposentou. A recuperação
   disparada por administrador prova adicionalmente a autorização administrativa.

**Tokens de API (extensão, scripts).** **Configurações → Tokens de API** cria uma
credencial de longa duração, exibida **uma vez** — o servidor guarda só um hash. Ela lê
e escreve seus links e notas e nada além disso: é recusada em troca de senha, sessões,
convites, administração de usuários e backup, então um token colado na configuração de
uma extensão não é a sua conta. Revogue e ele para de funcionar na hora.

**Esqueceu a senha.** **Redefina** pela tela de acesso — o link com fragmento chega por e-mail (ou no
log do backend, com o driver `log` padrão), vale 30 minutos e pode ser usado uma vez.
O link também é invalidado por troca de senha, sair de todos os dispositivos ou por um administrador
alterar o acesso da conta ou revogar suas sessões. Usá-lo desconecta todos os outros dispositivos, e uma conta com verificação em duas
etapas ainda precisa apresentar um código: a caixa postal sozinha nunca basta.

**Ficou trancado para fora?** Sem acesso à única conta administradora, a recuperação é
edição direta no banco — o mesmo status que a senha mestra de pastas já tem. O único
caso sem saída nenhuma pela interface é o **último administrador, só-Google, que perdeu
o acesso ao Google**: não existe outro admin para redefinir a senha dele. Voltar
`AUTH_ENABLED=0` e reiniciar devolve o comportamento single-user com todo o conteúdo
intacto, e é o caminho mais rápido de volta.

> **`SHARED_SECRET` foi removido.** Ele é anterior às contas: guardava `/api`,
> não identificava ninguém e não conseguia escopar uma linha sequer — a
> autenticação de verdade (ADR-30) o substituiu. A variável de ambiente, o
> header `X-Foldex-Secret` e a fiação dele na SPA e na extensão acabaram;
> apague-os do seu setup. A proteção de `/api/*` é exclusivamente trabalho da
> pilha de autenticação.

> **Links `/go/42` antigos pararam de funcionar?** Ids numéricos em `/go/{id}` e
> `/n/{id}` agora vêm desligados. Essas rotas resolvem sem sessão — são links públicos
> de compartilhamento — e o id de link é um contador compartilhado entre todas as
> contas, então deixá-los ligados permitiria caminhar 1, 2, 3… e enumerar todo link e
> nota da instância, inclusive de outras pessoas. Slugs não são afetados. Use
> `PUBLIC_NUMERIC_IDS=1` se você tem links numéricos antigos já compartilhados e prefere
> mantê-los funcionando.

> **Política de rede do preview.** Ranges de metadata/credenciais cloud e RFC6598 são
> sempre bloqueados. Use `PREVIEW_STRICT_SSRF=1` quando usuários não puderem alcançar
> serviços na rede interna do host; o modo strict rejeita os registries special-purpose
> completos da IANA. Deixar vazio preserva previews comuns de intranet RFC1918, como
> Jira e Confluence. Screenshots via Chromium sempre usam a política strict,
> independentemente dessa configuração.

Racional de design, threat model e a superfície de API completa:
[docs/SDD-AUTH-RBAC.md](docs/SDD-AUTH-RBAC.md).

## Docs

- [Vision](docs/VISION.md) — problema, goals, critérios de sucesso
- [Architecture](docs/ARCHITECTURE.md) — stack, modelo de dados, API, ADRs
- [Tasks](docs/TASKS.md) — log de implementação por fase
- [SDD: Backup & Restore](docs/SDD-BACKUP-RESTORE.md) — ZIP de snapshot DB + RustFS, modos de conflito, fluxo de validação
- [SDD: Senha master de pastas](docs/SDD-FOLDER-MASTER-PASSWORD.md) — recuperação de senha por pasta e palavra-dica
- [SDD: Auth, RBAC e multi-usuário](docs/SDD-AUTH-RBAC.md) — sessões, 2FA, OAuth Google, segmentação de dados por usuário
- [SDD: E-mail assíncrono e 2FA por e-mail](docs/SDD-EMAIL-ASYNC.md) — outbox transacional, templates HTML localizados, transporte plugável via RabbitMQ e e-mail como segundo fator permanente (**entregue**)

## Licença

[MIT](LICENSE) © 2026 Valmir Justo.
