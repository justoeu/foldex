# SDD — Addon Chrome + API Token (Foldex)

Status: **CONFIRMED** (2026-09-18)
Data: 2026-09-18
Branch alvo: `feature/chrome-addon-api-token`
Layout canônico: `Foldex - Plugin Chrome.html` (desempacotado e transcrito na §7 — é a fonte única de verdade visual)

---

## 1. Contexto e inventário do que JÁ existe

O backend e o web app já possuem **toda a infraestrutura de API token** — o SDD não recria nada disso:

| Capacidade | Estado | Onde |
|---|---|---|
| Token `fx_<id>_<secret>`, sha256 + comparação constant-time | ✅ existe | `auth/repository_apitoken.go` |
| Exibição **única** do plaintext (impossível rever: só hash no servidor) | ✅ existe | `CreateAPIToken` + `SecretBand` |
| Expiração opcional (`expires_in_days`; 0 = nunca expira) | ✅ existe | `apitoken_handler.go` |
| RBAC/scope: `content` apenas; `RejectAPIToken` blinda admin, settings, activity, password, backup | ✅ existe | `authgate`, `mount.go` |
| List/Create/Revoke (`GET/POST /api/auth/tokens`, `DELETE /api/auth/tokens/{id}`) | ✅ existe | `auth/handler.go:362-364` |
| UI de tokens na área de conta (Perfil → Tokens) | ✅ existe | `ApiTokensSection.tsx` |
| `POST /api/links` com `tag_ids` + `pending_tags` (tags novas inline) | ✅ existe | `links/dto.go` |
| `POST /api/folders` `{name, color}` | ✅ existe | `folders/handler.go` |
| `POST /api/links/{id}/image` (upload multipart de imagem) | ✅ existe | `mount.go:307` |
| `GET /api/folders`, `GET /api/tags`, `GET /api/stats/summary` | ✅ existe | mount.go |

**Gaps reais (escopo deste SDD):**
1. Limite de tokens ativos é **20** (`maxTokensPerUser = 20`) — requisito é **1**.
2. Não existe o addon Chrome.
3. Não existe download do addon na área admin.
4. Não existe botão "Rotacionar" token (revoga + cria novo, exigido pelo limite de 1).

---

## 2. Requisitos

### R1 — Um único token ativo
- `maxTokensPerUser: 20 → 1`. Criar com 1 ativo continua retornando `409 too_many_tokens` (comportamento e copy atuais já corretos: "revoke an existing token before creating another").
- `ApiTokensSection` ganha botão **Rotacionar**: `DELETE /api/auth/tokens/{id}` do token ativo seguido de `POST /api/auth/tokens` (mesmo nome, sufixo nada — nome é o atual do token), exibindo o novo plaintext na `SecretBand`. Erro no meio do par → estado consistente (token antigo revogado; usuário pode criar novo normalmente — copy da notice explica).

### R2 — Addon Chrome (Manifest V3)
`extension/` **já existe** na raiz do repo (config normalizado com regras de origin/loopback INV-093, api client com error mapping, i18n `_locales`, options page, testes) e é **retrabalhado** até este spec — módulos conformes permanecem, o resto é substituído. **Sem bundler** — JS vanilla (ES modules) + CSS + fontes woff2 embutidas. Build = zip determinístico.

#### R2.1 Manifest
```json
{
  "manifest_version": 3,
  "name": "foldex",
  "version": "3.0.10", /* lockstep com web/package.json — release bumpa os dois */
  "description": "Salve qualquer página na sua base, em dois cliques.",
  "action": { "default_popup": "popup.html", "default_title": "Salvar no Foldex" },
  "icons": { "16": "icons/fx-16.png", "32": "icons/fx-32.png", "48": "icons/fx-48.png", "128": "icons/fx-128.png" },
  "permissions": ["activeTab", "storage", "scripting", "alarms"],
  "optional_host_permissions": ["http://*/*", "https://*/*"],
  "commands": {
    "_execute_action": { "suggested_key": { "default": "Ctrl+Shift+S", "mac": "Command+Shift+S" }, "description": "Salvar no Foldex" }
  }
}
```
- Ícones "fx" gerados a partir do wordmark do design (fundo `#1B1B2F`, "fx" Outfit 800 branco).
- Permissão de host para o servidor do usuário é pedida em runtime (`chrome.permissions.request`) ao salvar configurações com um novo origin — mantém o install sem `<all_urls>`.

