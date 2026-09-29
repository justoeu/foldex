# Carga com k6

A suíte mede a API de uma instância **já no ar**. Ela não sobe Postgres, não entra no CI e não roda sozinha. Quem dispara é uma pessoa, quando quer procurar lentidão, cota estourada ou consulta sem índice.

O alvo padrão é o backend local, `http://127.0.0.1:9089`, sem passar pelo nginx. Para medir o caminho completo (TLS, redirect, proxy), aponte `K6_BASE_URL` para `https://localhost:9444`. Não aponte para uma instância compartilhada sem combinar antes: os fluxos de escrita criam e apagam linhas, e o perfil `stress` existe para bater na cota de propósito.

## O que cada fluxo faz

| Fluxo | Login | O que chama | Para que serve |
|---|---|---|---|
| `smoke` | não | `GET /healthz`, `GET /api/auth/me` anônimo, `GET /go/k6-no-such-slug`, `GET /api/entries` sem sessão | O processo responde, o anônimo leva 401 na biblioteca e 404 num slug que não existe. `/api/auth/me` anônimo é 200 de propósito. |
| `session` | uma vez | `GET /api/auth/me` | Sessão reaproveitada. Não repete login. |
| `library-read` | uma vez | entries (página, `sort=alpha`, `sort=clicks`, `q=k6`), counts, links, recent-changes, notes, folders `fields=minimal`, tags, `by-url` de um link que a lista devolver | A grade, a busca e a ordenação por clique. É o caminho quente. |
| `library-write` | uma vez | cria e apaga tag, pasta, link e nota; faz um PATCH no link | Escrita real, com a linha removida no fim da iteração. Cria preview na fila ao salvar o link. |
| `stats` | uma vez | summary, daily, top, tags, dashboard | Agregações de clique. |
| `activity` | uma vez | `GET /api/activity` com limite 25, 50 e 100 | A trilha da própria conta. |
| `settings` | uma vez | `GET /api/settings/master-password` e `/api/auth/me` | Leitura de conta. Não grava senha. |
| `admin` | uma vez | users, metrics, audit | 200 para admin, 404 para quem não é. Os dois são resposta saudável. |
| `redirect` | não | `GET /go/k6-no-such-slug` sem seguir o redirect | O handler público. 404 é o esperado. |
| `export` | uma vez | **uma** `GET /api/export?format=json` | Dump da biblioteca. Conta no balde caro (padrão 20 por hora). |
| `mixed` | uma vez | leitura de biblioteca, stats, activity e settings, em peso | Um uso misturado, sem escrita. |
| `capacity` | uma vez | 5 minutos. A cada minuto sobe o ritmo de GET e o de escrita, os dois ao mesmo tempo | Achar o platô: o minuto em que aparecem 429, timeout ou 5xx. |
| `orders-write` | uma vez | rampa até 1500 `POST /api/links` por minuto, segura 2 minutos | Escrita sustentada de um link por iteração. A cota padrão de 120/min precisa estar acima de 1500 nesse intervalo. |
| `orders-read` | uma vez | rampa até 3000 `GET /api/links?limit=50` por minuto, segura 2 minutos | A lista da conta, uma chamada por iteração. |
| `orders-mixed` | uma vez | rampa até 1000 criações e 2000 listas por minuto, juntas, segura 2 minutos | Os dois ao mesmo tempo. A cota de escrita precisa estar acima de 1000/min. |
| `surge-write` | uma vez | rampa até 10000 `POST /api/links` por minuto, segura 2 minutos | O mesmo formato de `orders-write`, num ritmo que passa do teto compilado de 6000/min. A cota da conta precisa estar acima de 10000 nesse intervalo. |
| `surge-read` | uma vez | rampa até 30000 `GET /api/links?limit=50` por minuto, segura 2 minutos | A lista da conta, uma chamada por iteração. |
| `surge-mixed` | uma vez | rampa até 8000 criações e 20000 listas por minuto, juntas, segura 2 minutos | Os dois ao mesmo tempo. A cota de escrita precisa estar acima de 8000/min. |

