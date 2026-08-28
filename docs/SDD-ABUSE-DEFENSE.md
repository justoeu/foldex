# SDD — Defesa contra abuso: rate limiting, força bruta e exaustão de recursos

> Software Design Document. Status: **Draft · v1.0 · 2026-08-28**
> Owner: foldex
> Related ADRs: **ADR-30/31/37** (sessões, convites, segundo fator), **ADR-46** (trilha de auditoria e lista de bloqueio) — em `docs/ARCHITECTURE.md`.
> Invariantes tocados: INV-007, INV-010, INV-041, INV-089, INV-155, INV-176, INV-178.
>
> **Escopo.** Como esta instância limita tentativas, absorve picos e sobrevive a um
> cliente hostil. **Fora de escopo:** DDoS volumétrico (§2.3 explica por quê), WAF,
> e qualquer coisa que exija infraestrutura fora da máquina do operador.

---

## 1. Origem deste documento

A conversa começou num defeito de configuração e virou uma pergunta de projeto.

A tela de auditoria (ADR-46) passou a gravar o endereço de cada evento, e o primeiro
smoke test na instância local mostrou que toda requisição vinda pelo nginx era registrada
como `192.168.107.5` — o endereço do próprio container `foldex-web`. `TRUSTED_PROXY_IPS`
não o inclui, então o balde de rate limit por IP colapsa num orçamento único.

A pergunta seguinte foi a certa: **olhar só para o IP não é frágil?** Empresas põem
centenas de pessoas atrás de um NAT. A sugestão foi compor a chave com `IP + e-mail +
User-Agent`.

Duas metades, opostas:

- **O User-Agent na chave tornaria o limitador pior**, e §4.1 argumenta isso.
- **A preocupação com NAT é legítima**, e o desenho atual responde a ela pela metade.

Este documento existe porque a segunda metade merece um projeto, não um remendo.

---

## 2. Modelo de ameaça

### 2.1 O que esta instância é

Um gerenciador de bookmarks **self-hosted, multi-usuário**, tipicamente numa máquina só,
com Postgres local, `BACKEND_BIND=127.0.0.1` por padrão e nginx na frente. A base de
usuários realista vai de **1 a algumas dezenas**. Não há CDN, não há balanceador, não há
segundo nó.

Isso não é uma desculpa para defesas fracas — é o que define **quais** defesas fazem
sentido. Um rate limiter distribuído com Redis seria mais engenharia de operação do que o
produto inteiro.

### 2.2 Adversários que importam

| # | Adversário | Objetivo | Alcance |
|---|---|---|---|
| **A1** | Roteiro automatizado na internet | Entrar em qualquer conta (spray, credenciais vazadas) | Rede |
| **A2** | Ex-usuário / conta desativada | Voltar a entrar, ou negar serviço por rancor | Rede + conhece endereços |
| **A3** | Usuário autenticado hostil | Consumir os recursos da instância (é vizinho dos outros tenants) | Sessão válida |
| **A4** | Visitante de um link público | Abusar de `/go/{slug}` e `/n/{slug}`, que não pedem sessão | Rede |
| **A5** | Administrador curioso | Ler conteúdo de outra conta | Sessão privilegiada |

**A5 é tratado pelo ADR-46 e pelo INV-045** (split de leitura em SQL) e não se repete aqui.
**A3 é o menos coberto hoje** e é o foco de §5.3.

### 2.3 Por que DDoS volumétrico está fora de escopo

Um flood de saturação de banda **não é defensável nesta camada**, e fingir que é seria
pior que admitir. Ele se resolve upstream — no provedor, num Cloudflare, num scrubbing
center — todos fora da máquina do operador. O que **está** no escopo, e é o que a maioria
das pessoas quer dizer com "DDoS" na prática:

- **Exaustão de recursos com pouco tráfego** — poucas requisições caras derrubando a
  instância (§5.3). Este é o vetor real para um serviço deste tamanho.
- **Amplificação** — fazer a instância trabalhar por terceiros (SSRF do preview, já
  coberto pelo INV-079/080).
- **Negação por lockout** — trancar usuários legítimos abusando dos próprios limitadores
  (§4.2). O NAT é o caso acidental disso.

---

## 3. Estado atual (medido, não presumido)

### 3.1 Os onze limitadores em memória