#### R2.2 Popup — dois painéis (404×632, layout da §7)
**Painel A "Novo link"** (default):
1. **Header**: logo `fx` (30×30, r9, `#1B1B2F`), "foldex" + "pessoal · self-hosted", pill de conexão (`conectado` verde / `sem conexão` neutro que abre o painel B), botão ⚙.
2. **Página ativa**: card com favicon do tab (via `chrome://favicon` não disponível em MV3 → usar letra inicial do host em chip 28×28 `#1B1B2F` ◈), título e URL mono.
3. **Título** (input, editável — pré-preenchido com `tab.title`).
4. **Imagem do site**: segmented control `Área visível | Página inteira` (estilo §7) + área de preview 16:9 com estados `capturando…` (spinner) / pronta (caption com dimensões) + botão flutuante **Recapturar**.
5. **Pasta**: grid 2-col de chips com dot colorido + contagem; primeira linha fixa: **＋ Nova pasta** → mini-form inline (nome + 6 swatches da paleta) → `POST /api/folders` → seleciona. Hint: "N links" da pasta ou "escolha uma pasta".
6. **Tags**: input tracejado (Enter adiciona), chips toggáveis; lista inicial de `GET /api/tags`; novas entram como `pending_tags` no save. Hint: "N selecionadas".
7. **Nota (opcional)**: textarea → `description` do link.
8. **Footer**: botão **Salvar no Foldex** (`#5B54E8`, estados `Salvando…`/disabled), painel de sucesso `✓ Salvo em {pasta}` + meta `{n} tags · imagem anexada` + botão **Novo**; meta-line `server · v{version}` e `⌘⇧S`.

**Painel B "Configurações"**:
1. Header com ←, título, tag "conexão".
2. **Endereço do servidor** (mono, hint "Instância self-hosted do Foldex, com https.").
3. **API token** (mono, toggle 👁/🙈, hint "Perfil → Tokens").
4. **Testar conexão** (outline) → card verde `Conexão estabelecida` com latency + **conta / links / pastas** (via `GET /api/auth/identities` [primeira identidade, se o token puder ler; senão omitir linha], `GET /api/stats/summary.total_links`, `GET /api/folders` length) OU card vermelho `Token recusado (401)` / `Servidor não respondeu` conforme status.
5. **Padrões de captura** (3 toggles): capturar automaticamente ao abrir o popup / fechar popup após salvar / sincronizar tags do servidor (a cada hora, alarm no service worker).
6. **Pasta padrão**: chips (mesma origem da lista de pastas; persiste `defaultFolderId`).
7. Footer: **Salvar configurações** + meta `token guardado em chrome.storage.local`.

#### R2.3 Captura de imagem
- **Área visível**: `chrome.tabs.captureVisibleTab`.
- **Página inteira** (decisão do usuário: scroll+stitch, sem banner de debugger): `chrome.scripting.executeScript` no tab ativo para obter `document.scrollHeight/Width` + devicePixelRatio; loop `window.scrollTo` por passo de viewport; `captureVisibleTab` por passo; costura em `<canvas>` offscreen no popup; PNG final enviado para `POST /api/links/{id}/image`.
- **Limitações documentadas** (não bloqueadoras): elementos `position:sticky` repetem em cada passo; imagens lazy só carregam ao rolar. O caption do preview mostra dimensões reais da imagem final.
- Ordem de save: `POST /api/links` → em sucesso, upload da imagem → painel de sucesso. Falha no upload de imagem **não** desfaz o link: sucesso com meta `{n} tags · sem imagem`.

#### R2.4 Armazenamento
`chrome.storage.local`: `{ server, token, prefs: {auto, close, sync}, defaultFolderId }`. Nada mais persiste.

#### R2.5 Atalho
`_execute_action` com ⌘⇧S / Ctrl+Shift+S (abre o popup — comportamento nativo do comando).

