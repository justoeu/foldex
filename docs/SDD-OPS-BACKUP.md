# SDD — Backup operacional agendado da instância (backup-agent)

> Software Design Document. Status: **Draft · v1.0 · 2026-08-25**
> Owner: foldex
> Related ADRs: **ADR-43** (backup operacional agendado — este documento é o detalhe), **ADR-20** (backup per-user como ZIP), **ADR-42** (matriz RBAC configurável) — todos em `docs/ARCHITECTURE.md`.
> Supersede: a linha "Backup recomendado: cron de `pg_dump` (template em `scripts/backup.sh`)" da seção Deploy local, e o próprio `scripts/backup.sh`.
>
> **Não confundir com [`SDD-BACKUP-RESTORE.md`](./SDD-BACKUP-RESTORE.md)**, que descreve o
> export/restore **por usuário** via `/api/backup/*` (ADR-20, INV-102…107). Aquele é um
> recurso de produto: um usuário leva o SEU conteúdo embora, sem material de auth (INV-105),
> e por isso **não restaura uma instância**. Este documento descreve a outra metade:
> disaster recovery operacional, do banco inteiro e do bucket inteiro, agendado, verificado
> e enviado para fora da máquina.

---

## 1. Visão geral

### 1.1 Problema

Uma instância Foldex guarda tudo em dois lugares: Postgres (conteúdo + contas + credenciais)
e RustFS (screenshots, imagens de nota, objetos de backup per-user). Hoje nada copia esses
dois lugares para fora da máquina:

- O backup per-user (ADR-20) exclui deliberadamente toda tabela de auth. Restaurá-lo exige
  uma instância já de pé, com contas já criadas. Não é DR.
- `scripts/backup.sh` está quebrado desde a separação dos compose files (`docker compose
  exec -T db` sem `-f docker-compose.db.yml` → "no such service"), roda `pg_dump` plain
  `.sql.gz` sem formato custom, não toca o RustFS, não verifica nada, não envia para lugar
  nenhum e depende de o operador editar crontab à mão. Falhou em silêncio e se autocancelou
  — a mesma classe de defeito do INV-033.
- Não existe nenhuma resposta para "o backup de ontem restaura?". Um backup nunca
  restaurado é esperança, não backup.

### 1.2 Decisão

Um quarto binário, **`cmd/backup-agent`**, rodando como serviço opcional do compose
(`COMPOSE_PROFILES=backup`), no modelo do `mailer`: mesma imagem-repositório, entrypoint
próprio, off por default. Ele executa quatro jobs agendados:

| Job | O que faz | Cadência default |
|---|---|---|
| `dump` | `pg_dump -Fc` do banco inteiro → cifra (age) → SHA-256 → upload para S3 externo | diário, `BACKUP_DUMP_AT=03:30` |
| `drill` | baixa o dump REAL do S3, decifra, restaura num Postgres efêmero dentro do próprio container e roda queries de sanidade | semanal, `BACKUP_DRILL_AT` |
| `mirror` | espelho incremental do bucket RustFS → S3 externo (sem propagar deleções) | `BACKUP_MIRROR_INTERVAL_MIN=360` |
| `user_zip` | ZIP per-user (reuso de `backup.Service.Export`) por usuário ativo → cifra → S3 | diário, `BACKUP_USERZIP_AT` (opt-in) |

Estado e histórico vivem na tabela **`backup_run`** (migração 000040), modelada no
`mail_outbox`. A observação tem três canais independentes: métricas Prometheus servidas
pelo próprio agente (+ dashboard e alert rules Grafana versionados no repo), uma banda
"Backup da instância" no settings hub (owner/admin), e alerta por e-mail via o outbox
transacional existente.

### 1.3 Goals

- Dump lógico completo e **cifrado** do Postgres, diário, com retenção GFS, fora da máquina.
- Cópia dos objetos do RustFS fora da máquina (o dump sem o bucket restaura cards sem imagem
  e notas sem mídia).
- **Prova periódica de restaurabilidade** — o drill restaura o artefato real e compara
  contagens, não presume.
- Monitoria em que **silêncio nunca parece saúde**: o agente que nunca subiu, o job que
  nunca rodou e o dump que ficou velho são todos estados visíveis e alertáveis (lição do
  incidente do `mailer`, `docs/TASKS.md`).
- Superfície de administração: o owner vê na UI que o backup rodou, para onde foi, que
  tamanho tem e que o último drill provou a restauração — e dispara uma execução manual.
- Opt-in por perfil compose; instância sem o perfil continua funcionando exatamente como hoje.

### 1.4 Non-goals (v1)

- **PITR / incremental de bloco** (pgBackRest, wal-g). O banco de uma instância Foldex é
  pequeno; um dump diário completo custa segundos e a perda máxima de 24h é aceitável para
  bookmarks. Se um dia doer, pgBackRest entra como serviço irmão sem tocar neste desenho.
- **Restore automatizado da instância.** O caminho de volta é documentado e manual
  (`age -d | pg_restore` + espelho de volta) — restaurar uma instância é decisão de
  operador, não de agente.
- **Shippar Grafana/Prometheus.** O repo entrega dashboard JSON, scrape config e alert
  rules; o stack de observabilidade é do operador.
- **Multi-destino / replicação cruzada.** Um alvo S3 por instância.
- **Backup do `.env` / chaves.** Segredos não viajam para o bucket (INV-105 na veia
  operacional): o runbook de DR instrui o operador a guardar `.env` + identidade age em um
  cofre próprio. Um backup que carrega a própria chave não é cifrado, é ofuscado.

---

## 2. Arquitetura

```
                         rede docker `foldex`
┌──────────────┐   pg_dump / SQL   ┌────────────┐
│ foldex-db    │◄──────────────────│            │      HTTPS
│ postgres     │                   │ backup-    │────────────────► S3 externo
│ 18.4-alpine  │                   │ agent      │  dump cifrado     (qualquer
└──────────────┘                   │            │  mirror           compatível)
┌──────────────┐   ListObjects/    │ postgres:  │  user_zip
│ rustfs       │   GetObject       │ 18.4-alpine│
│ (origem, ro) │◄──────────────────│ + binário  │──┐
└──────────────┘                   │ Go         │  │ initdb/pg_ctl/pg_restore
                                   └─────┬──────┘  ▼
┌──────────────┐    lê backup_run        │      Postgres efêmero (drill)
│ backend      │─────────────────────────┤      unix socket, /tmp, descartável
│ (API admin + │    INSERT requested     │
│  UI hub)     │◄────────────────────────┘ escreve backup_run, mail_outbox
└──────────────┘                           serve :9099 /metrics /healthz
```

