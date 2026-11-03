# Tasks — docs/SDD-SIDEBAR-FOLDERS.md

- [x] **T-001** Abas segmentadas Pastas|Tags com ações por aba  | tests: 3 | validation: red-green (verified) | immutability: no | wt: 1 | DONE

  Escopo: Remodelar TagSidebar em Navegar: segmented control, painéis, ações migradas para dentro da aba Tags.

  Descrição: Renomeie o cabeçalho da sidebar de Tags para Navegar. Adicione o controle segmentado 📁 Pastas | # Tags abaixo do cabeçalho. A aba ativa persiste em localStorage (chave foldex.sidebar.tab, valores 'folders'|'tags', default 'tags'). 'Todos os links' e 'Atualizações recentes' permanecem fora das abas, sempre visíveis. Mova as ações 'Nova tag' e 'Gerenciar' do rodapé global para dentro do painel Tags. O painel Pastas nesta task é um estado vazio provisório (mensagem i18n).

  Aceite:
  - A sidebar renderiza o segmented control com duas abas e o cabeçalho Navegar.
  - Trocar de aba troca o painel visível; recarregar restaura a última aba ativa.
  - As ações Nova tag e Gerenciar aparecem apenas no painel Tags, não no rodapé global.
  - Os testes existentes de TagSidebar continuam verdes após o re-arranjo.

  Referências: SDD-SIDEBAR-FOLDERS §Requisitos 1,3
- [x] **T-002** Painel de pastas: árvore sem pastas com senha, clique entra na pasta  | tests: 7 | validation: red-green (verified) | immutability: no | wt: 1 | DONE

  Escopo: Novo painel de pastas na sidebar conectado à navegação existente.

  Descrição: Crie o painel Pastas dentro da aba correspondente. Ele recebe a lista plana de todas as pastas (useFolders scope null, fields minimal — já carregada em App.tsx), constrói a árvore por parent_id, remove pastas com has_password=true e toda a sua subárvore, e renderiza cada pasta com dot colorido, nome, contagem (link_count) e subpastas indentadas com guia vertical (padrão fx-side-row + classe nova fx-folder-children). A pasta atualmente aberta (prop openFolder) fica destacada (fx-side-row-active). Clicar numa pasta chama onOpenFolder(id) — AppShell conecta ao requestOpenFolder existente do AppNavigation (gate ADR-28; pastas protegidas nunca chegam aqui, mas o pipe é o mesmo). O rodapé do painel tem a ação Nova pasta (callback para o dialogs.openNewFolder existente — sem diálogo novo; 'Gerenciar pastas' não existe como superfície no app hoje, e criação/edição segue no FolderDialog/FolderCard). Estado vazio com mensagem i18n quando não há pastas sem senha.

  Aceite:
  - Pastas com has_password=true não são renderizadas, nem seus descendentes.
  - Subpastas aparecem indentadas sob a pasta-mãe com guia vertical.
  - Clicar numa pasta invoca onOpenFolder com o id correto; a pasta aberta fica ativa.
  - Nova pasta dispara o callback recebido por props (dialogs.openNewFolder no AppShell).
  - O painel não faz novas requisições HTTP próprias (usa a lista já carregada).

  Referências: SDD-SIDEBAR-FOLDERS §Requisitos 2

  Dependências: T-001
- [x] **T-003** Rail colapsado segue a aba ativa  | tests: 2 | validation: red-green (verified) | immutability: no | wt: 1 | DONE

  Escopo: Atualizar o rail fino da sidebar para o modelo de abas.

  Descrição: No estado colapsado, o ícone do rail reflete Navegar e as ações exibidas são as da aba ativa: aba Tags mantém Nova tag e Gerenciar tags; aba Pastas mostra Nova pasta. O clique nas ações do rail segue os mesmos callbacks/estado da aba expandida.

  Aceite:
  - Rail colapsado na aba Tags mostra as ações de tag (comportamento atual preservado).
  - Rail colapsado na aba Pastas mostra a ação de nova pasta.
  - A aba ativa persiste entre colapsar e expandir.

  Referências: SDD-SIDEBAR-FOLDERS §Requisitos 4

  Dependências: T-002
- [x] **T-004** i18n en/pt/es e verificação de cobertura da slice  | tests: 0 | validation: check (verified) | immutability: no | wt: 1 | DONE

  Escopo: Fechar as chaves de tradução e apurar cobertura sobre o código tocado.

  Descrição: Confira e complete as chaves novas (sidebar.navigate, sidebar.folders, sidebar.tags_tab, sidebar.new_folder, sidebar.manage_folders, sidebar.empty_folders, sidebar.tab tooltip) nos três locales com o mesmo tom das existentes. Rode typecheck e vitest coverage; declare o escopo de cobertura (arquivos tocados) e garanta piso ≥90% statements nos novos/editados, respeitando o gate global do repo (85/80).

  Aceite:
  - Os três locales têm todas as chaves novas, sem fallback pendurado.
  - bun run typecheck verde; cobertura dos arquivos tocados declarada e ≥90% statements.
  - O teste de paridade de permissões/CSS (guard scripts) segue verde.

  Referências: SDD-SIDEBAR-FOLDERS §Requisitos 8, §Aceite

  Dependências: T-003
