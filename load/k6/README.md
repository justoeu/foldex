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

Na raiz do repositório:

```bash
# sem credencial: só o que é público
./load/k6/run.sh smoke

# o restante precisa de uma conta SEM segundo fator
export K6_EMAIL='load@example.com'
export K6_PASSWORD='…'

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

O k6 imprime, por tag `name`, `avg`, `med`, `p(95)`, `p(99)` e `max`. A métrica `unexpected_status` é a fração de respostas fora do conjunto saudável daquele fluxo. O limiar padrão é menos de 1%. O `p(95)` de `http_req_duration` fica abaixo de 800 ms na leitura e de 1,5 s na escrita. No `stress` o limiar de latência sai, porque a cota responde na hora e o resto pode enfileirar.

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

Não compara duas versões sozinha. Guarde o resumo (`k6 run --summary-export /tmp/k6.json`, se quiser) e diff manual. Não abre o browser. Não mede o nginx, a menos que `K6_BASE_URL` seja a origem do web.