Pontos estruturais:

- **O agente é o único processo com credenciais do S3 externo.** O backend web nunca as
  recebe; o botão "Executar agora" da UI só INSERE uma linha `requested` em `backup_run`,
  e o agente a reivindica. Comprometer o processo exposto à web não dá escrita no bucket
  de backup.
- **A imagem do agente deriva de `postgres:18.4-alpine`** (novo estágio `backup-agent` no
  `backend/Dockerfile`, binário Go copiado do estágio `build`). É o que dá `pg_dump`,
  `pg_restore`, `initdb` e `postgres` version-matched com `foldex-db` sem docker-in-docker
  — `apk add postgresql18-client` sobre a imagem do backend daria o client mas não o
  servidor que o drill exige. Publica como tag do MESMO repositório Docker Hub
  (`justoeu/foldex-backend:<ver>-backup-agent`), respeitando o precedente anti-terceiro-repo
  registrado no Dockerfile para o mailer.
- **Postgres passa a estar pinado em QUATRO lugares** (dois compose, testcontainers, e o
  estágio `backup-agent`) — a linha do Postgres em `docs/STACK.md` é emendada no PR1, e o
  agente compara no boot a major de `SELECT version()` com a de `pg_dump --version`,
  logando warning em divergência (o `pg_dump` já recusa servidor mais novo, então drift
  real falha alto, não em silêncio).
- **Coordenação entre processos é advisory lock do Postgres** — o único mutex
  cross-process que a stack já tem. Nova constante em `internal/backup` (importável pelo
  agente): `InstanceBackupAdvisoryLockKey int64 = 0x464F4C4458424B50` ("FOLDXBKP", mesmo
  esquema mnemônico do `RestoreAdvisoryLockKey` = "FOLDXRST"). O slot HTTP per-user
  (`maxConcurrentArchiveOperations = 1`) fica intocado: ele é por-processo e continua
  governando só os endpoints.

---

## 3. Modelo de dados (migração `000040_backup_run`)

```sql
CREATE TABLE backup_run (
    id              BIGSERIAL PRIMARY KEY,
    job             TEXT        NOT NULL CHECK (job IN ('dump','mirror','user_zip','drill')),
    status          TEXT        NOT NULL DEFAULT 'running'
                      CHECK (status IN ('requested','running','succeeded','failed')),
    claim_token     UUID,                     -- identidade da instância do agente; NULL só em requested
    scheduled_for   TIMESTAMPTZ NOT NULL,     -- o slot que este run satisfaz (catch-up marca o slot perdido)
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ,
    artifact_key    TEXT,                     -- chave S3 (dump/user_zip); NULL para mirror/drill
    artifact_bytes  BIGINT,
    artifact_sha256 TEXT,                     -- sha256 do CIPHERTEXT (o que está no S3)
    objects_scanned BIGINT,                   -- mirror
    objects_copied  BIGINT,
    bytes_copied    BIGINT,
    drill_of_run_id BIGINT REFERENCES backup_run(id),  -- qual dump o drill validou
    last_error      TEXT,                     -- razão NORMALIZADA (precedente mail_outbox: nunca output cru)
    meta            JSONB       NOT NULL DEFAULT '{}'::jsonb  -- versões de ferramenta, durações por fase, contagens
);

CREATE INDEX backup_run_job_started_idx ON backup_run (job, started_at DESC);
-- Exclusão mútua na camada de persistência: dois agentes (erro de deploy) não
-- registram o mesmo job simultaneamente — o segundo INSERT/claim falha e pula o slot.
CREATE UNIQUE INDEX backup_run_one_running_idx ON backup_run (job) WHERE status = 'running';
```

Decisões deliberadas, na contramão do `mail_outbox` de onde o resto veio:

- **Sem `attempts`/`next_attempt_at`.** Um run falho é uma LINHA, não um estado que evolui:
  a política de retry do backup é "o próximo slot agendado", e um retry curto opcional
  (`BACKUP_RETRY_AFTER_MIN`) cria linha nova. O histórico é o produto — a UI e o Grafana
  leem a série, não o último estado.
- **`requested` é o canal do botão "Executar agora".** O backend INSERE
  `(job, status='requested', scheduled_for=now())`; o agente faz poll (~30 s) e reivindica
  por `UPDATE … SET status='running', claim_token=$1, started_at=now() WHERE id=$2 AND
  status='requested'` — CAS no molde do claim do outbox.
- **Janitor obrigatório.** No boot e antes de cada INSERT `running`, o agente expira
  `running AND started_at < now() - ttl` para `failed` com `last_error='stale_claim'`.
  Sem isso, um agente morto no meio de um run trava o índice parcial único para sempre —
  o análogo do claim-TTL do outbox.
- **`last_error` é um token estável** (`pg_dump_failed`, `upload_failed`, `s3_unreachable`,
  `drill_restore_failed`, `drill_counts_mismatch`, `stale_claim`, …), nunca stderr cru:
  stderr de `pg_dump` pode carregar DSN, e a UI/e-mail exibem esse campo.

**`RequiredSchemaVersion` NÃO bumpa nesta migração.** A regra do repo é bumpar quando o
código do BACKEND lê ou escreve algo novo — e no PR1 só o agente toca a tabela. O bump
37→40 acontece no PR5, quando o endpoint admin passa a ler `backup_run`. O agente faz seu
próprio gate no boot (`SELECT version FROM schema_migrations` ≥ 40 → senão sai com
"run backend migrations first"): ele **não roda migrações**; o dono único de migrações
continua sendo o backend.

`.down.sql`: `DROP TABLE backup_run;` — reversível de verdade.

---

## 4. Configuração

