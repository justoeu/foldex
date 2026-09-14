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

## Diagnóstico

Abra **Actions → ci → SonarQube Quality Gate** e o painel do projeto. Um erro de autenticação pede conferir o secret e sua validade; um relatório ausente pede conferir os jobs de testes da mesma execução. Falha do Quality Gate pede examinar as condições no SonarQube, sem reduzir os gates para deixar o workflow verde.

Para validar caminhos localmente depois de gerar os relatórios:

```bash
python3 -m unittest discover -s .github/scripts -p 'test_*.py'
python3 .github/scripts/sonar-coverage.py web backend/coverage.out
```
