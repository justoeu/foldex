# Foldex — Vision

## Problema

Os bookmarks nativos de browser (Chrome/Edge/Firefox) viraram um cemitério: a pasta cresce, ninguém renomeia, ninguém limpa, e a busca é pobre. Pra quem trabalha com ferramentas internas (Jira, Confluence, Looker, dashboards Datadog, Grafana, Verdi, intranet) o problema é pior:

- não dá pra **classificar** um link com várias categorias (um board pode ser "Jira" + "Time Invoices" + "Q3");
- não dá pra ver **quais links são realmente usados** (qual dashboard você abre toda segunda? qual ficou lá fazendo número?);
- não dá pra ter um **link curto compartilhável** (`/go/47`) que ainda te leve pro destino e conte os cliques;
- **importar/exportar** funciona só dentro do mesmo browser e perde toda a metadata útil.

Foldex resolve isso como um **app pessoal self-hosted**: bookmark com cara de produto, organizado por tags livres, com contador de cliques, preview visual e captura via extensão.

## Goals

1. **Listar e abrir links em segundos.** Grid visual com favicon e og:image, filtro instantâneo por texto + tags, atalho `⌘K` pra command palette.
2. **Classificações livres em M:N + organização em pastas 1:N.** Tags coexistem com pastas no estilo iPhone: tag = label (M:N, várias por link), pasta = containment (1:N, link mora em UMA pasta). Drag-and-drop de link sobre pasta move; soltar link sobre outro link cria uma pasta nova com os dois dentro. Detalhes em ADR-19.
3. **Métricas de uso.** Cada clique via `/go/:id` incrementa contador atomicamente; lista pode ser ordenada por "mais usados" / "usados recentemente".
4. **Preview automático.** Ao criar, backend busca `<title>`, `og:image`, favicon. Card renderiza visualmente parecido ao que o link representa.
5. **Import/Export sem fricção.** Aceita o `bookmarks.html` (Netscape) que qualquer browser exporta + um JSON versionado próprio. Export volta no mesmo formato (sem lock-in).
6. **Captura via extensão.** Manifest V3 popup que pré-preenche URL + título da aba atual, escolhe tags, salva.

## Non-goals (v1)

- **Sem multi-user.** Roda como app de uma pessoa, sem login, atrás do `127.0.0.1`.
- **Sem sync entre máquinas.** O dado vive num Postgres local; backup é responsabilidade do usuário (script `pg_dump` sugerido).
- **Sem app mobile nativo.** A SPA é responsiva o suficiente; extensão é desktop only.
- **Sem AI tagging automático.** Sugestão de tags por LLM fica pra v2.
- **Sem deploy cloud.** Sem TLS, sem secrets manager, sem CI. Quando virar multi-user, repensar.

## Target user

Engenheiro/PM que vive em browser, abre dezenas de ferramentas internas por semana, e quer um lugar único pra organizar tudo sem depender da estrutura de pastas do navegador. Caso de uso primário: links pra Jira, dashboards, Confluence, wikis internas etc.

## Experiências core

| Experiência                | Como acontece                                                                                  |
|----------------------------|-----------------------------------------------------------------------------------------------|
| **Bookmarks bar visual**   | Página inicial: grid de cards (folder cards + links soltos), filtro por tag, densidade 3/5/8 colunas. Card mostra favicon, título, og:image, contador. Pasta mostra 2×2 mini-thumbnails do conteúdo. |
| **Pastas iPhone-style**    | Clicar uma pasta entra nela (Esc / botão "← Pastas" volta). URL bookmarkável (`?folder=N`). Drag-and-drop: link → pasta (move), link → link (cria nova pasta com os dois). `⌥F` cria nova pasta. |
| **Command palette**        | `⌥K` abre overlay com busca fuzzy (título + URL + tag). `Enter` abre o link via `/go/:id`.    |
| **New link**               | `⌥N` ou botão `+` abre dialog. URL → cola → backend resolve preview em background.            |
| **Captura via extension**  | Pin do popup MV3 no Chrome/Edge. Clique → URL e título preenchidos → escolhe tag → salva.     |
| **Import**                 | Drag-drop do `bookmarks.html` ou `.json` → backend cria links idempotentemente.               |
| **Export**                 | Botão "Export" → download em Netscape HTML (reimportável no Chrome) ou JSON nosso.            |

## Out of scope

- Compartilhamento de links com outros usuários (não há outros usuários).
- Histórico de visitas (só `click_count` + `last_clicked_at`).
- Pastas aninhadas (folders são flat — 1 nível). Aninhamento ainda fica fora de escopo; tags resolvem o caso multidimensional.
- Notificações, lembretes, due-dates em links.
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