Namespace novo `BACKUP_*`, carregado por `loadCfg()` local no binário (precedente
`cmd/rustfs-bootstrap`: binário auxiliar não arrasta `internal/config` inteiro). Postgres
continua vindo de `POSTGRES_*` (INV-101); RustFS de origem, de `RUSTFS_*`.

| Var | Default | Notas |
|---|---|---|
| `BACKUP_S3_ENDPOINT` | — (obrigatória) | host:porta do S3 externo |
| `BACKUP_S3_REGION` | `us-east-1` | novo campo `Region` em `storage.Config` |
| `BACKUP_S3_BUCKET` | — (obrigatória) | |
| `BACKUP_S3_ACCESS_KEY` / `BACKUP_S3_SECRET_KEY` | — (obrigatórias) | em branco no `.env.example`, como todo segredo |
| `BACKUP_S3_USE_SSL` | `true` | |
| `BACKUP_DUMP_AT` | `03:30` | âncora de relógio `HH:MM` (ver §6) |
| `BACKUP_DRILL_AT` | vazio = desligado | semanal (`HH:MM <weekday>`); implementado no PR2 como **opt-in** — ligá-lo por default exigiria a identidade privada em todo host, e essa é uma decisão do operador |
| `BACKUP_USERZIP_AT` | vazio = desligado | opt-in |
| `BACKUP_MIRROR_INTERVAL_MIN` | `360` | intervalo, não âncora — segue a convenção int do repo |
| `BACKUP_RETAIN_DAILY` / `_WEEKLY` / `_MONTHLY` | `7` / `4` / `6` | GFS (§7) |
| `BACKUP_RETAIN_USERZIP` | `7` | últimos N ZIPs por usuário (§5.4); só em `RETENTION_MODE=agent` |
| `BACKUP_RETENTION_MODE` | `agent` | `agent` \| `bucket` (§7) |
| `BACKUP_AGE_RECIPIENTS` | — | chaves públicas age, separadas por vírgula (§8) |
| `BACKUP_AGE_IDENTITY_FILE` | — | só o drill precisa; padrão `keyfile`, sem autogenerate |
| `BACKUP_ALLOW_PLAINTEXT` | `0` | opt-out explícito da cifragem (§8) |
| `BACKUP_ALERT_AFTER` | `2` | falhas consecutivas → e-mail ao owner (§9.3) |
| `BACKUP_METRICS_ADDR` | `:9099` | interno à rede `foldex`; sem porta no host por default |
| `METRICS_TOKEN` | (compartilhada) | mesmo bearer do `/metrics` do backend |
| `AUTH_ENCRYPTION_KEY` | (compartilhada) | exigida só se `BACKUP_ALERT_AFTER > 0` — `mailoutbox.EnqueueTx` cifra o payload com ela |
| `TZ` | `UTC` | as âncoras `HH:MM` são interpretadas neste fuso |

Boot fail-fast: sem endpoint/bucket/credenciais → exit 2 com mensagem nomeando a var; sem
`BACKUP_AGE_RECIPIENTS` e sem `BACKUP_ALLOW_PLAINTEXT=1` → recusa (§8); placeholder de
credencial conhecido → recusa (precedente `knownPlaceholder` do rustfs-bootstrap).

---

## 5. Os quatro jobs

### 5.1 `dump`

1. `pg_try_advisory_lock(InstanceBackupAdvisoryLockKey)` — ocupado ⇒ PULA o slot: o lock ocupado significa que OUTRO agente está executando o trabalho agora, então reagendar duplicaria o dump que já está acontecendo; o registro do outro agente é o registro do slot.
2. `pg_dump -Fc --no-password` **sem `-C`**: não embutir `CREATE DATABASE` desacopla o
   artefato de locale/provider do cluster de origem — o drill (e um DR real) cria o banco
   alvo com `createdb -T template0 -E UTF8` no cluster de destino.
3. Stream: `pg_dump stdout → age encrypt → tee sha256 → spool 0600 em disco` (padrão
   `snapshotSpool`/`boundedWriter` do `internal/backup`), depois
   `storage.PutObjectStream` no client apontado ao S3 externo. Spool em vez de upload
   direto porque `PutObjectStream` exige tamanho — e um upload que falha no meio não
   deixa objeto truncado com cara de backup.
4. Chave: `backups/dump/YYYY/MM/DD/foldex-<ts>.dump.age`. Registra bytes, sha256 (do
   ciphertext — o que um verificador externo consegue conferir com `sha256sum` no bucket),
   contagens por tabela (`meta`, para o drill comparar) e versões de ferramenta.
5. Retenção (§7) roda após sucesso.

O dump carrega TUDO, auth incluída — é essa a diferença de propósito para o ZIP per-user,
e é por isso que a cifragem é default-obrigatória (§8). A chave `AUTH_ENCRYPTION_KEY` que
decifra os seeds TOTP **não** está no banco (env/keyfile), então o dump sozinho não abre
TOTP — mas hashes bcrypt e todo o conteúdo estão lá.

`pg_dump` concorrente com um restore per-user é seguro para o banco: o restore é UMA
transação (`pg_try_advisory_xact_lock` em `restore.go`), e MVCC dá ao dump um snapshot
consistente — ou inteiro antes, ou inteiro depois.

### 5.2 `drill`

Restaura o artefato REAL do S3 num Postgres efêmero DENTRO do container do agente — sem
docker-in-docker, porque a imagem-base já traz o servidor:

1. Escolhe o `dump` `succeeded` mais recente — **como implementado (PR2): sempre o mais
   recente, drillado antes ou não** — rodar de novo é barato e cada run re-valida os
   BYTES do bucket, não a memória que o pipeline tem deles. O `drill_of_run_id` é
   carimbado na própria linha do drill assim que a fonte é escolhida, para que até um
   drill que falha no meio registre QUAL dump estava validando.
2. Download do S3 → decrypt com `BACKUP_AGE_IDENTITY_FILE` → spool. Isso valida os BYTES
   ARMAZENADOS e o round-trip da cifragem num passo só — um drill do arquivo local
   provaria menos.