`internal/pkg/attemptlimit` conta **falhas consecutivas por chave**, com API
reserve-then-commit (`Begin` → `CommitFail`/`CommitSuccess`/`Release`) — que é o que
segura o teto sob concorrência: um check-then-act deixaria N tentativas paralelas lerem a
mesma contagem pré-teto e passarem todas.

| Chave | Teto | Lockout | Superfície |
|---|---|---|---|
| `loginByIP` | 20 | 15 min | `POST /api/auth/login` |
| `loginByEmail` | 5 | 15 min | idem, por conta resolvida |
| `bootstrapIP` | 5 | 1 h | `POST /api/auth/bootstrap` |
| `inviteIP` | 20 | 1 h | lookup/accept de convite |
| `pwResetIP` | 10 | 1 h | `POST /api/auth/password/forgot` |
| `pwResetEmail` | 3 | 1 h | idem, por conta |
| `stepUpUser` | 5 | 15 min | TOTP em sessão viva |
| `stepUpPasswordUser` | 5 | 15 min | senha em sessão viva |
| `availabilityUser` | 60 | 5 min | sonda de username |
| `oauthIP` | 30 | 1 h | callback do Google |
| `folders.unlock` | (pkg próprio) | — | senha de pasta |

**Estado é só memória.** Um restart levanta todo lockout — aceitável porque cada
superfície tem um segundo controle durável atrás dela: o unlock paga bcrypt por tentativa,
e o login tem o balde por e-mail **mais** o contador de tentativas do desafio, que vive no
banco justamente para que um restart não zere orçamento de segundo fator (INV-010).

O INV-155 obriga todo `*attemptlimit.Limiter` do `Handler` a aparecer em `limiters()`, com
teste reflexivo: um balde fora do sweep só cresce, e vira lockout de 5 minutos que a conta
não fez nada para merecer.

### 3.2 O que já está bem resolvido

- **Login é byte-idêntico** para e-mail desconhecido, senha errada e conta desativada
  (INV-041), com `burnDummyHash` e piso de duração. Sem oráculo de enumeração.
- **Chave do balde de e-mail é normalizada e resolvida** — `A@x.com`, `a@x.com ` e o
  username da mesma conta caem no mesmo orçamento, em vez de 5 tentativas novas por
  grafia.
- **Corpo de requisição limitado por path** (`defaultBodyLimit`, INV-089: 64 KiB para JSON,
  ceilings maiores para upload/import).
- **SSRF com dupla checagem** e faixas de metadata sempre bloqueadas (INV-079/080).
- **Workers de fundo com teto de concorrência** derivado do `resourcebudget`.
- **`X-Forwarded-For` só de proxy configurado** (INV-007) — e o ADR-46 tornou a
  procedência visível na trilha.

### 3.3 As lacunas, ordenadas por risco real

| # | Lacuna | Adversário | Severidade |
|---|---|---|---|
| **G1** | Balde de IP conta **profundidade**, não **largura** — 20 erros de uma pessoa parecem 20 contas sondadas | A1, A2 | **Alta** |
| **G2** | Nenhum limite na API **autenticada** — sessão válida = requisições ilimitadas contra um pool de 16 conexões | A3 | **Alta** |
| **G3** | `/go/{slug}` e `/n/{slug}` são públicos e **sem limitador nenhum**; cada acerto escreve em `click_log` | A4 | **Média** |
| **G4** | nginx **não tem `limit_req` nem `limit_conn`** — nada absorve pico antes do Go | A1, A4 | **Média** |
| **G5** | `TRUSTED_PROXY_IPS` vazio por padrão colapsa o balde de IP quando há proxy | A1 (acidental) | **Média** |
| **G6** | Lockout só em memória: restart do container zera todos | A1 | **Baixa** |
| **G7** | Sem teto global de conexões concorrentes por origem | A3, A4 | **Baixa** |

---

## 4. Decisões de projeto

### 4.1 O User-Agent NÃO entra em nenhuma chave de rate limit

**Decisão: recusado.**

O `User-Agent` é um cabeçalho que **o cliente escreve**. Com a chave em `IP + UA`, o
atacante manda um UA diferente por tentativa — dimensão livre e infinita — e ganha
**20 tentativas novas por requisição**. O balde deixa de existir.

É exatamente a falha que o `trustedProxyRealIP` já documenta para o `X-Forwarded-For`:
*"an attacker rotates one header value per attempt and never trips a cap"*. Repetir o
defeito numa chave nova, depois de tê-lo corrigido numa, seria o pior tipo de regressão.

