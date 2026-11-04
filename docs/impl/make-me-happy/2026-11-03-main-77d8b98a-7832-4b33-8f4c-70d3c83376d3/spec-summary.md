# Spec summary — Sidebar Navegar: Pastas + Tags (variante B · Abas)

**Meta.** A sidebar esquerda apresenta pastas e tags em abas segmentadas
(`📁 Pastas | # Tags`); clicar numa pasta filtra e entra nela; pastas com senha
não são renderizadas (nem sua subárvore).

**Atores.** Usuário autenticado (SPA); sem superfície nova para addon/API token.

**Contratos.** Nenhum contrato HTTP novo — a feature reusa `GET /api/folders`
(scope null, fields minimal, já carregado em `App.tsx`) e a navegação existente
(`AppNavigation.requestOpenFolder` com gate ADR-28, `?parent_id=` no HomeView).
Contratos de UI: aba persistida em `localStorage[foldex.sidebar.tab]`;
ações por aba; rail colapsado segue a aba ativa.

**Fora de escopo.** CRUD novo de pastas (usa FolderDialog), qualquer mudança de
backend, unlock de pastas protegidas pela sidebar.

**Slice.** Uma slice (`mmh-77d8b98a/slice-1`) — tarefas sequenciais no mesmo
conjunto de arquivos (TagSidebar, AppShell, sidebar/*, i18n).

**Sketch aprovado.** `.planning/sketches/001-sidebar-pastas-tags/` — variante B,
com emenda do owner (03/11): ações vivem dentro de cada aba.