3. Cluster efêmero como uid 999 (`postgres` da imagem; o servidor recusa root de qualquer
   forma): `initdb -U $POSTGRES_USER --locale=C --encoding=UTF8` em `/tmp/drill/data`
   (layer gravável, sem volume), `pg_ctl start -o "-c listen_addresses='' -c
   unix_socket_directories=/tmp/drill -c shared_buffers=64MB -c max_connections=10 -c
   fsync=off -c synchronous_commit=off -c autovacuum=off"` — `fsync=off` é seguro para
   dados descartáveis e corta o tempo do drill. `initdb` com o MESMO usuário do cluster
   de produção elimina erros de ownership no restore.
4. `createdb -T template0 -E UTF8` + `pg_restore -j1` via unix socket.
5. Sanidade: contagem por tabela vs `meta["tables"]` do run de origem (o job `dump`
   grava as contagens lidas do pool ANTES do `pg_dump`, mesmas tabelas de conteúdo que o
   drill reconta) e `schema_migrations.version` igual ao registrado. Divergência ⇒
   `failed('drill_counts_mismatch')`. **Como implementado (PR2): a amostra de joins
   órfãos (validação de FKs) ficou de fora** — o `pg_restore --exit-on-error` já falha
   em qualquer FK inválida no caminho do restore, e a amostra extra não tinha fonte real
   de divergência que as contagens não peguem; reavaliar se um incidente provar o
   contrário.
6. Teardown incondicional (`pg_ctl stop -m immediate` + `rm -rf`), inclusive em pânico.
   Razões normalizadas do drill: `drill_no_dump` (nada para validar — também um estado
   honesto, nunca sucesso vazio), `drill_download_failed`, `drill_digest_mismatch`,
   `drill_decrypt_failed`, `drill_restore_failed`, `drill_counts_mismatch`.

As extensões do schema (`pg_trgm`, `btree_gin`, `unaccent` — migrações 000001/000009/000017)
são contrib e existem na imagem. Uma migração futura que adicionar extensão não-contrib
quebra o drill — **por design**: o drill é exatamente o detector de "o backup não restaura
no ambiente que temos".

### 5.3 `mirror`

Espelho incremental RustFS → S3 externo, prefixo `backups/rustfs/`.

- `ObjectInfo` (`internal/storage`) ganha `ETag` e `LastModified` — minio-go já os entrega
  em ListObjects, custo zero.
- Diff por **watermark + lista de chaves**: cada run lista origem e destino (1 LIST por
  1000 objetos); copia quando (a) a chave não existe no destino, (b) o tamanho difere, ou
  (c) `src.LastModified >= watermark`, onde watermark = `started_at` do último mirror
  `succeeded` − 1 h de overlap. **ETag NÃO é critério de diff**: etag multipart
  (`hash-N`) depende do part size de cada upload — comparar etags entre uploads distintos
  produz mismatch permanente e re-cópia eterna. ETag serve só como verificação de
  integridade da resposta de cada upload.
- Cópia paralela no padrão `runRestoreObjectTasks` (pool com fail-fast, teto derivado de
  `resourcebudget`).
- **Deleções NÃO se propagam** (default): um backend comprometido (ou um bug de wipe) que
  apaga objetos não pode apagar também a cópia de backup — propagar delete converte
  ransomware em perda do backup. Órfãos no destino são bounded (chaves UUID); a limpeza
  endurecida é bucket versionado + lifecycle sobre versões noncurrent, zero lógica no
  agente. Um `BACKUP_MIRROR_DELETE=1` como opt-in estrito é possibilidade FUTURA — o v1
  não implementa propagação de delete de forma nenhuma, nem atrás de flag.
- **Objetos saem cifrados por objeto** (`<chave>.age`, mesmos `BACKUP_AGE_RECIPIENTS` do dump; opt-out conjunto via `BACKUP_ALLOW_PLAINTEXT`): a mídia é o ÚNICO payload que o dump cifrado não carrega, e o espelho é o único canal que a leva para fora da máquina — em claro, comprometer o bucket externo renderia a mídia de todos os tenants. Consequências: o diff por TAMANHO só vale no modo plaintext (ciphertext ≠ origem por construção — seria o modo de falha do ETag com outra cara), e o DR do bucket vira `age -d` por objeto em vez de prefix-copy puro.
- **Origem é `storage.NewReadOnly`** (nunca cria bucket): um typo em `RUSTFS_BUCKET` falha o boot em vez de criar um bucket vazio e espelhar "com sucesso" para sempre. A interface da origem no agente é só-leitura (`SourceBucket`: Walk+Open) — o tipo impede Put/Delete na origem.
- Notas operacionais: objetos com `LastModified` dentro da janela de overlap re-copiam a cada passada (por design — em modo cifrado isso re-cifra essa janela; custo marginal no cadence de 6h). Ligar a cifragem depois de passes plaintext re-copia tudo como `.age` e DEIXA as cópias plaintext antigas no destino (deleções não propagam) — limpe `backups/rustfs/` manualmente ou gire o bucket ao ativar.
- **Chaves com componente `..` são puladas com aviso** (contadas em `meta.suspicious_keys_skipped`), e a gramática do sufixo em `linkObjectID` (`internal/backup`) recusa qualquer coisa além de `^\.[A-Za-z0-9]{1,16}$` — sem isso, um ZIP artesanal restaurado plantaria uma chave com traversal que o espelho copiaria e, num destino que normalize caminhos, sobrescreveria os dumps.
- Antes de rodar, probe `pg_try_advisory_lock(RestoreAdvisoryLockKey)` (adquire e solta):
  um restore per-user em andamento deixa o RustFS em estado intermediário — o banco é
  transacional, o bucket não (INV-104). Ocupado ⇒ o job espera NO PRÓPRIO run (probe a
  cada 1 min, até 10 min, ctx-aware — o scheduler fica genérico, zero estado de
  reagendamento); ainda ocupado no deadline ⇒ `failed('restore_in_flight')`, razão
  operacional que NÃO conta para o alerta de falhas consecutivas — o próximo slot refaz.

### 5.4 `user_zip`