**A regra que sai daqui, e que este documento pede que seja invariante:**

> Nenhuma entrada controlada pelo cliente pode compor uma chave de rate limit. Toda
> dimensão que o cliente escolhe livremente é uma saída do próprio balde.

IP e e-mail passam no teste porque **nenhum dos dois é grátis de trocar**: o IP custa
infraestrutura, e o e-mail é o alvo — trocá-lo abandona a conta que se quer invadir.

O User-Agent continua sendo **gravado** na trilha (INV-176), onde é útil justamente por não
ter autoridade nenhuma: "mesma conta, dispositivo novo" é uma pergunta que investigações
fazem. Registrar ≠ confiar.

### 4.2 O balde de IP passa a contar LARGURA, não profundidade

**Decisão: aceita. É a correção central deste documento.**

Os dois baldes respondem perguntas diferentes e hoje contam a mesma coisa:

| Balde | Pergunta | Hoje | Proposto |
|---|---|---|---|
| e-mail | *"alguém está martelando ESTA conta?"* | falhas consecutivas | **inalterado** |
| IP | *"esta origem está varrendo MUITAS contas?"* | falhas consecutivas | **contas distintas falhadas** |

Hoje, 20 erros de senha da mesma pessoa e 20 contas diferentes sondadas são
indistinguíveis para o balde de IP. São coisas completamente diferentes:

- **Uma pessoa errando a própria senha** é ruído. O balde de e-mail (5) já a segura, e ela
  não deveria consumir orçamento de mais ninguém.
- **Uma origem tocando muitas contas** é spray. É o único sinal que o balde de IP existe
  para pegar.

Contando **e-mails distintos falhados por origem**, um escritório inteiro atrás de um NAT
errando as próprias senhas nunca encosta no balde de IP — cada pessoa é um e-mail, e cada
uma tem o próprio orçamento de 5. E um spray tropeça na quinta conta em vez da vigésima
tentativa.

**Isto resolve G1 e o problema de NAT ao mesmo tempo**, sem enfraquecer nada: a defesa
contra força bruta em conta específica é do balde de e-mail, que não muda.

**A informação já existe.** O sinal de risco do ADR-46 calcula
`count(DISTINCT target_email)` por IP — o dado está na trilha, só não realimenta o
limitador.

**Forma proposta:** `attemptlimit` ganha um modo que conta **membros distintos de um
conjunto por chave**, e não um contador escalar. O login registra
`loginByIP.CommitFailFor(ipKey, emailBucket)`. Isso mantém uma implementação de limitador,
como o pacote já se propõe a fazer, em vez de uma segunda estrutura paralela.

**Trade-off aceito e declarado:** um atacante que martela **uma só conta** de um só IP
deixa de tropeçar no balde de IP aos 20 — passa a ser segurado só pelo balde de e-mail,
aos 5. Como 5 < 20, **ele é parado antes**, não depois. Não há perda.

**Pisos:** o novo teto de contas distintas deve ser baixo (proposta: **5 contas / 15 min**,
mesma janela). Uma pessoa não erra em cinco contas alheias por acidente.

### 4.3 Sem Redis, sem armazenamento distribuído

**Decisão: manter em memória.**

Um limitador distribuído resolveria G6 (restart zera lockouts), ao custo de um serviço novo
no compose, uma dependência nova, e um modo de falha novo — *"o Redis caiu, e agora,
falha aberto ou fechado?"*. Para uma instância de um nó, o restart é raro e o segundo
controle durável já existe (§3.1).

**Corolário que este documento pede que seja explícito:** se um dia houver segundo nó, os
limitadores em memória viram *por nó* e todos os tetos efetivamente dobram. Isso deve ser
um item de bloqueio no ADR que introduzir o segundo nó, não uma descoberta.

### 4.4 O que fazer no nginx: `limit_req`, e só

**Decisão: aceita, com escopo estreito.**

nginx hoje não tem `limit_req` nem `limit_conn`. Uma zona por IP nas rotas de auth absorve
o pico **antes** do Go — antes do bcrypt, antes do pool de conexões, antes do piso de
duração do login. É a defesa mais barata que existe aqui, e é a única camada que roda antes
de qualquer alocação do backend.

Estreito de propósito:

- `limit_req` em `/api/auth/*` com burst generoso (o SPA faz rajadas legítimas em
  `/api/auth/me` e `/api/auth/refresh`).
