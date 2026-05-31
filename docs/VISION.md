# Foldex — Vision

## Problema

Os bookmarks nativos de browser (Chrome/Edge/Firefox) viraram um cemitério: a pasta cresce, ninguém renomeia, ninguém limpa, e a busca é pobre. Pra quem trabalha com ferramentas internas (Jira, Confluence, Looker, dashboards Datadog, Grafana, Verdi, intranet) o problema é pior:

- não dá pra **classificar** um link com várias categorias (um board pode ser "Jira" + "Time Invoices" + "Q3");
- não dá pra ver **quais links são realmente usados** (qual dashboard você abre toda segunda? qual ficou lá fazendo número?);
- não dá pra ter um **link curto compartilhável** (`/go/47`) que ainda te leve pro destino e conte os cliques;
- **importar/exportar** funciona só dentro do mesmo browser e perde toda a metadata útil.

Foldex resolve isso como um **app pessoal self-hosted**: bookmark com cara de produto, organizado por tags livres, com contador de cliques, preview visual e captura via extensão.

## Goals

1. **Listar e abrir links em segundos.** Grid visual com favicon e og:image, filtro instantâneo por texto + tags, atalho `⌥K` pra command palette.
2. **Classificações livres em M:N + organização em pastas 1:N (aninháveis).** Tags coexistem com pastas no estilo iPhone: tag = label (M:N, várias por link), pasta = containment (1:N, link mora em UMA pasta). Pastas se aninham em N níveis via `parent_id` self-FK. Drag-and-drop de link sobre pasta move; soltar link sobre outro link cria uma pasta nova com os dois dentro. Detalhes em ADR-19.
3. **Métricas de uso.** Cada clique via `/go/:id` insere uma row em `click_log` (single source of truth); lista pode ser ordenada por "mais usados" / "usados recentemente".
4. **Preview automático.** Ao criar, backend busca `<title>`, `og:image`, favicon. Quando o site não tem og:image e o host resolve pra IP público, Chromium headless captura um screenshot como fallback. Card renderiza visualmente parecido ao que o link representa.
5. **Monitorar links + Web Push.** Per-link opt-in (horário/diário/semanal) — o changecheck worker faz fingerprint híbrido (feed RSS/Atom se existir, content hash do `<main>` como fallback) e dispara notificação push quando o conteúdo muda. Sino no Topbar liga/desliga a permissão; badge âmbar no card aparece até o usuário clicar pra marcar como visto. ADR-23 e ADR-24.
6. **Import/Export + Backup completo.** Aceita o `bookmarks.html` (Netscape) que qualquer browser exporta + um JSON versionado próprio. Backup ZIP com `manifest.json` + `database.json` (5 tabelas) + todos os arquivos do MinIO — round-trip lossless com 3 modos de conflito (wipe/skip/duplicate).
7. **Captura via extensão.** Manifest V3 popup que pré-preenche URL + título da aba atual, escolhe tags, salva.

## Non-goals (v1)

- **Sem multi-user.** Roda como app de uma pessoa, sem login, atrás do `127.0.0.1`.
- **Sem sync entre máquinas.** O dado vive num Postgres local; backup é responsabilidade do usuário (script `pg_dump` sugerido).
- **Sem app mobile nativo.** A SPA virou PWA-grade (manifest + service worker offline + install via "Adicionar à tela inicial") e tem layout responsivo dedicado em ≤768px e ≤600px (topbar single-row com kebab, dialogs full-screen, FAB). Pra alcançar do celular, basta `WEB_BIND_HOST=0.0.0.0` e o LAN IP no SAN do cert. Extensão MV3 segue desktop only.
- **Sem AI tagging automático.** Sugestão de tags por LLM fica pra v2.
- **Sem deploy cloud.** Sem TLS gerenciado, sem secrets manager. Quando virar multi-user, repensar.

## Target user

Engenheiro/PM que vive em browser, abre dezenas de ferramentas internas por semana, e quer um lugar único pra organizar tudo sem depender da estrutura de pastas do navegador. Caso de uso primário: links pra Jira, dashboards, Confluence, wikis internas etc.

## Experiências core

| Experiência                | Como acontece                                                                                  |
|----------------------------|-----------------------------------------------------------------------------------------------|
| **Bookmarks bar visual**   | Página inicial: grid de cards (folder cards + links soltos), filtro por tag, densidade 3/5/8 colunas. Card mostra favicon, título, og:image, contador. Pasta mostra 2×2 mini-thumbnails (ou RapidView popover no modo compacto). |
| **Pastas iPhone-style + aninhadas** | Clicar uma pasta entra nela (Esc / botão "← Pastas" sobe um nível). Navegação 100% em memória — IDs nunca aparecem na URL. Drag-and-drop: link → pasta (move), link → link (cria nova pasta com os dois). `⌥F` cria nova pasta — dentro de uma pasta cria subpasta. |
| **Command palette**        | `⌥K` abre overlay com busca fuzzy (título + URL + tag). `Enter` abre o link via `/go/{id-or-slug}`. |
| **New link**               | `⌥N` ou botão `+` abre dialog. URL → cola → backend resolve preview em background.            |
| **Paste-to-create**        | `⌘V`/`Ctrl+V` (ou "Paste" no menu de seleção do celular) em qualquer canto da página sniffa o clipboard; se for uma URL, o dialog de New Link abre com ela pré-preenchida. No-op dentro de inputs ou com outro modal aberto. ADR-21. |
| **Monitor + Push**         | Em qualquer link, escolher frequência (horário/diário/semanal) no `LinkDialog`. Sino do Topbar pede permissão de Web Push; quando o conteúdo do link muda, o sistema operacional recebe a notificação (mesmo com a aba fechada — Service Worker). Badge âmbar no card até o usuário clicar pra dispensar. Lista "Atualizações recentes" na sidebar. ADR-23, ADR-24. |
| **Captura via extension**  | Pin do popup MV3 no Chrome/Edge. Clique → URL e título preenchidos → escolhe tag → salva.     |
| **Import**                 | Drag-drop do `bookmarks.html` ou `.json` → backend cria links idempotentemente.               |
| **Export / Backup**        | Botão "Export" → download em Netscape HTML (reimportável no Chrome) ou JSON v2. Card "Backup completo" gera ZIP DB+MinIO; restore com 3 modos de conflito. |

## Out of scope

- Compartilhamento de links com outros usuários (não há outros usuários).
- Histórico granular de visitas (só agregados a partir de `click_log` — sem trilha por sessão / por device).
- Lembretes / due-dates em links (notificações de change-detection ≠ lembretes manuais).
- Search semântica via embeddings.

## Critérios de sucesso (definição de "MVP pronto")

1. `make up` sobe `db + backend + web` em <30s num laptop Mac/Linux limpo.
2. Criar tag → criar link com essa tag → clicar via SPA → contador incrementa.
3. URL preview popula `og_image_url` em até 5s pra um link público (ex: news.ycombinator.com).
4. Import de `bookmarks.html` real do Chrome de 100+ links roda em <10s.
5. Export Netscape HTML é reimportável no Chrome sem erro.
6. Extension carregada como unpacked salva uma aba e ela aparece no SPA dentro de 1s (TanStack Query refetch).
7. `docker compose down && up` preserva todos os dados (volume nomeado).
8. `make psql` abre prompt direto no banco.