Agendamento do backup de produto: para cada usuário ativo, `backup.Service.Export(ctx,
uid, spool, …)` — o agente tem pool e storage, constrói o `Service` diretamente — depois
cifra e sobe para `backups/users/<uid>/<ts>.zip.age`. Retenção própria simples
(`BACKUP_RETAIN_USERZIP=7` por usuário). Mesma deferência ao `RestoreAdvisoryLockKey`
do mirror (Export lê o bucket). Serializado com os demais jobs pelo
`InstanceBackupAdvisoryLockKey` — e, por ser outro processo, nunca compete pelo slot HTTP.

Nota: o `.zip.age` cifra com os recipients do OPERADOR — o fluxo de entrega ao usuário é o operador decifrar (`age -d`) e entregar o `.zip`, que aí sim restaura via `/api/backup/restore` sem mais intervenção; o usuário não decifra sozinho, por desenho (uma chave por usuário seria um segundo sistema de chaves inteiro).

Opt-in (`BACKUP_USERZIP_AT` vazio desliga): o dump da instância já contém tudo; o valor do
user_zip é oferecer o formato restaurável PELO USUÁRIO (via `/api/backup/restore`) sem
intervenção do operador. Opt-in exige as `RUSTFS_*` do bucket de origem (boot fail-fast
sem elas); um agente sem o job configurado nem constrói o client RustFS.

Semântica de falha (PR4): UM `backup_run` para o job inteiro. Falha de um usuário não
aborta os demais; a deferência ao restore (probe por usuário) pula só o ciclo daquele
usuário. O run só falha (`user_zip_failed`) quando a listagem de usuários falha ou quando
TODOS os exports tentados falham. `meta` registra `users`, `bytes_total` e, quando
existirem, `failed_users` / `deferred_users` / `prune_error` — é o que a superfície admin
(PR5) renderiza. A chave é `backups/users/<uid>/<ts>.zip[.age]`, timestamp UTC de largura
fixa: ordem lexicográfica É ordem cronológica, e a retenção poda sem parse de data,
nunca cruzando o prefixo de outro usuário nem tocando chave que o job não escreveu.

---

## 6. Agendamento

Sem lib de cron. Por job com âncora: calcular
`next := time.Date(hoje, HH, MM, TZ)`; se `next <= now`, `next += 24h` (drill: próximo dia
da semana configurado); `time.Timer` até lá; recalcular após cada disparo. Recalcular
absorve DST — no dia da transição uma âncora pode disparar duas vezes ou pular; documentado
e aceito (o catch-up cobre o pulo).

**Formato `HH:MM` é um desvio deliberado da convenção `*_SEC`/`*_MIN`**: a convenção
codifica DURAÇÕES; uma âncora é um instante de parede, e `BACKUP_DUMP_AT_MIN=210` seria
hostil ao operador. O desvio fica restrito às âncoras; intervalos continuam ints.

**Catch-up**: no boot, para cada job habilitado, ler o último `succeeded` em `backup_run`;
se inexistente ou `now − last > intervalo + grace(25%)`, rodar imediatamente com jitter de
1–5 min, gateado em ping do Postgres e do RustFS (não disparar no meio do
`docker compose up`). O estado em `backup_run` torna o catch-up correto entre restarts sem
arquivo local — o container é descartável.

Ciclo de vida do processo: modelo `cmd/mailer` — `signal.NotifyContext(SIGINT, SIGTERM)`,
`run(logger) int`, workers com `Start(ctx)`/`Stop()` idempotente via `sync.Once`. Um job em
andamento recebe o cancelamento do ctx e registra `failed('shutdown')`; o catch-up do
próximo boot o refaz.

---

## 7. Retenção GFS

Layout de chave `backups/dump/YYYY/MM/DD/…` torna a classificação trivial por LIST.

- **Default `BACKUP_RETENTION_MODE=agent`**: após cada dump bem-sucedido (e, para dumps,
  preferindo podar só o que um drill já cobriu), o agente mantém os últimos
  `BACKUP_RETAIN_DAILY=7` diários, `_WEEKLY=4` (domingo) e `_MONTHLY=6` (dia 1) e deleta o
  resto. Funciona em qualquer alvo S3-compatível sem console de bucket.
- **Modo endurecido `BACKUP_RETENTION_MODE=bucket`**: credencial do agente SEM
  `DeleteObject` (ou bucket com Object Lock/versionamento) e expiração via lifecycle do
  bucket. O prune do agente vira no-op **declarado** — modo explícito por env, nunca
  inferido de `AccessDenied`, para não mascarar erro de permissão real. (`storage.New` já
  tolera `AccessDenied` no `MakeBucket`, então credencial restrita funciona com o wrapper
  existente.)

Trade-off documentado no `.env.example`: modo `agent` = um host comprometido pode apagar o
histórico; modo `bucket` = ransomware-resistente ao custo de configurar lifecycle fora do
Foldex.

---

## 8. Criptografia

**Default-obrigatória com opt-out explícito.** O dump contém hashes bcrypt e todo o
conteúdo de todos os usuários; o destino é um bucket fora da máquina. O agente **recusa
subir plaintext** a menos que `BACKUP_ALLOW_PLAINTEXT=1` (para quem cifra no bucket com
SSE-KMS próprio e sabe o que está fazendo).

Motor: **`filippo.io/age`** (X25519), não AES-GCM caseiro:

- age é streaming nativo com autenticação por chunk — GCM single-shot num artefato de GB
  tem o problema clássico de decrypt-before-verify e não é streamable com segurança.
- O argumento decisivo é DR: o operador decifra **sem o Foldex** (`age -d -i chave.txt`),
  em qualquer máquina. Um formato proprietário transformaria "perdi o host" em "perdi o
  backup".
- `BACKUP_AGE_RECIPIENTS` (chaves públicas) cifra — **o caminho de upload não carrega
  segredo nenhum**. `BACKUP_AGE_IDENTITY_FILE` (privada; padrão `internal/pkg/keyfile`,
  modo 0600) é exigida apenas pelo drill.
- **Sem autogenerate** (na contramão do `AUTH_ENCRYPTION_KEY_AUTOGEN`): uma chave gerada
  que só existe ao lado dos dados protege o bucket mas convida ao backup indecriptável
  quando o host morre. Falhar no boot sem recipients configurados é o comportamento
  correto; o runbook manda guardar a identidade num cofre externo.