Fora da suíte, de propósito:

- Login em loop. Cada tentativa espera no mínimo 250 ms e o balde por IP trava a origem. Um único login acontece no `setup`, antes dos VUs.
- `POST /api/links/url-metadata` e captura de screenshot. Os dois saem para a rede e entram no balde caro.
- Importação de arquivo, restore de backup e download de artefato.
- Conta com segundo fator. O login para no desafio e o script recusa seguir, para não gastar um código.

No `setup` dos fluxos autenticados há um único `POST /api/links` **sem** o header CSRF. A resposta saudável é 401 ou 403, não 500. Isso não se repete no laço.

## Instalação

```bash
brew install k6
k6 version
```

## Execução

Na raiz do repositório, num terminal:

```bash
./load/k6/run.sh library-read
```

A primeira pergunta é se deve criar ou reutilizar o usuário de teste `k6-load@foldex.local`. **s** tenta o login dessa conta. Se a conta não existe, a senha não confere ou ela parou num segundo fator, o script cria de novo: editor, sem segundo fator, senha nova em `load/k6/.test-user` (modo `600`, fora do git). No fim ele pergunta se apaga essa conta. **Enter** nas duas perguntas deixa tudo como está.

A conta só existe para a API local (`127.0.0.1`, `localhost`). O Postgres tem de ser o desta máquina: serviço `db` / `foldex-db` na rede do container, ou uma porta publicada (`127.0.0.1`, `host.docker.internal`). Outro host é recusado, e a senha do teste não sai por proxy. Para outro alvo, responda **n** e exporte a conta você mesmo. Sem terminal (pipe, CI), as perguntas não aparecem: `K6_TEST_USER=1` usa ou cria, `K6_TEST_USER=0` pula, `K6_DELETE_TEST_USER=1` apaga no fim.

```bash
# sem conta de teste: só o que é público, ou uma conta que você já tem
K6_TEST_USER=0 ./load/k6/run.sh smoke

export K6_EMAIL='load@example.com'
export K6_PASSWORD='…'
export K6_TEST_USER=0

./load/k6/run.sh library-read
./load/k6/run.sh stats
./load/k6/run.sh activity
./load/k6/run.sh settings
./load/k6/run.sh admin
./load/k6/run.sh session
./load/k6/run.sh mixed
./load/k6/run.sh library-write
./load/k6/run.sh export
./load/k6/run.sh redirect
```

O mesmo pelo Make: `make load-k6 FLOW=library-read`.

### Perfis

O perfil manda em VUs e duração quando você não passou os dois. Dá para cobrir com `FOLDEX_K6_VUS` e `FOLDEX_K6_DURATION`. Os nomes `K6_VUS` e `K6_DURATION` são do próprio k6 e apagam os cenários do script.

| `K6_PROFILE` | VUs | Duração | Quando |
|---|---|---|---|
| `smoke` (padrão) | 1 | 15s | Ver se o script e a instância conversam. |
| `read` | 8 | 1m | Leitura concorrente. |
| `write` | 1 | 30s | Escrita abaixo da cota. |
| `stress` | 20 | 2m | Só com `K6_I_MEAN_IT=1`. 429 aqui é a cota funcionando. |
| `capacity` | rampa de 5 min | 5 min | `./load/k6/run.sh capacity`. Um degrau por minuto. GET sobe 20→50→100→200→400 iterações/s. Escrita sobe 12→24→48→96→192 iterações/min. |

```bash
K6_PROFILE=read K6_EMAIL=… K6_PASSWORD=… ./load/k6/run.sh library-read
K6_PROFILE=write K6_EMAIL=… K6_PASSWORD=… ./load/k6/run.sh library-write
K6_I_MEAN_IT=1 K6_PROFILE=stress K6_EMAIL=… K6_PASSWORD=… ./load/k6/run.sh library-read
```