- **Não** limitar `/api/*` inteiro no nginx: a API autenticada tem padrões de uso
  irregulares (import, backup, colar 50 links) e um teto na borda produziria falso
  positivo onde o backend tem contexto para decidir melhor (§5.3).
- `limit_conn` fica de fora: com HTTP/2 multiplexado ele conta conexões, não requisições, e
  o resultado engana.

**Ressalva importante:** isso só funciona se o nginx enxergar o endereço real. Numa
instalação atrás de outro proxy, `limit_req_zone $binary_remote_addr` limita o proxy —
mesmo defeito do G5, uma camada acima.

### 4.5 `TRUSTED_PROXY_IPS` ganha um default que corresponde ao compose

**Decisão: aceita.**

G5 é acidental e afeta toda instalação padrão: o compose põe nginx na frente, e a
configuração que faria o backend acreditar nele vem vazia. O aviso de boot existe mas é
silencioso em bind loopback — que é justamente o default.

Proposta: o `.env.example` e o compose passam a trazer a rede do compose como valor
padrão, com comentário explicando o que ele faz e quando mudar. **Não** deduzir a rede em
runtime: adivinhar em quem confiar é a decisão que o INV-007 tira das mãos do código de
propósito.

---

## 5. Desenho proposto

### 5.1 Camadas, e o que cada uma pode decidir

```
                          o que sabe            o que decide
  ┌────────────────────────────────────────────────────────────────┐
  │ 1. nginx              endereço, path        pico bruto em /auth│  ← §4.4
  ├────────────────────────────────────────────────────────────────┤
  │ 2. blocklistGate      endereço              banido permanente  │  ← ADR-46
  ├────────────────────────────────────────────────────────────────┤
  │ 3. defaultBodyLimit   path                  tamanho do corpo   │  ← INV-089
  ├────────────────────────────────────────────────────────────────┤
  │ 4. attemptlimit       endereço + conta      força bruta        │  ← §4.2
  ├────────────────────────────────────────────────────────────────┤
  │ 5. cota autenticada   principal             abuso de recurso   │  ← §5.3 (NOVO)
  ├────────────────────────────────────────────────────────────────┤
  │ 6. budget no banco    desafio               segundo fator      │  ← INV-010
  └────────────────────────────────────────────────────────────────┘
```

Cada camada decide **só o que ela sabe**. A borda não conhece contas; o limitador de
tentativas não conhece custo de query; a cota autenticada não deve reimplementar força
bruta. Misturar as responsabilidades é como se chega a chaves com User-Agent dentro.

### 5.2 Mudanças no `attemptlimit` (G1)

`Limiter` ganha um segundo modo, **contagem por conjunto**:

```go
// CommitFailFor registra uma falha atribuída a um MEMBRO. O teto passa a valer
// sobre |membros distintos|, não sobre o número de tentativas.
func (l *Limiter) CommitFailFor(key, member string)
```

- `login:ip:<addr>` passa a usar `CommitFailFor(ipKey, emailBucket)`, teto **5 / 15 min**.
- `login:em:<conta>` fica **exatamente como está** — escalar, 5 / 15 min.
- O conjunto por chave é limitado (proposta: 64 membros) e descartado junto com a entrada
  no sweep. **Sem teto, o próprio limitador vira o vetor de exaustão de memória** — a
  correção não pode abrir o buraco que fecha.
- O sweep (INV-155) já cobre a entrada; o conjunto morre junto.

**Testes obrigatórios** (o pacote tem garantias de concorrência a preservar):
- 20 falhas na MESMA conta do mesmo IP não trancam o IP (é o caso do NAT).
- 5 contas distintas do mesmo IP trancam.
- `Begin`/`CommitFailFor` concorrentes não estouram o teto (o mesmo argumento de
  reserve-then-commit).
- O conjunto respeita o teto de membros e é liberado no sweep.

### 5.3 Cota da API autenticada (G2) — a lacuna mais séria

Hoje, **uma sessão válida não tem limite nenhum**. Contra um pool de 16 conexões, um
usuário hostil — ou um script bugado de um usuário legítimo — satura a instância inteira,
e todos os outros tenants sentem. Este é o vetor de "DDoS" que de fato existe aqui (§2.3).

Proposta deliberadamente simples:

- **Uma cota por principal**, não por rota: N requisições mutantes por janela, aplicada no
  grupo privado. Ler é barato; escrever custa.
- **As rotas caras ganham teto próprio e menor**: import, restore de backup, captura de
  screenshot, refresh de preview. São as que fazem I/O externo ou trabalho de CPU por
  requisição.