O `artifact_sha256` registrado (e conferível na UI) é do **ciphertext** — verificável
contra o bucket com `sha256sum` sem decifrar nada.

---

## 9. Monitoria

Três canais independentes; nenhum estado de falha é representável como silêncio.

### 9.1 Métricas Prometheus (canal primário)

O agente serve HTTP próprio em `BACKUP_METRICS_ADDR` (`:9099`), interno à rede `foldex`
(sem porta no host por default), com `/healthz` (ping db + S3 externo) e `/metrics` atrás
do MESMO `METRICS_TOKEN` do backend — nenhum mecanismo novo de auth de scrape. Registry
próprio no padrão `internal/metrics`. Séries:

```
foldex_backup_last_success_timestamp_seconds{job}
foldex_backup_consecutive_failures{job}
foldex_backup_last_run_duration_seconds{job}
foldex_backup_artifact_bytes{job}
foldex_backup_runs_total{job,status}
```

Prometheus-pull em vez de OTLP-push como canal primário porque **`absent()` de scrape
detecta o agente morto** — push que para de chegar é indistinguível de "nada a reportar".
OTLP segue disponível para traces (o agente reusa `internal/tracing` se
`OTEL_EXPORTER_OTLP_ENDPOINT` estiver setada).

### 9.2 Grafana (artefatos versionados em `deploy/observability/`)

O repo entrega, versionados e testados contra nomes de série reais:

1. **`grafana-backup-dashboard.json`** — dashboard provisionável: idade do último sucesso
   por job, duração e tamanho por execução, falhas consecutivas, resultado dos drills,
   timeline de `backup_run` (via métricas; a UI do hub é quem lê a tabela).
2. **`prometheus-scrape.example.yml`** — scrape do `:9099` com bearer `METRICS_TOKEN`.
3. **`prometheus-alerts.yml`** — regras prontas:
   - `time() - foldex_backup_last_success_timestamp_seconds{job="dump"} > 93600` (26 h)
   - idem drill > 8 d; mirror > 2× intervalo
   - `foldex_backup_consecutive_failures >= 2`
   - **`absent(foldex_backup_last_success_timestamp_seconds{job="dump"})`** — o alerta que
     pega o agente que NUNCA subiu (profile esquecido, crash-loop). É a regra que teria
     pego o incidente do mailer.

O stack Grafana/Prometheus em si não é shippado — instruções de import no README.

### 9.3 Alerta por e-mail (via outbox existente)

Após `BACKUP_ALERT_AFTER` falhas consecutivas do mesmo job, o agente INSERE em
`mail_outbox` via `mailoutbox.EnqueueTx` — template novo `backup_failed` (en/pt/es,
INV-035), destinatário = e-mail do owner (lido do banco), corpo com job, `last_error`
normalizado e link para o hub. Quem ENVIA é o relay no processo do backend (o container
`mailer` deliberadamente não tem `DB_URL`; ele só consome AMQP) — o agente nunca fala
SMTP/AMQP. Ressalva de config: `EnqueueTx` cifra o payload com `AUTH_ENCRYPTION_KEY`,
então o agente recebe essa env no compose — não é expansão de privilégio (quem dumpa o
banco inteiro já lê tudo que ela protege no banco). Backend fora do ar ⇒ o alerta não sai;
é a limitação aceita, porque backend fora do ar é um alarme mais alto e §9.1 é o canal
independente.

---

## 10. Superfície de administração

### 10.1 API (backend)

- `GET /api/admin/backup/runs?job=&page=` — histórico paginado de `backup_run` + resumo
  por job (último sucesso, falhas consecutivas, staleness calculada contra a cadência
  configurada — o backend a conhece por env espelhada ou pela mediana dos slots).
- `POST /api/admin/backup/run {job}` — INSERE `requested`; 409 se já há `requested`
  pendente ou `running` do mesmo job; audit_log (`backup.run_requested`).
- Permissão nova **`PermInstanceBackupRead = "instance.backup"`** — grupo `instance.*`
  (ao lado de `instance.transfer`), NÃO `backup.*`, que em `permissions.go` significa o
  backup per-user. Checklist ADR-42 completo: const + `AllPermissions` + `rolePermissions`
  (owner+admin) + seed na migração (se editável) + i18n nos 3 locales. Não-admin recebe
  404 (INV-043).
- É neste PR (PR5) que `RequiredSchemaVersion` bumpa 37→40.

### 10.2 UI (settings hub)

Banda **"Backup da instância"** no settings hub (INV-148 — superfície única; não é página
avulsa), visível atrás de `instance.backup`:

- **Estado por job**: último sucesso relativo ("há 6 h"), destino (`bucket/chave` do
  artefato enviado), tamanho, SHA-256 (truncado, copiável), duração.
- **Staleness com contrato**: "último dump há X — esperado a cada Y", em cor de alerta
  quando X > Y + grace. É a resposta de um relance para "está funcionando?".
- **Último drill em destaque**: quando rodou, qual dump validou (`drill_of_run_id`) e as
  contagens que provaram a restauração. Um dump verde com drill velho é meio-backup, e a
  UI diz isso.
- **Histórico paginado** de `backup_run` com `last_error` normalizado por linha.
- **"Executar agora"** por job — botão com rótulo (INV-151) e confirmação (INV-122); só
  enfileira `requested`, o feedback é a linha nova no histórico.
- **Empty-state honesto**: sem o profile `backup` ativo (nenhum run jamais registrado, ou
  `requested` envelhecendo sem claim), a banda diz "o serviço de backup não está ativo —
  habilite `COMPOSE_PROFILES=backup`" em tom de aviso. Um serviço opcional que nunca subiu
  não pode parecer saudável (incidente do mailer).
- i18n en/pt/es; tabela responsiva (INV-165); classes novas cobertas pelo guard de CSS
  órfão (INV-159).

---

## 11. Decisões de design

### 11.1 Por que um quarto binário, e não um worker no backend