`export` ignora o perfil e faz uma única requisição.

A escrita pausa para ficar perto de 90 mutações por minuto **na conta inteira**, abaixo do padrão de 120. Vários VUs dividem esse teto. O perfil `stress` não pausa desse jeito: espere 429 e leia `Retry-After`. Um 429 no perfil `write` ou `read` é falha da suíte, não “a API aguentou”.

### Cookies

O login devolve `fx_at` e `fx_csrf`. O script manda o cookie e copia o CSRF para `X-Foldex-CSRF` em toda requisição autenticada. Se `K6_BASE_URL` for `http://` e `AUTH_PUBLIC_URL` for `https://`, o navegador (e o k6) descartam o cookie `Secure` e o login parece ter funcionado sem sessão. Use o backend direto em `:9089` ou a origem HTTPS de verdade.

## Como ler o resultado

O resumo também sai no terminal: por tag `name`, `avg`, `med`, `p(95)`, `p(99)` e `max`. Quatro taxas dizem se a corrida quebrou:

| Métrica | O que conta | Limiar |
|---|---|---|
| `unexpected_status` | status fora do conjunto saudável daquele pedido, inclusive timeout | < 1% |
| `timeouts` | o cliente desistiu aos 30 s, o mesmo prazo do axios da interface (`FOLDEX_K6_TIMEOUT`) | < 1% |
| `server_errors` | HTTP 5xx | < 1% |
| `quota_limited` | HTTP 429 | informativo. No `capacity` a escrita passa da cota de propósito, então 429 conta aqui e não como falha inesperada |

O `p(95)` de `http_req_duration` fica abaixo de 800 ms na leitura e de 1,5 s na escrita. No `stress` e no `capacity` o limiar de latência sai: o `capacity` existe para subir até aparecer erro.

Cada iteração de leitura faz cerca de dez GETs, então 20 iterações/s são cerca de 200 GETs/s e 400 iterações/s são cerca de 4 mil. Cada iteração de escrita faz 7 mutações. A cota da conta é 120 mutações por minuto, perto de 17 iterações de escrita por minuto: o 429 deve aparecer no segundo minuto. Timeout e 5xx, se vierem, são o platô do processo, não o da cota. O pool do backend é 16 conexões. O `max_connections` é o da instância sob teste.

O envio ao Prometheus fica desligado até existir `K6_PROMETHEUS_RW_SERVER_URL`. O lugar disso é `load/k6/env.local`, copiado de `load/k6/env.example` (o arquivo local não entra no git). Com a URL definida, o `run.sh` usa `-o experimental-prometheus-rw`. `K6_GRAFANA=0` deixa só o terminal. `K6_GRAFANA=1` sem URL encerra a corrida, para ela não seguir com o painel em branco. `K6_GRAFANA_URL` é a base do Grafana; o painel desta suíte tem uid `foldex-k6`. No topo, o seletor **execução** é o `testid` daquela corrida (`smoke-20260927211604`, por exemplo). Deixe **execução** e **fluxo** em All para a corrida nova aparecer na borda direita. Abaixo da latência, o mesmo painel mostra o Go, o pool, o Postgres e a máquina que esse Prometheus já coleta da instância sob teste. Isso descreve essa instância, não o processo k6.

O Prometheus que recebe a escrita precisa de `--web.enable-remote-write-receiver`. Sem o receptor a API responde 404 e o Grafana não tem série para desenhar.

O que fazer com um `p(95)` alto, **depois** de repetir o mesmo fluxo uma segunda vez (a primeira paga cache frio):