- **429 com `Retry-After`**, não desconexão: o cliente é legítimo até prova em contrário, e
  precisa poder recuar.
- **O owner não é isento.** Uma isenção seria uma conta capaz de derrubar a instância, e a
  primeira pessoa a tropeçar nela seria o próprio operador rodando um import grande.

**Alternativa considerada e rejeitada:** limitar por consumo de conexão do pool em vez de
contagem de requisições. Mede a coisa certa, mas o feedback chega tarde demais — quando o
pool está saturado o dano já ocorreu — e é muito mais difícil de explicar num 429.

### 5.4 Superfícies públicas (G3)

`/go/{slug}` e `/n/{slug}` não pedem sessão e cada acerto escreve em `click_log`. Um laço
sobre um slug conhecido é escrita ilimitada no banco por um anônimo.

- `limit_req` no nginx cobre o grosso (§4.4), com zona própria e mais generosa: são links
  compartilhados, e um post popular gera rajada legítima.
- **Escrita em `click_log` com coalescência**: dedup por (entidade, endereço) numa janela
  curta. Contagem de cliques é métrica de produto, não contabilidade — perder o segundo
  clique do mesmo visitante em 10 segundos não muda nada que alguém leia, e remove o
  amplificador.

Fora de escopo declarado: `PublicNumericIDs` continua off por padrão (INV-024), então
enumerar por id não é caminho a menos que o operador o abra.

---

## 6. O que este documento NÃO propõe, e por quê

| Rejeitado | Motivo |
|---|---|
| User-Agent na chave | Entrada controlada pelo cliente — §4.1 |
| Fingerprinting de dispositivo | Mesmo defeito, com mais código e uma promessa de privacidade quebrada |
| CAPTCHA | Terceiro na página de login de uma instância self-hosted, com custo de privacidade e acessibilidade que o ganho não paga |
| Redis / limitador distribuído | Um nó — §4.3 |
| Bloqueio automático de IP | O ADR-46 deu o bloqueio **manual** com trilhos, de propósito. Automatizar é entregar a chave da porta a um heurístico que o atacante controla a entrada. Um falso positivo automático tranca o operador |
| Banir por ASN/geografia | Base de dados nova, licença nova, e falso positivo alto para uso legítimo em viagem |
| `limit_conn` no nginx | Com HTTP/2 conta conexão, não requisição — engana mais do que informa |

A linha do bloqueio automático merece ênfase: o INV-178 diz que o modo de falha desse
controle **não é "um bloqueio que não funciona"** — é uma instância que ninguém alcança,
instalada pela pessoa que mais precisava alcançá-la. Um heurístico faria isso sozinho, de
madrugada.

---

## 7. Faseamento

| PR | Escopo | Risco | Depende |
|---|---|---|---|
| **1** | `TRUSTED_PROXY_IPS` com default do compose + doc (§4.5) | Baixo | — |
| **2** | `attemptlimit` conta contas distintas no balde de IP (§5.2) | **Médio** — mexe em autenticação | — |
| **3** | `limit_req` no nginx para `/api/auth/*` e rotas públicas (§4.4) | Baixo | 1 |
| **4** | Cota da API autenticada (§5.3) | **Médio** — pode gerar falso positivo | — |
| **5** | Coalescência de `click_log` (§5.4) | Baixo | — |

PR 2 e PR 4 são os que merecem sweep de agentes e testes de concorrência próprios.
PR 1 é o que resolve o defeito que originou esta conversa e pode ir sozinho, hoje.

---

## 8. Como saber se funcionou

A tela de auditoria do ADR-46 **é o instrumento**, e isso não é coincidência — ela foi
construída para responder exatamente estas perguntas:

- "Origens de acesso" deixa de mostrar uma linha só (valida PR 1).
- O sinal de risco distingue rajada de erro honesto: `count(DISTINCT target_email)` alto é
  spray, `= 1` é alguém que esqueceu a senha (valida PR 2).
- O gráfico por dia mostra se um 429 novo começou a atingir tráfego legítimo (valida PR 4).

**Critério de falha explícito:** se depois do PR 2 ou 4 a trilha registrar lockouts de
contas que não estavam sob ataque, a mudança está errada e volta. Um limitador que tranca
usuário legítimo é pior que um limitador frouxo — ele nega serviço de graça, e ensina o
operador a afrouxá-lo até virar decoração.