O backend é o processo exposto à web. Backup de instância exige credencial de escrita num
bucket externo e a capacidade de ler o banco INTEIRO — colocar isso no processo web
significa que qualquer RCE no backend vira exfiltração do backup e escrita no bucket. No
agente separado, o backend nunca vê `BACKUP_S3_*`, e o botão da UI só enfileira. De
brinde: backup sobrevive a deploy/crash do backend, e a imagem certa (postgres-base) não
infla a imagem web. Trade-off: mais um serviço para operar — mitigado pelo profile off por
default e pelo empty-state da UI que aponta o caminho.

### 11.2 Por que pg_dump lógico, e não pgBackRest/wal-g

O banco de uma instância Foldex mede megabytes; um `pg_dump -Fc` custa segundos e o
artefato é restaurável com ferramentas universais. pgBackRest/wal-g dão PITR e incremental
de bloco ao custo de acoplar ao PGDATA e ao `archive_command` do container do Postgres —
complexidade permanente por um requisito que bookmarks não têm. "Incremental" aqui é
retenção GFS: o custo de armazenar 17 dumps completos pequenos é menor que o custo de
operar uma cadeia incremental. Trade-off: perda máxima de até 24 h — aceito no non-goal.

### 11.3 Por que o drill roda no próprio container

A alternativa (subir um container Postgres efêmero) exige docker socket ou DinD — um
privilégio enorme para um job de verificação. A imagem-base `postgres:18.4-alpine` já traz
`initdb`/`postgres` version-matched; um cluster em `/tmp` com `fsync=off` sobe em ~2 s e
morre com o processo. Trade-off: o drill valida restauração na MESMA major do servidor de
origem (que é o cenário de DR real), não em majors futuras.

### 11.4 Por que a exclusão mútua é um índice parcial + advisory lock, e não só um

O índice parcial único (`WHERE status='running'`) é a verdade PERSISTIDA — dois agentes
por erro de deploy não registram o mesmo job, mesmo que nunca tenham se visto. O advisory
lock é o mutex de EXECUÇÃO — barato de segurar pela duração do job e liberado por queda de
conexão (o que o índice não faz sozinho: daí o janitor de `stale_claim`). Cada um cobre a
falha do outro. Trade-off: dois mecanismos para raciocinar — documentados juntos aqui.

### 11.5 Por que age e não o `secrets.Cipher` da casa

`internal/pkg/secrets` (AES-256-GCM) é o padrão do repo para segredos PEQUENOS no banco.
Backup é um artefato de possivelmente GB cuja história de recuperação precisa funcionar
NUM MUNDO SEM O FOLDEX. age dá streaming autenticado por chunk e uma CLI universal de
decrypt. Trade-off: uma dependência nova (`filippo.io/age`, autor do time de cripto do Go,
sem transitividade relevante) — entra na tabela do STACK.md com esse porquê.

### 11.6 Por que o mirror não usa ETag como critério

ETag de multipart é `md5(concat(md5s))-N` — função do part size DO UPLOAD, não do
conteúdo. Origem (uploads do backend) e destino (uploads do agente) particionam diferente
⇒ etags nunca batem ⇒ diff por etag re-copia o bucket inteiro a cada run, para sempre, em
silêncio. Watermark por `LastModified` + presença/tamanho é barato (2 LISTs) e o overlap
de 1 h cobre relógio e runs longos. Trade-off: um overwrite com mesmo tamanho fora da
janela de overlap escapa — cenário sem fonte real no Foldex (objetos são imutáveis por
chave UUID; re-upload gera chave nova, INV-078).

### 11.7 Por que "Executar agora" é um INSERT, não um RPC

Backend → agente por HTTP exigiria endpoint autenticado novo no agente, service discovery
e mais uma superfície. A tabela já é o ponto de encontro; `requested` + claim CAS reusa o
padrão do outbox, ganha auditoria de graça (a linha É o registro) e degrada honestamente:
sem agente vivo, o `requested` envelhece visível na UI em vez de um RPC dar timeout.
Trade-off: latência de até ~30 s do poll — irrelevante para backup.

---

## 12. Trade-offs e limitações

| Limitação | Mitigação |
|---|---|
| Perda máxima de até 24 h (sem PITR) | Cadência configurável; non-goal declarado; pgBackRest pode entrar como serviço irmão depois |
| Dump e mirror não são um snapshot atômico conjunto (DB ≠ bucket, mesmo problema do INV-104) | Órfão de referência vira card sem imagem, re-armado pelo próprio pipeline de preview (INV-081); dump e mirror rodam em sequência sob o mesmo lock para encurtar a janela |
| Alerta por e-mail depende do backend vivo | Métricas + `absent()` são o canal independente (§9.1) |
| DST pode duplicar/pular uma âncora no dia da transição | Catch-up cobre o pulo; duplicação é inócua (segundo run é rápido e a retenção poda) |
| Modo retenção `agent`: host comprometido apaga histórico | `BACKUP_RETENTION_MODE=bucket` + credencial sem delete + lifecycle (§7) |
| Drill valida a major corrente, não upgrades futuros | O guard de versão no boot detecta drift; upgrade de major do Postgres já é um evento operacional com runbook próprio |
| `requested` sem agente vivo fica pendente | UI mostra o envelhecimento + empty-state aponta o profile |

---

## 13. Segurança

- **Credenciais `BACKUP_S3_*` só existem no processo do agente** — nunca no backend web,
  nunca na UI, nunca em `backup_run` (a tabela guarda chave de objeto, não credencial).
- Cifragem client-side default-obrigatória (§8); a chave privada não fica no bucket nem na
  imagem (INV-117 por analogia); sem autogenerate.
- `last_error` normalizado: stderr de ferramenta nunca chega a banco/UI/e-mail (DSN leak).
- O DSN nunca é logado (logger com `logsafe.NewRedactHandler`, INV-006); comandos
  `pg_dump`/`pg_restore` recebem senha via env `PGPASSWORD`, não argv (argv é visível em
  `/proc`).
- Endpoint admin atrás de `instance.backup`; não-admin recebe 404 (INV-043); "Executar
  agora" audita em `audit_log`.
- `/metrics` do agente atrás de `METRICS_TOKEN` (vazio ⇒ 503, como no backend); sem porta
  publicada no host por default.
- Volume RustFS montado no agente como leitura (`:ro`) — o mirror não precisa e não recebe
  escrita na origem. (Acesso é via S3 API de qualquer forma; a credencial usada é a de
  app, não a root.)