| Tag | Consulta | Índice que deve aparecer no plano |
|---|---|---|
| `GET /api/entries` | união `link` + `note` por `user_id`, página, depois cliques da página | `link_user_created_idx`, `link_user_pinned_created_idx`; cliques da página em `click_log_user_entity_idx` |
| `GET /api/entries?sort=alpha` | `lower(title)` por usuário | `link_user_title_lower_idx` |
| `GET /api/entries?sort=clicks` | `entity_click_stats` por usuário | a chave da projeção `(user_id, entity_kind, entity_id)` |
| `GET /api/entries?q=` | `ILIKE` / trigram em título e URL | `link_user_title_trgm`, `link_user_url_trgm` |
| `GET /api/stats/*` | agregação de `click_log` por usuário e tempo | `click_log_user_clicked_idx` |
| `GET /api/activity` | `audit_log` do próprio ator, `ORDER BY id DESC` | índice de ator na trilha; se o plano for varredura, o `EXPLAIN` é a evidência para um índice novo |

Para confirmar, com a instância quieta:

```bash
# no psql da instância, não no meio da carga
EXPLAIN (ANALYZE, BUFFERS)
SELECT id FROM link WHERE user_id = 1 ORDER BY created_at DESC LIMIT 50;
```

Um índice novo só entra com migração **depois** desse `EXPLAIN`. A suíte não altera schema. Os índices da tabela acima já existem nas migrações `000017` e `000018`; uma carga lenta com eles no plano é CPU, pool ou disco, não “faltou índice”.

`http_req_failed` do k6 conta qualquer status ≥ 400. Vários fluxos esperam 401, 403 ou 404. Não use essa taxa sozinha. Use `unexpected_status` e os checks por tag.

## Sobra de uma escrita interrompida

Cada iteração apaga o que criou. Se o processo morrer no meio, ficam linhas cujo nome ou título começa por `k6-`. Apague pela UI ou pela API com a mesma conta. O link também enfileira um preview: uma corrida curta não varre a web; uma corrida `stress` de escrita sim, e ainda por cima leva 429.

## O que esta suíte não faz

Não compara duas versões sozinha. Cada corrida ganha um `testid` (`fluxo-AAAAMMDDhhmmss`). No Grafana, escolha essa execução. O terminal continua imprimindo o mesmo resumo. Não mede o nginx, a menos que `K6_BASE_URL` seja a origem do web.

## Corridas encadeadas

Os fluxos acima são um de cada vez. `scripts/k6-appcheck.sh` e `scripts/k6-surge-run.sh` chamam o `run.sh` em sequência. `scripts/k6-gate-run.sh` executa `scripts/k6_gate.js`, que separa o 503 do timeout. Logs, a URL do banco e o snapshot da cota ficam em `/tmp`, nunca no repositório. A cota volta ao valor anterior no fim, e a conta `k6-load@foldex.local` é apagada.

| Script | O que faz |
|---|---|
| `scripts/k6-appcheck.sh` | A superfície inteira no backend que já está no ar, depois os pedidos e a rampa. Até a capacidade, a cota armazenada não muda. Nos pedidos e na rampa ela sobe para 6000/min, o teto compilado, e a escrita acima disso recebe 429. |
| `scripts/k6-surge-run.sh` | Para o backend local, sobe um processo com o teto compilado temporário de 20000/min e roda as três rampas. O arquivo no repositório continua em 6000. |
| `scripts/k6-gate-run.sh` | O mesmo, com o binário da worktree de admissão (`FOLDEX_ADMISSION_ROOT`, senão `../foldex-admission`) e o script `scripts/k6_gate.js`, que conta 503 à parte. |

`scripts/k6-watch.sh /tmp/foldex-appcheck` fica quieto até a corrida terminar ou gravar um alerta. `K6_BASE_URL` é a origem da API (padrão `http://127.0.0.1:9089`). `FOLDEX_K6_BACKEND`, `FOLDEX_K6_POSTGRES` e `FOLDEX_K6_RUN_DIR` trocam o container, o Postgres amostrado e o diretório de log. Sem `FOLDEX_K6_POSTGRES`, a amostra de CPU do banco fica de fora. O certificado vem do mount que o container atual já usa. Os endereços de Grafana e Prometheus ficam em `load/k6/env.local`.
