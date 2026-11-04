# SDD — Sidebar Navegar: Pastas + Tags (variante B · Abas)

Status: confirmed · Aprovada via sketch 001 (`.planning/sketches/001-sidebar-pastas-tags/`)
em 03/11/2026 — variante B, com a emenda do owner: ações por aba.

## Objetivo

A sidebar esquerda (hoje 100% tag-cêntrica) passa a apresentar **pastas e tags**
por meio de um controle segmentado `📁 Pastas | # Tags`. Clicar numa pasta
**filtra e entra** nela (breadcrumb + grid filtrado). Pastas protegidas por
senha **não são renderizadas** nesta superfície.

## Requisitos

1. **Segmented control** abaixo do cabeçalho "Navegar" (era "Tags").
   - Aba ativa persiste em `localStorage` (padrão `foldex.sidebar.*`).
   - "Todos os links" e "Atualizações recentes" ficam **fora** das abas,
     sempre visíveis.
2. **Painel Pastas**: árvore de `useFolders` com subpastas indentadas
   (guia vertical, como no sketch), dot colorido por pasta.
   - **Sem badge de contagem** (decisão do owner, 03/11, no review round): a
     projeção slim do backend zera counts de propósito (`repository.go:222`) —
     trazer counts exigiria mudança de backend fora do escopo. Contagens
     continuam nos cards do HomeView.
   - Filtra `has_password === true` **e toda a subárvore** (flag já existe no DTO).
   - Ação **dentro da aba**: "Nova pasta" → callback para o `FolderDialog`
     existente (não há superfície "gerenciar pastas" no app; edição segue nos
     FolderCards do grid).
   - Clicar numa pasta: seleciona a pasta como filtro de contexto + breadcrumb
     na área principal; o grid lista o conteúdo da pasta (inclui subpastas —
     containment ADR-19: listing de pasta mostra seus links/notas diretos E
     das subpastas, conforme comportamento atual de `?parent_id=`).
3. **Painel Tags**: idêntico ao comportamento atual (Frequentes/Outras,
   load-more, seleção multi-tag) com ações "Nova tag"/"Gerenciar" **dentro da
   aba** (movidas do rodapé global).
4. **Rail colapsado**: mostra as ações da aba ativa (aba ativa também
   persiste no estado colapsado); ícone do rail passa a refletir "Navegar".
5. **Drawer mobile**: inalterado no comportamento (overlay ≤768px), herda a
   nova estrutura.
6. **Filtro combinado**: pasta (contexto) + tags (interseção) + busca — a
   pasta é o escopo, tags refinam dentro dela. Trocar de pasta limpa tags? Não:
   tags selecionadas persistem ao trocar de pasta (filtro composto), exceto ao
   usar "Todos os links" (limpa tudo, como hoje).
7. **Breadcrumb** na área principal: `Todos os links / Dev / Frontend`, clicável,
   com badge colorido da pasta atual. Aparece só quando dentro de pasta.
8. **i18n**: chaves novas em en/pt/es (`sidebar.navigate`, `sidebar.folders`,
   `sidebar.new_folder`, `sidebar.manage_folders`, `sidebar.hidden_locked_hint`
   opcional — ver decisão aberta).

## Fora de escopo

- CRUD de pastas novo (usa `FolderDialog` existente).
- Qualquer mudança no backend (todos os endpoints já existem: `GET /api/folders`,
  `GET /api/entries?folder=`, counts).
- Unlock de pastas protegidas na sidebar (elas simplesmente não aparecem).

## Decisões abertas

- Mostrar ou não um hint discreto "N pastas protegidas ocultas" no rodapé do
  painel Pastas. Default: **não mostrar** (superfície limpa); revisitável.

## Aceite

- Sidebar renderiza abas, entra em pasta ao clicar, breadcrumb navega, pastas
  com senha ausentes da lista, ações por aba, rail/mobile preservados.
- Testes vitest cobrindo: troca de aba persistida, filtro de pastas com senha,
  entrada em pasta (contexto + breadcrumb), ações por aba, rail.
- Gates do repo verdes (typecheck, coverage ≥85/80, guard scripts).