- Recomendação de IAM no `.env.example`: credencial dedicada, escopo mínimo
  (`PutObject`+`ListBucket`+`GetObject`; `DeleteObject` só no modo `agent`).

---

## 14. Testing strategy

### Backend / agente (Go)

- Unit: parser de âncora `HH:MM` (+ TZ, + dia da semana), cálculo de next-run em bordas de
  DST (fuso fixo com transição conhecida), classificação GFS de chaves, normalização de
  `last_error`, watermark do mirror.
- Integração (`testdb.Shared`, com `TestMain` → `StopShared` — CLAUDE.md §2): claim CAS de
  `requested`; índice parcial único rejeitando segundo `running`; janitor expirando
  `stale_claim`; catch-up decidindo por `backup_run`; gate de `schema_migrations`.
- Integração com containers: um teste que roda `pg_dump` real contra o Postgres de teste,
  cifra, decifra e restaura no cluster efêmero — o drill inteiro, em miniatura, no CI
  (build-tag `integration`; exige binários postgres no ambiente de teste → roda dentro da
  própria imagem `backup-agent` no CI, não no runner cru).
- Guards no molde `internal/security`:
  - `TestBackupAgentNeverReceivesBackendOnlySecrets` — o compose do agente não carrega
    `VAPID_*`/`AUTH_*` além do que o §4 declara (AST/parse do compose).
  - `TestDumpCommandNeverCarriesThePasswordInArgv`.
  - `TestLastErrorIsAlwaysANormalizedToken` — nenhum write de `last_error` com string fora
    do conjunto declarado.
- Mutantes que os testes devem matar (padrão do repo: provar que X FAZ EFEITO, não que
  existe): remover o `-T template0` do drill; remover o overlap de 1 h do watermark;
  trocar o sha256 para o plaintext; remover o probe do `RestoreAdvisoryLockKey`.

### Frontend

- Vitest + testing-library: banda renderiza os 4 jobs; staleness muda de classe quando
  vence o contrato; "Executar agora" confirma antes de POSTar (INV-122) e mostra a linha
  `requested`; empty-state sem runs; erro normalizado renderizado; mock em
  `src/test/server.ts` em sincronia com o handler.
- Guards CSS existentes cobrem as classes novas (INV-154/159).

### Observabilidade

- Teste de script (`scripts/test-*.sh`, precedente do repo) validando que TODA série usada
  em `deploy/observability/prometheus-alerts.yml` e no dashboard JSON existe no registry do
  agente — um rename de métrica não pode deixar um alerta apontando para série morta (a
  regra `absent()` ficaria… ausente).

### Coverage

Gate de 85% do CLAUDE.md §2 vale para o pacote do agente; `cmd/backup-agent` entra na
lista de exclusão de MEDIÇÃO como os demais `cmd/*`, com a lógica toda em pacotes
`internal/backupagent/*` testáveis.

---

## 15. Faseamento

| PR | Escopo | Flag / fronteira |
|---|---|---|
| PR1 | Migração 000040 (sem bump de `RequiredSchemaVersion`) + `cmd/backup-agent` (config, scheduler, locks, claim+janitor) + job `dump` completo (pg_dump→age→sha256→S3) + retenção GFS + estágio `backup-agent` no Dockerfile + serviço compose sob profile `backup` + métricas §9.1 + `deploy/observability/` (scrape + alert rules) + **remoção de `scripts/backup.sh`** + `.env.example`/STACK (linha do Postgres: 4 lugares; linha do age)/README/INVARIANTS novos | `COMPOSE_PROFILES=backup` |
| PR2 | Job `drill` (cluster efêmero, sanidade, `drill_of_run_id`) + teste de integração do pipeline completo | — |
| PR3 | Job `mirror` (extensão `ObjectInfo` com ETag/LastModified, watermark, sem delete) | `BACKUP_MIRROR_INTERVAL_MIN` |
| PR4 | Job `user_zip` (reuso de `Service.Export`, deferência ao `RestoreAdvisoryLockKey`) | `BACKUP_USERZIP_AT` (vazio = off) |
| PR5 | Superfície de status: endpoint admin + permissão `instance.backup` + **bump `RequiredSchemaVersion` 37→40** + alerta e-mail via outbox (template 3 locales) + banda completa no settings hub + dashboard Grafana completo | permissão `instance.backup` |

Ordem justificada: cada PR é shippável sozinho; o drill vem em segundo porque a
verificação é o núcleo da proposta — expandir escopo (mirror, user_zip) antes de provar
que o dump restaura seria inverter a prioridade; a superfície de status vem por último
porque só é honesta quando todos os tipos de job existem.

**Paralelização por worktree**: PR1 é serial (fundação). Com ele no main, PR2/PR3/PR4 são
três worktrees paralelas — jobs mutuamente independentes, nenhum com migração própria. Para
os merges serem triviais, o PR1 deixa o registro de jobs table-driven (adicionar um job =
uma entrada na tabela, não editar o scheduler). PR5 fecha serial (a UI exige todos os jobs;
é ele que bumpa `RequiredSchemaVersion`). Gates de integração das worktrees rodam
escalonados, não simultâneos — cada um sobe seus próprios containers Postgres
(CLAUDE.md §2, `TEST_PARALLEL`).

Cada PR segue o Definition of Done integral do CLAUDE.md §6 (gates, sweep de 5 agentes,
README nos dois idiomas, `graphify update .`, bump semver).

## 16. Open questions

1. **Cadência do drill**: semanal é o default proposto; instâncias paranoicas podem querer
   diário (o custo é ~segundos). Decidir se `BACKUP_DRILL_AT` aceita `daily`.
2. **Staleness na UI**: o backend conhece a cadência por env espelhada (`BACKUP_DUMP_AT`
   visível a ele) ou infere da mediana dos `scheduled_for`? Env espelhada é mais simples e
   está proposto; confirmar na implementação do PR5.
3. **Retenção de `backup_run`**: a tabela cresce ~4 linhas/dia; propor purge de `failed`
   > 1 ano no janitor do PR5, ou deixar crescer (é minúscula). Default atual: deixar.