### R3 — Download do addon na área admin
- Nova rota **admin**: `GET /api/admin/addon/download` → zip do addon (`Content-Disposition: attachment; filename="foldex-extension-<version>.zip"`; header `X-Addon-Version`). Montada em `adminSurface` (atrás de `RequireAdmin` + `RejectAPIToken`).
- O zip é **embutido no binário** via `go:embed`: `backend/internal/addon/dist/extension.zip` + `version.txt`.
- Build: `make extension` → valida `extension/manifest.json`, zipa (`extension/` → `foldex-extension-<version>.zip`), copia para `backend/internal/addon/dist/`. Falha se `dist/` não existir no `go build` → `make backend` passa a depender de `extension` (com placeholder documentado: build sem o zip embute versão vazia e a rota responde `503 addon_not_built`).
- Web/área admin: card **"Extensão Chrome"** na página admin existente (onde vive `AdminUsersPage`): botão baixar `.zip`, versão, passos de instalação (chrome://extensions → Developer mode → Load unpacked extraiu o zip) e nota de permissões. Strings i18n pt-BR/en-US.

### R4 — Segurança (herda + reforços)
- Token só via `Authorization: Bearer` para o **servidor configurado** — nunca logado, nunca em URL.
- Origin do servidor validado (`https://` exigido em produção; `http://` permitido só para `localhost`/hosts `.local` — copy do hint).
- O addon não solicita `<all_urls>`; `optional_host_permissions` + request por origin no save de settings.
- A rota de download é admin-only (sem token — humano atrás de sessão admin).

---

## 3. Não-metas
- Chrome Web Store publishing.
- Captura full-page via `chrome.debugger` (rejeitada: banner).
- Context menu "Salvar no Foldex" (follow-up natural).
- Multi-token (o requisito é 1).
- Captura de iframes cross-origin (impossível sem debugger).

## 4. Contratos de API usados pelo addon (todos existentes, sem mudança)
```
GET    /api/auth/identities                 → contas (para o card de teste)
GET    /api/stats/summary                  → { total_links }
GET    /api/folders                        → pastas (+contagem se o payload já trouxer; senão sem count)
GET    /api/tags                           → tags existentes
POST   /api/folders                        { name, color }
POST   /api/links                          { url, title, description, folder_id, tag_ids, pending_tags }
POST   /api/links/{id}/image               multipart (file=png)
```

## 5. Design tokens (da §7 — fontes: Outfit 400–800, JetBrains Mono 400–700; woff2 embutidas no addon)
Paleta: ink `#1B1B2F` · primary `#5B54E8` (hover `#3F37D6`) · text-muted `#6B6B85` · mono-muted `#9A95B8`/`#8B86AE` · bordas `#E7E3F8`/`#E4E0F6`/`#EDE9FB`/`#F0EDFB` · fundos `#F8F7FF`/`#FBFAFF`/`#F1EEFC` · sucesso `#10B981`/`#0B7A5A`/`#3F9C7E` · erro `#E0286B`/`#A81049` · amarelo `#F5900B`.
Raios: card 18 · inputs 11 · chips 999 · botões 12. Sombra card: `0 30px 70px -30px rgba(38,30,90,.35), 0 2px 6px rgba(38,30,90,.06)`. Focus ring: `0 0 0 3px rgba(91,84,232,.14)` + border `#5B54E8`. Animações: `fx-spin .7s`, `fx-pop .25s`.

## 6. Testes e cobertura
- Backend: gates do repo (≥85% stmts). Novos testes para cap=1 (casos: criar com 0 ativos; criar com 1 ativo → 409; revoke→criar; concorrência sob lock — já coberto pelo padrão `FOR NO KEY UPDATE`, adaptar fixture).
- Web: vitest — rotate flow (sucesso, falha no meio, 409), card do addon (download href, versão, i18n).
- Extension: **`node --test`** para módulos puros (`extension/test/*.test.mjs`): api client (URL join, headers, error mapping), storage layer (defaults, normalização), stitch math (offsets/rows), folder/tag state reducers. Alvo ≥90% dos módulos puros. `make test-extension` + entrada no CI (bloco `run:` do workflow ci).
- mmh: piso 90% sobre o código tocado, red→green por task.

## 7. Layout canônico (transcrição fiel do HTML aprovado)
O arquivo de origem é o `Foldex - Plugin Chrome.html`. Estrutura, hierarquia, espaçamentos e TODOS os estados interativos (chips ativos/inativos, toggles, saving, saved, testing, connOk/connErr, focus rings, hover) estão especificados inline no próprio HTML (painéis de 404×632, header fx, página ativa, título, imagem/segmented, pasta grid, tags, nota, footer de save; painel de configurações completo). Onde este SDD e o HTML divergirem, **o HTML vence** em visual; este SDD vence em comportamento/API.
- Referência desempacotada (leitura): `template.html` em `/var/folders/0z/c5nklh457hbfkp524n88czhc0000gn/T/opencode/plugin-layout/` (temporária) — os valores definitivos são os do arquivo original.

## 8. Entregáveis
1. `extension/` completo (manifest, popup, service worker do sync de tags, icons, fontes, testes node).
2. `make extension` + embed no backend + `GET /api/admin/addon/download`.
3. Card "Extensão Chrome" na área admin (i18n pt/en).
4. `maxTokensPerUser = 1` + botão Rotacionar na ApiTokensSection.
5. CI: job/step de testes do addon; gates §6.1 verdes.
6. Docs: seção no README do repo (instalação do addon).

## 9. Definition of Done
- Todas as §8 entregues, gates §6.1 verdes localmente, sweep 5 agentes sem HIGH, graphify atualizado, PR mergeado em `feature/chrome-addon-api-token` (não direto na main), release minor `v3.1.0` despachada.
- E2E manual do addon documentada no PR (salvar link com imagem full-page em página real, testar conexão, rotacionar token).
