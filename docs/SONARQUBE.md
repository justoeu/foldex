# SonarQube

O workflow `ci` executa testes e cobertura em pull requests e em pushes na `main`.
Somente um push na `main` envia análise para o [projeto Foldex](https://sonarqube.justoeu.cloud/dashboard?id=foldex).
A edição Community não oferece análise nativa de PRs ou de múltiplas branches.

## Fluxo e custo

1. Os jobs `backend` e `frontend` executam cada suíte uma vez, com os gates de cobertura existentes.
2. Na `main`, publicam apenas `backend/coverage.out` e `web/coverage/lcov.info`, com retenção de dois dias.
3. `SonarQube Quality Gate` baixa os relatórios da mesma execução e normaliza os caminhos para a raiz do checkout.
4. O scanner envia o código e os resultados ao SonarQube e espera até dez minutos pelo Quality Gate. Recusa ou timeout falha o job.

O scanner roda no GitHub; o servidor SonarQube processa o relatório recebido. Os runners padrão são gratuitos para este repositório público ([GitHub](https://docs.github.com/en/actions/concepts/billing-and-usage)). Artefatos e caches têm limites próprios. O push após merge gera testes novos; a cobertura de um PR não é reutilizada para um commit diferente.

Um novo evento para a mesma ref cancela a execução anterior do CI, evitando trabalho obsoleto e um scan antigo enviado depois do mais recente. Um relatório já aceito pelo servidor pode terminar seu processamento mesmo após o cancelamento do runner.

Os builds Docker permanecem exclusivos de PRs. A publicação de imagens continua manual em `release.yml`: o resultado do SonarQube não bloqueia automaticamente esse workflow separado. Não configurar o job SonarQube como check obrigatório de PR, pois ele só roda na `main`.

## Configuração do repositório

Em **Settings → Secrets and variables → Actions**:

| Tipo | Nome | Valor |
| --- | --- | --- |
| Secret | `SONAR_TOKEN` | Token de análise restrito ao projeto `foldex`; nunca usar a senha administrativa |
| Variable | `SONAR_HOST_URL` | `https://sonarqube.justoeu.cloud` |

A chave `foldex` fica em `sonar-project.properties`. O token instalado vence em **13/09/2027**; substituí-lo no secret antes dessa data. Ele só é entregue à etapa de scan na `main`.

O escopo inicial cobre `backend` e `web/src`. A extensão continua com seus testes existentes, mas não integra este primeiro escopo de análise. As exclusões de cobertura acompanham os helpers/boot excluídos do Makefile e do Vitest; os números podem diferir porque Go, Vitest e SonarQube medem cobertura de formas diferentes.

## Escopo de maintainability

**Código de teste não é analisado.** `sonar.exclusions` cobre `*_test.go`, `*.test.*`, `*.spec.*`, `web/src/test/**` e os helpers de teste do backend (`testdb`, `testsupport`, `authctxtest`, `spantest`). A edição Community conta code smells de teste no rating de maintainability sem separá-los dos de produto; a qualidade dos testes fica com os gates locais (cobertura ≥85/80, charter tests de complexidade, sweep de revisão). Os relatórios de cobertura continuam sendo lidos normalmente — eles referenciam arquivos de produção.

**Duas regras estão desativadas nos Quality Profiles, com motivo documentado:**

| Regra | Perfil | Motivo |
| --- | --- | --- |
| `typescript:S6819` (elemento nativo no lugar de `role`) | `justoeu (sem S1135)` | Pede `<dialog>`/`<select>`/`<option>` nativos; INV-121/137/156 decidiram overlays portaled, OTP posicional e listbox própria. |
| `css:S7924` (contraste mínimo) | `justoeu (sem S7924)` (cópia do `Sonar way` vinculada ao projeto) | Dispara sobre tokens deliberados do tema (INV-142); reajustar cor por finding quebraria o design system. |

Reativar qualquer uma das duas exige revisar a invariante correspondente primeiro — o finding não é um bug, é um desacordo documentado com a regra.

**Um item é `won't fix` por decisão:** `typescript:S6479` sobre as linhas de horário do `BackupScheduleEditor`. As linhas editam o próprio valor; uma key derivada do conteúdo remonta o input no meio da digitação e derruba o foco — a identidade da linha É o slot posicional (o próprio `aria-label` a chama de "horário N"). Novas ocorrências da mesma forma (lista posicional editável) merecem a mesma avaliação, não um fix automático.

## Diagnóstico

Abra **Actions → ci → SonarQube Quality Gate** e o painel do projeto. Um erro de autenticação pede conferir o secret e sua validade; um relatório ausente pede conferir os jobs de testes da mesma execução. Falha do Quality Gate pede examinar as condições no SonarQube, sem reduzir os gates para deixar o workflow verde.

Para validar caminhos localmente depois de gerar os relatórios:

```bash
python3 -m unittest discover -s .github/scripts -p 'test_*.py'
python3 .github/scripts/sonar-coverage.py web backend/coverage.out
```
