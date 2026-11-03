# Sketch 001 — Sidebar com Pastas + Tags

**Questão de design:** a sidebar esquerda é hoje 100% tag-cêntrica (seções
Frequentes/Outras + Ações). Onde as pastas entram — e o que acontece ao clicar?

**Requisito fixo do owner:** clicar numa pasta filtra e ENTRA nela; pastas com
senha não aparecem nesta visualização (o flag `has_password` já existe no DTO).

## Variantes

- **A · Seções** — pastas como nova seção colapsável acima das tags (padrão
  `fx-side-sep` existente). Mais conservadora, menor custo de implementação.
- **B · Abas** — segmented control `Pastas | Tags`; sidebar nunca cresce além
  de uma tela com muitas pastas E tags. Aba ativa persiste. **Ações por aba**
  (decisão do owner em 03/11): "Nova pasta/Gerenciar" no painel Pastas,
  "Nova tag/Gerenciar" no painel Tags; rail colapsado segue a aba ativa.
- **C · Drill-down** — home igual a A; ao entrar, a sidebar muda para o
  contexto da pasta (subpastas, tags da pasta, recentes filtradas, voltar).
  Modelo Finder; maior custo (contagens por pasta — `/api/entries/counts`).

## Contraints respeitadas (do código real)

- Tokens/typography idênticos (`foldex.css`): `fx-side-row` grid 16/1fr/auto,
  dot 10px com halo, count `JetBrains Mono` 11px, seções mono 10px caps.
- Cabeçalho muda de "Tags" para "Navegar" nas três variantes.
- Rail colapsado e drawer mobile seguem existindo (não afetados).
- i18n: chaves novas (`sidebar.folders`, `sidebar.navigate`, etc.) em en/pt/es.

## Estado

- [x] Mockup HTML fiel (light + dark, interativo — clique nas pastas)
- [x] Variante aprovada pelo owner — **B · Abas**
- [ ] Wrap-up para skill de findings

Abrir: `index.html` (nav fixa no topo troca A/B/C e liga o modo escuro).
