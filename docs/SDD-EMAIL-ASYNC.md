# SDD — E-mail assíncrono (outbox + RabbitMQ) e segundo fator por e-mail

> Software Design Document. Status: **Todas as fases entregues · v1.0 · 2026-08-18**
>
> Implementado: fase 1 (outbox transacional, relay `inproc`, templates i18n),
> fase A (`locale` no perfil + force-reset assíncrono), fase B (transporte AMQP,
> `cmd/mailer`, escada de retry, dead-letter) e fase C (e-mail como segundo fator
> permanente). Os desvios entre o que este documento propôs e o que subiu estão
> em §13.1 e §13.2, cada um com o motivo.
>
> Cobre ADR-36 (entrega de e-mail durável, com transporte plugável) e ADR-37
> (e-mail como segundo fator permanente, e não apenas como escape de um desafio
> TOTP). Migrações `000034_mail_outbox`, `000035_user_locale` e `000036_email_second_factor`.
>
> Este documento **altera quatro invariantes** já registrados em `CLAUDE.md` §4.
> Cada alteração é nomeada explicitamente em §11, com o motivo e o que passa a
> valer no lugar. Nenhuma delas é acidental.

---

## 1. Visão geral

### 1.1 Problema

A tela "Esqueceu a senha?" está ativa e o endpoint responde `202`. Numa instância
sem SMTP — `MAIL_DRIVER=log`, que é o **default** — o link de redefinição vai para
o stdout do container e ninguém recebe nada. O `202` é correto e deliberado (o
`forgot` responde igual para endereço desconhecido, conta desativada e rate-limit,
nos três canais: status, corpo e tempo), mas o resultado operacional é uma tela que
promete um e-mail que não sai.

Isso é o sintoma. Abaixo dele há três problemas estruturais.

**(a) A entrega é in-process, efêmera e sem retry.**

`mailer.Dispatcher` (`internal/mailer/dispatcher.go`) é uma fila de 32 slots em
memória com 2 workers e timeout de 15 s por envio. Um `Send` que falha é logado e
**descartado** — não há backoff, não há dead-letter, não há persistência:

```go
// dispatcher.go:98-100
if err := d.mailer.Send(sendCtx, job.msg); err != nil {
    d.logger.Error("send mail", "what", job.what, "err", err)
}
```

`Stop()` **cancela** o que está em voo em vez de drenar. Um restart do backend, um
deploy ou um blip de rede no provedor de SMTP perde convites, links de reset e
códigos de login em silêncio. O usuário vê `202` e espera para sempre.

**(b) Não existem templates.**

As nove mensagens são concatenação de string em `internal/mailer/templates.go`, com
`html.EscapeString` aplicado manualmente por chamada. Não há layout compartilhado,
não há i18n (a UI é en/pt/es; o e-mail é só inglês), não há como um designer mexer
no HTML sem editar Go, e não há preview.

**(c) OTP por e-mail não é um fator que se cadastra.**

Ele só existe como escape **dentro de um desafio que já é TOTP**:

```go
// twofa_handler.go:80-82
func (h *Handler) emailFactorAvailable(purpose ChallengePurpose, mailboxAlreadyProven bool) bool {
	return purpose == PurposeTOTP && !mailboxAlreadyProven && h.mailer.Driver() == "smtp"
}
```

`purpose == PurposeTOTP` é o nó: o desafio só nasce `totp` quando a conta **já tem**
um autenticador confirmado (`secondFactorPurpose`, `twofa_handler.go:50-65`). Uma
conta sem TOTP nunca recebe desafio, então `/2fa/email` é inalcançável para ela. Não
existe `email_2fa_enabled`, não existe `POST /2fa/email/enroll`, e
`AUTH_REQUIRE_2FA_FOR_ADMINS` (default `true`) só aceita autenticador — em três
pontos de aplicação independentes.

### 1.2 Decisão

Três movimentos, nesta ordem de dependência:

1. **Outbox transacional.** A mensagem é gravada em `mail_outbox` na **mesma
   transação** que a credencial que ela carrega. O payload é cifrado com
   AES-256-GCM.
2. **Transporte plugável.** Um relay drena o outbox e entrega ao sink configurado:
   `inproc` (o dispatcher atual, default) ou `amqp` (RabbitMQ, opt-in). Com `amqp`,
   um binário separado `cmd/mailer` consome, renderiza e envia.
3. **E-mail vira fator de verdade.** Nova tabela `email_factor` com o mesmo formato
   de `totp_secret`, cadastro com confirmação, e `has_second_factor` substituindo
   `totp_enabled` como a noção de "esta conta tem segundo fator" — **preservando** o
   guard que impede a caixa postal de satisfazer os dois fatores.

### 1.3 Goals

- Nenhuma mensagem perdida por restart, deploy, fila cheia ou falha transitória de
  SMTP.
- Retry com backoff e dead-letter observável.
- Nenhuma credencial em claro no banco nem no broker.
- Templates HTML+texto versionados, com layout compartilhado e i18n en/pt/es.
- O foldex continua subindo e enviando e-mail **sem broker nenhum**.
- Usuário escolhe app OU e-mail como segundo fator, no cadastro.

### 1.4 Non-goals (v1)

- Webhooks de bounce/complaint e supressão de endereços. Um bounce hoje é invisível
  e continua sendo; a DLQ cobre falha de *entrega ao MTA*, não rejeição posterior.
- Editor de template na UI. Templates são arquivos versionados, revisados em PR.
- Broker além do RabbitMQ (Kafka, SQS, NATS). A interface admite, o escopo não.
- Digest/agrupamento de notificações. Toda mensagem continua 1:1 com o evento.
- Push e change-check continuam com suas próprias filas em memória — este documento
  é sobre e-mail.

---

## 2. Arquitetura

### 2.1 O caminho de uma mensagem

```
  handler HTTP
      │  BEGIN
      │  ├─ INSERT password_reset (token_hash)      ─┐
      │  └─ outbox.Enqueue(ctx, tx, msg)             │ mesma transação
      │  COMMIT                                     ─┘
      ▼
  mail_outbox (payload cifrado, status='pending')
      │
      │  relay: claim FOR UPDATE SKIP LOCKED
      ▼
  ┌─────────────────┬──────────────────────────────┐
  │ MAIL_TRANSPORT  │                              │
  │   = inproc      │            = amqp            │
  ▼                 ▼                              │
mailer.Dispatcher   exchange foldex.mail ──────────┤
  │  (mesmo proc)     │  publisher confirms         │
  │                   ▼                             │
  │              queue foldex.mail.send             │
  │                   │                             │
  │                   ▼                             │
  │              cmd/mailer  (decifra, renderiza)   │
  ▼                   ▼                             │
mailer.Mailer.Send  ──┴─────────────────────────────┘
  (drivers smtp / log — inalterados)
```

### 2.2 Por que outbox, e não publish-após-commit

O protocolo `Reserve()/Publish()/Release()` de hoje existe por um motivo preciso,
já registrado em `CLAUDE.md` §4: **reservar a vaga na fila ANTES de persistir a
credencial**. Três handlers dependem disso — `password_handler.go:54` (reset),
`password_handler.go:291` (verificação) e `twofa_handler.go:359` (OTP de login). Se
a fila está cheia, a credencial **não é criada**, em vez de nascer um token que
nunca será enviado.

Publicar no broker *depois* do `COMMIT` reabre exatamente esse buraco: um crash
entre o commit e o publish deixa a credencial gravada e a mensagem perdida. Pior que
o estado atual, porque o cooldown de 60 s já foi cobrado — o usuário não consegue
nem pedir outro.

O outbox resolve por construção. O `INSERT` participa da transação que já está
aberta: não pode falhar por capacidade, não pode ser perdido, e ou os dois existem
ou nenhum existe. **É mais forte que o invariante atual**, não apenas compatível:
hoje fila-cheia derruba a operação inteira; com outbox a operação sempre acontece e
a entrega passa a ser garantida-eventual.

Efeito colateral bom: `Reserve/Publish/Release` **sai dos handlers**. Eles passam a
chamar `outbox.Enqueue(ctx, tx, msg)` dentro da transação que já abrem, e some toda
a coreografia de `defer admission.Release()` + `admission = nil` que hoje existe em
três lugares (e cuja única razão de ser é a ausência de um outbox).

### 2.3 Por que o payload é cifrado

Hoje o token cru de reset existe **apenas dentro do corpo do e-mail**. O banco
guarda `password_reset.token_hash` (sha256) e nada mais. Isso é deliberado e é a
mesma razão pela qual sessões são sha256 e não texto: um `pg_dump` não pode ser um
kit de sequestro.

Gravar o link renderizado — ou os params que o produzem — em texto numa tabela
destruiria essa propriedade. O mesmo vale para o broker: o RabbitMQ persiste
mensagem durável em disco, e um vhost compartilhado com outros projetos amplia quem
consegue lê-la.

`mail_outbox.payload_{ciphertext,nonce}` guardam AES-256-GCM produzido por
`internal/pkg/secrets.Cipher` — a mesma primitiva que já protege o seed TOTP —
chaveada por `AUTH_ENCRYPTION_KEY`, que já é obrigatória e carregada com
`AllowEphemeral: false`. **O mesmo blob cifrado é o corpo da mensagem AMQP**, então
o broker nunca vê credencial em claro; quem decifra é o worker.

GCM e não CTR pela mesma razão do seed TOTP: a tag de autenticação. Sem ela,
escrita no banco (ou no broker) vira ataque de substituição — trocar o link de
destino de um e-mail de recuperação de conta, e a vítima veria apenas um e-mail
legítimo apontando para o lugar errado.

Consequência operacional: `cmd/mailer` precisa de `AUTH_ENCRYPTION_KEY`. Ele **não**
precisa (nem recebe) credencial de Postgres.

### 2.4 Por que o Rabbit é transporte, e não a fila de origem

O outbox faz o que o broker não consegue: atomicidade com a transação do banco. O
broker faz o que o outbox não faz bem: entrega com retry/backoff, dead-letter,
consumidores escaláveis e independentes do processo que serve HTTP.

O relay é o ponto plugável, e é o único lugar que conhece os dois mundos:

| `MAIL_TRANSPORT` | Relay entrega para | Worker | Quando usar |
|---|---|---|---|
| `inproc` (default) | `mailer.Dispatcher` no mesmo processo | nenhum | self-hosted de binário único; sem broker |
| `amqp` | exchange `foldex.mail`, com publisher confirms | `cmd/mailer` | quem já tem RabbitMQ |

Assim o foldex continua sendo o que é — um self-hosted que sobe com `docker compose
up` e um Postgres — e quem tem infra de broker liga uma variável.

---

## 3. Modelo de dados

### 3.1 Migration `000034_mail_outbox`

```sql
CREATE TABLE mail_outbox (
  id                 BIGSERIAL PRIMARY KEY,
  template           TEXT NOT NULL,
  recipient          TEXT NOT NULL,
  payload_ciphertext BYTEA NOT NULL,
  payload_nonce      BYTEA NOT NULL,
  locale             TEXT NOT NULL DEFAULT 'en',
  status             TEXT NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending','publishing','published','failed')),
  attempts           INT NOT NULL DEFAULT 0,
  claim_token        UUID,
  claimed_at         TIMESTAMPTZ,
  last_error         TEXT,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  published_at       TIMESTAMPTZ
);

CREATE INDEX mail_outbox_claim_idx
  ON mail_outbox (created_at)
  WHERE status = 'pending';

CREATE INDEX mail_outbox_stuck_idx
  ON mail_outbox (claimed_at)
  WHERE status = 'publishing';
```

**Decisões de coluna:**

- **Não há `user_id`.** O destinatário é um endereço, não uma conta: um convite
  precede a existência da conta, e um `forgot` para endereço desconhecido não tem
  usuário para referenciar. Isso também mantém a tabela **fora do escopo do backup**
  (o ZIP nunca carrega material de auth, `CLAUDE.md` §4) sem precisar de regra nova.
- **`template` + payload, não corpo renderizado.** A mensagem é `(nome do template,
  params)`. Renderizar no worker mantém o registro pequeno, permite corrigir um
  template sem drenar a fila, e é o que torna o `locale` útil.
- **`last_error` guarda razão normalizada**, no padrão de
  `preview.operationErrorReason` / `push.pushErrorReason` / `changecheck.fetchFailureReason`
  — `timeout` / `canceled` / `smtp_rejected` / `<genérico>`. Nunca o erro cru do
  servidor SMTP: §7 do repo proíbe vazar texto interno, e um erro de MTA
  frequentemente ecoa o envelope.
- **`claim_token`** é o que torna a publicação idempotente sob concorrência. O relay
  reivindica com `FOR UPDATE SKIP LOCKED` (mesmo padrão de
  `SystemFindDueForCheck`) e só marca `published` com CAS naquele token exato — um
  relay que dormiu e acordou depois de outro ter reivindicado a linha não sobrescreve
  o resultado.
- **`mail_outbox_stuck_idx`** existe para o varredor de linhas presas em
  `publishing` (relay morreu entre claim e confirm): elas voltam para `pending`
  depois de um TTL de claim.

**Retenção.** O sweeper existente (`internal/auth/sweeper.go`) ganha a limpeza:
`published` some depois de 7 dias; `failed` fica 90 dias (é evidência operacional,
e a mesma janela da trilha de auditoria).

### 3.2 Migration `000036_email_second_factor`

```sql
CREATE TABLE email_factor (
  user_id                  BIGINT PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
  confirmed_at             TIMESTAMPTZ,
  enrollment_token_version INT,
  enrollment_session_id    BIGINT REFERENCES session(id) ON DELETE SET NULL,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT email_factor_epoch_check
    CHECK (confirmed_at IS NOT NULL OR enrollment_token_version IS NOT NULL) NOT VALID
);

ALTER TABLE email_otp DROP CONSTRAINT email_otp_purpose_check;
ALTER TABLE email_otp ADD CONSTRAINT email_otp_purpose_check
  CHECK (purpose IN ('login_2fa', 'verify_email', 'enroll_email_2fa'));
```

A forma espelha `totp_secret` **de propósito**. O binding de época
(`enrollment_token_version`, `enrollment_session_id`) e o `CHECK ... NOT VALID` são
exatamente o que a migration `000025` aplicou ao TOTP, então os padrões de
`confirmTOTPRowTx` — confirmação por UPDATE condicional sob lock de `app_user`,
recusa se a época mudou — transferem sem invenção. Um fator novo que inventasse seu
próprio esquema de binding seria um fator novo com bugs novos.

Não há segredo a guardar: o "seed" do fator e-mail é o endereço, que já está em
`app_user.email`. A tabela é um marcador de cadastro, não um cofre.

### 3.3 `testdb.Reset`

**Ambas as tabelas precisam entrar em `testdb.resetStatement`.**
`TestResetCoversEveryTable` (`internal/testdb/drift_test.go:58-100`) consulta
`information_schema` e falha até que entrem — por desenho, e depois de a lista já
ter esquecido silenciosamente `folder`, `click_log`, `note` e `app_setting` em
ocasiões anteriores.

---

## 4. Templates

### 4.1 Estrutura

`internal/mailer/templates/`, servidos por `embed.FS`, com `html/template` para a
parte HTML e `text/template` para a parte texto:

```
templates/
  layout.html.tmpl          layout.txt.tmpl
  password_reset.{html,txt}.tmpl
  reset_unavailable.{html,txt}.tmpl
  login_code.{html,txt}.tmpl
  invite.{html,txt}.tmpl
  verify_email.{html,txt}.tmpl
  admin_recovery.{html,txt}.tmpl
  session_revoked.{html,txt}.tmpl
  recovery_code_used.{html,txt}.tmpl
  account_converted.{html,txt}.tmpl
  enroll_email_code.{html,txt}.tmpl      ← novo (ADR-37)
  strings.{en,pt,es}.json
```

### 4.2 O que não muda

- **A parte texto continua obrigatória em toda mensagem.** O `render` atual só emite
  `multipart/alternative` quando há HTML (`mailer.go:242-247`), e a regra
  "toda mensagem permanece legível num cliente que recusa HTML" continua valendo.
- **`session_revoked` e `reset_unavailable` continuam sem link.** O primeiro é
  anti-phishing: um alerta de "suas sessões foram encerradas" com um link clicável é
  o formato exato que um atacante imitaria. O segundo é ADR-31: um link de reset ali
  deixaria o controle da caixa postal ressuscitar uma credencial de senha numa conta
  que migrou para o Google.
- **`LoginCodeMessage` mantém o código no `Subject`.** É o que permite ler o código
  na notificação do celular sem abrir o e-mail.
- Os headers atuais permanecem: `Auto-Submitted: auto-generated`, Q-encoding de
  Subject e From-Name, `sanitizeHeader` contra injeção de CRLF, e o dot-stuffing de
  `mailer.go:259-268`.

### 4.3 O que muda

- `html/template` faz escaping **por contexto** — some o `html.EscapeString` manual,
  que hoje depende de cada autor de template lembrar de chamá-lo.
- **i18n.** O outbox carrega `locale` e o worker renderiza no idioma do
  destinatário. `en` é a fonte da verdade, `pt` e `es` em paridade total — a mesma
  regra da UI (`CLAUDE.md` §1). Um locale desconhecido cai em `en`.
- Ganha `Message-ID` (necessário para threading e para diagnosticar entrega com o
  provedor) e `List-Unsubscribe` **apenas nas mensagens de notificação**
  (`recovery_code_used`, `session_revoked`, `account_converted`) — nunca nas
  transacionais, que não são opt-out.

---

## 5. Topologia AMQP

```
exchange foldex.mail            direct, durable
   └─ rk "send" ─────────────▶ queue foldex.mail.send        durable, quorum
                                  x-dead-letter-exchange: foldex.mail.dlx
                                  x-dead-letter-routing-key: retry

exchange foldex.mail.dlx        direct, durable
   ├─ rk "retry" ────────────▶ queue foldex.mail.retry       durable
   │                              x-message-ttl: 60000 | 300000 | 1800000
   │                              x-dead-letter-exchange: foldex.mail
   │                              x-dead-letter-routing-key: send
   └─ rk "dead" ─────────────▶ queue foldex.mail.dead        durable, sem TTL
```

- **Retry com backoff por TTL de fila**, não por `sleep` no consumidor: uma mensagem
  nack-ada sem requeue cai na `.retry`, expira, e o DLX a devolve para `.send`. O
  header `x-death` conta as passagens; ao esgotar o escalonamento (1 min → 5 min →
  30 min) o worker roteia para `.dead` e o relay marca `mail_outbox.status='failed'`.
  Um `sleep` no consumidor seguraria o prefetch e transformaria latência em perda de
  vazão.
- **Filas quorum**, não clássicas espelhadas: é o que o RabbitMQ 3.8+ recomenda para
  fila durável replicada, e o que sobrevive a failover sem perder confirmação.
- **Publisher confirms obrigatórios no relay.** Sem confirm, sem
  `status='published'`. Um publish sem confirm é fire-and-forget com aparência de
  durabilidade.
- **Prefetch baixo (1–4)** por consumidor. Envio SMTP é I/O serial; prefetch alto só
  aumenta quantas mensagens ficam presas quando um consumidor morre.
- **TLS obrigatório para broker não-loopback**, aplicado em
  `config.validateSecureDefaults` no mesmo formato da regra que já recusa
  `MAIL_INSECURE_SKIP_VERIFY=1` contra host real.
- Dependência nova: `github.com/rabbitmq/amqp091-go` (o `streadway/amqp` está
  deprecado e não recebe correções).

---

## 6. `cmd/mailer`

Binário e imagem próprios, no shape de `cmd/server/main.go`: logger raiz embrulhado
em `logsafe.RedactHandler` **primeiro**, depois config, depois dependências
construídas e injetadas de cima para baixo. Sem DI, sem estado global, sem service
locator (`CLAUDE.md` §7).

- Consome `foldex.mail.send`, decifra o payload com `secrets.Cipher`, renderiza o
  template no `locale` da mensagem, envia via `mailer.Mailer` — os drivers `smtp` e
  `log` atuais são reaproveitados **sem nenhuma mudança**.
- `Ack` só depois de `Send` retornar `nil`. `Nack(requeue=false)` em falha, para o
  DLX cuidar do backoff. Requeue imediato seria um loop apertado contra um servidor
  que acabou de recusar.
- **Não fala com o Postgres.** O estado do outbox é responsabilidade do relay; o
  worker só conhece a fila. Isso mantém o worker sem credencial de banco, o que
  importa porque ele é o processo que decifra credenciais.
- Lifecycle no padrão do repo: `stopped atomic.Bool` → `cancel()` → `wg.Wait()`,
  canal de jobs **nunca fechado**, deadline de processo de 12 s.

---

## 7. Segundo fator por e-mail

### 7.1 O que muda no modelo

`user.totp_enabled` é hoje a **única** noção de "esta conta tem segundo fator",
derivada por `EXISTS` e nunca cacheada (`repository.go:64-84`), lida em cinco
lugares. Passa a haver três noções:

```
totp_enabled       EXISTS(totp_secret confirmado)     -- inalterado
email_2fa_enabled  EXISTS(email_factor confirmado)    -- novo
has_second_factor  totp_enabled OR email_2fa_enabled  -- derivado, novo
```

Todas derivadas, pelo mesmo motivo que a primeira: um booleano armazenado precisaria
ser atualizado em quatro lugares e discordaria da realidade na primeira vez que um
fosse esquecido — e a direção da discordância decide se o login exige um código que
o usuário não consegue produzir.

`secondFactorPurpose` (`twofa_handler.go:50-65`) passa a divergir em
`has_second_factor`. O purpose continua se chamando `totp` — o
`auth_challenge_purpose_check` é fechado e renomear custaria uma migration por
cosmética — mas passa a significar **"deve um segundo fator"**. Qual método satisfaz
é decidido pela lista `methods` do payload, que o frontend já consome
(`TwoFactorScreen.tsx:96`).

### 7.2 O guard que não pode cair

```go
func (h *Handler) emailFactorAvailable(purpose ChallengePurpose, mailboxAlreadyProven bool, u *User) bool {
	return purpose == PurposeTOTP &&
		!mailboxAlreadyProven &&      // ← intacto
		h.mailer.Driver() == "smtp" &&
		u.Email2FAEnabled             // ← novo: cadastro, não disponibilidade
}
```

`mailbox_already_proven` (migration `000019`) existe para impedir a cadeia:

```
/password/forgot → link na caixa postal → /password/reset → desafio
                 → /2fa/email → código na MESMA caixa postal → sessão
```

Ele é *sticky*: `CreateChallenge` faz OR do valor herdado com o novo
(`repository_2fa.go:197`), então um login por senha subsequente não o lava. Isso
continua exatamente como está.

**A consequência nova, e é a parte que mais importa:** uma conta cujo **único** fator
é e-mail, entrando por link de reset, fica sem método de e-mail disponível. Por isso
o cadastro do fator e-mail **obriga a emissão de códigos de recuperação**, igual ao
TOTP. Eles são a saída de lockout — sem eles o fluxo tranca o usuário fora da própria
conta, e o guard de segurança viraria um bug de disponibilidade.

### 7.3 Fluxo de cadastro

```
POST /api/auth/2fa/email/start     sessão + senha, ou desafio enroll_2fa
     → grava email_factor pendente com token_version (+ session_id no caminho de sessão)
     → emite OTP com purpose 'enroll_email_2fa' e enfileira no outbox

POST /api/auth/2fa/email/confirm   {code}
     → confirma o fator, emite os recovery codes e (no caminho pré-auth) consome o
       desafio e emite a sessão — tudo na MESMA transação

POST /api/auth/2fa/email/disable   {password, code}
     → apaga o fator, apaga os recovery codes se não sobrar nenhum fator,
       bumpa token_version e revoga as outras sessões
```

Espelha `/2fa/totp/{start,confirm,disable}` linha a linha, incluindo o CAS de
confirmação sob lock de `app_user` e o `ErrTOTPAlreadyConfirmed` →
`ErrFactorAlreadyConfirmed`.

### 7.4 Step-up de sessão

Quatro call sites chamam `verifyTOTPProof` diretamente — `DisableTOTP`,
`RegenerateRecoveryCodes`, `SetPassword` e o vínculo OAuth. Passam a chamar
`verifySecondFactorProof`, que aceita TOTP, OTP por e-mail ou código de recuperação.
O limitador in-memory `stepUpUser` (5 tentativas / 15 min) continua sendo o teto
desses caminhos, porque não há `auth_challenge` neles para carregar o orçamento no
banco.

### 7.5 Administradores

`RequireAdmin` re-verifica `HasConfirmedTOTP` a cada request
(`middleware.go:143-153`), o que é o que faz a política valer para sessões antigas.
Vira `HasConfirmedSecondFactor`.

Um admin cujo único fator é e-mail é mensuravelmente mais fraco que um com
autenticador: a caixa postal já é o canal de recuperação, e concentrar os dois no
mesmo lugar reduz a superfície que um atacante precisa comprometer. Por isso isso
**não** vira constante — vira knob na política da instância (ADR-35), com piso no
comportamento mais permissivo e o dono podendo apertar:

```
instance_policy.admin_second_factor ∈ { "any", "totp_only" }   piso: "any"
```

### 7.6 Frontend

- `TwoFactorSection.tsx` ganha um seletor de método (app × e-mail) antes do fluxo
  atual; `useTwoFactorController.ts` ganha as ações correspondentes.
- `EnrollTotpScreen.tsx` vira `EnrollFactorScreen` com a mesma escolha, **preservando
  o ref-guard de mount** (o `started` que impede o duplo-mount do StrictMode de
  substituir o seed sob um usuário que já está escaneando o QR) e a regra de "sem
  skip".
- `TwoFactorScreen.tsx` já lê `pending.methods` do servidor — nada muda ali.
- `OtpInput` é reaproveitado **sem alteração**: contrato de seis células,
  `autoComplete="one-time-code"` só na primeira, `onComplete` disparando uma vez só.
- i18n: chaves novas em `en`/`pt`/`es`, com `en` como fonte.

---

## 8. Configuração

| Var | Default | Onde | Observação |
|---|---|---|---|
| `MAIL_TRANSPORT` | `inproc` | `config.go` | `inproc` \| `amqp`; driver desconhecido **recusa boot** |
| `AMQP_URL` | `""` | `config.go` | obrigatório quando `MAIL_TRANSPORT=amqp` |
| `AMQP_EXCHANGE` | `foldex.mail` | `config.go` | |
| `AMQP_QUEUE` | `foldex.mail.send` | `config.go` | |
| `AMQP_PREFETCH` | `4` | `config.go` | clampado em 1..64 |
| `MAIL_OUTBOX_BATCH` | `32` | `config.go` | linhas por claim do relay |
| `MAIL_OUTBOX_POLL` | `5s` | `config.go` | intervalo de varredura |

Segue as convenções existentes: `envOr`/`envInt`/`envBool` com default inline para o
opcional; **condicionalmente obrigatório é verificado no construtor do pacote
consumidor**, não em `config.Load` — exatamente como `mailer.New` já recusa
`MAIL_DRIVER=smtp` sem `MAIL_HOST`, com a justificativa de que "`MAIL_DRIVER=smpt` e
o e-mail indo silenciosamente para o log é uma falha muito pior que recusar subir".

`validateSecureDefaults` ganha uma recusa: `AMQP_URL` com esquema `amqp://` (sem
TLS) apontando para host não-loopback.

---

## 9. Testing strategy

**Backend (unit).** Renderização de template por locale; cifra/decifra do payload
com round-trip e com tag corrompida (deve falhar, não degradar); escalonamento de
backoff; normalização de `last_error`.

**Backend (integration, tag `integration`).**
- Outbox: `Enqueue` participa da transação — rollback do handler não deixa linha
  órfã, e commit deixa exatamente uma.
- Claim concorrente: dois relays, `SKIP LOCKED`, nenhuma linha publicada duas vezes.
- Linha presa em `publishing` volta para `pending` depois do TTL de claim.
- **Testcontainer de RabbitMQ**, no molde de
  `internal/storage/rustfs_testhelper_integration_test.go`: `GenericContainer` com
  imagem **pinada por digest**, `wait.ForLog`/`ForHTTP`, `t.Cleanup` terminando.
- Fluxo fim-a-fim: handler → outbox → relay → fila → worker → `captureMailer`.
- 2FA por e-mail: cadastro, confirmação atômica com recovery codes, disable, e o
  teste que importa — **`TestEmail2FA_ResetIssuedChallengeStillRefusesTheEmailFactor`**,
  construindo o caso em vez de depender de ordem de execução.

**Frontend.** Seletor de método; `EnrollFactorScreen` nos dois caminhos; paridade de
chaves i18n nos três locales.

**Cobertura.** Gate inalterado: ≥85% statements, ≥80% branches.

---

## 10. Faseamento

| PR | Escopo | Entregável independente | Estado |
|---|---|---|---|
| **1** | `mail_outbox` + cifra + templates `embed`/`html/template` + i18n. Transporte segue `inproc`. `Reserve/Publish/Release` sai dos handlers. | Entrega durável e com retry, **sem broker nenhum**. | **Entregue** |
| **2** | `MAIL_TRANSPORT=amqp`, relay, `cmd/mailer`, topologia + DLQ, compose, imagem, CI. | Rabbit ligado. | Proposto |
| **3** | `email_factor`, endpoints de cadastro, `has_second_factor`, step-up, política de admin, UI. | 2FA por e-mail. | Proposto |

### 10.1 Onde a fase 1 divergiu deste documento

Três desvios, todos deliberados:

1. **O `mailer.Dispatcher` não virou sink do transporte `inproc` — foi removido.** §2.4
   o previa como sink, mas ele **descarta** um `Send` que falha (`dispatcher.go:98-100`),
   então um PR1 apoiado nele entregaria durabilidade só até a primeira recusa de SMTP —
   e "entrega durável e com retry, sem broker nenhum" era literalmente a promessa da
   fase. O `Relay` passou a ser o próprio worker do `inproc`: envia, marca `published`
   apenas no sucesso, e no fracasso reagenda. Mantê-los lado a lado seria código morto
   com aparência de camada.

2. **Backoff virou coluna (`next_attempt_at`), não TTL de fila.** §5 resolve o backoff
   com a fila `.retry` do Rabbit, o que só existe na fase 2. Sem broker, a alternativa
   seria `sleep` no worker — que segura o slot e transforma um destinatário lento em
   fila parada para todos, exatamente o que §5 já rejeita. A coluna dá o mesmo
   escalonamento (1 → 5 → 15 → 30 → 60 min) sem prender nada. Quando a fase 2 chegar, o
   TTL de fila governa o retry do transporte e a coluna continua governando o do relay;
   são camadas distintas, não duplicação.

3. **Falha permanente liquida na hora.** Não estava no documento. Um `unknown_template`
   ou um payload que não decifra não passa a funcionar na quarta tentativa, e gastar
   seis só atrasa o operador descobrir.

4. **A cifra usa uma SUBCHAVE derivada, não a `AUTH_ENCRYPTION_KEY` direta.** §2.3 dizia
   "chaveada por `AUTH_ENCRYPTION_KEY`", que é o que o seed TOTP faz. Mas o seed cifra uma
   vez por usuário e o outbox cifra uma vez por link de reset, código de entrada e convite
   — sob uma chave só, as duas distâncias até o limite de aniversário do GCM viram um
   orçamento compartilhado, e rotacionar uma passa a exigir destruir a outra. O repo já
   tinha o padrão certo nos MACs de código (subchaves por propósito); o outbox agora o
   segue via `secrets.NewDerivedCipher`. O corpo AMQP da fase 2 continua sendo o mesmo blob
   cifrado, então `cmd/mailer` deriva a mesma subchave a partir da mesma variável.

E uma decisão que **não** foi tomada: o force-reset administrativo continua síncrono
(§11.2 / §12.1). Muda invariante de segurança e espera aval explícito.

O `locale` vem do `Accept-Language` da requisição (§12.3, opção B). A consequência
conhecida vale: um convite disparado por um administrador anglófono chega em inglês
para um convidado lusófono. A coluna em `app_user` continua sendo a resposta melhor, e
continua pendente.

Cada PR fecha o §6 (Definition of Done) por completo — testes, cobertura, docs,
README nos dois idiomas, sweep de 5 agentes e bump semver.

---

## 11. Invariantes de `CLAUDE.md` §4 que este documento altera

Nomeados explicitamente, porque nenhum deles deve mudar por acidente:

1. **"Auth mail has one process-owned bounded dispatcher… queue admission reserved
   BEFORE superseding/persisting a credential."** O dispatcher continua existindo
   como sink do transporte `inproc`, mas a **admissão sai dos handlers**: o que
   garante a ordem passa a ser a transação, não a reserva. Substituto: *"toda
   mensagem é gravada em `mail_outbox` na mesma transação que a credencial que ela
   carrega"*.

2. **"Administrator force-reset stays synchronous inside its transaction, and SMTP
   failure rolls the token back."** Com outbox, token e mensagem commitam juntos e a
   entrega é garantida-eventual. **Some o `503 mail_unavailable`**, e uma
   indisponibilidade transitória do SMTP deixa de negar a operação ao administrador.
   O que se preserva é a propriedade que motivava a regra — *"um administrador nunca
   instala uma credencial que o alvo não recebe"* — agora por durabilidade em vez de
   por acoplamento síncrono. **Requer aval explícito** (§12.1).

3. **"`user.totp_enabled` is derived with EXISTS, never stored."** Continua verdade,
   e ganha duas irmãs. O que muda é que *ele deixa de ser a resposta* para "esta
   conta tem segundo fator" — quem responde isso passa a ser `has_second_factor`.

4. **"`AUTH_REQUIRE_2FA_FOR_ADMINS` … `RequireAdmin` re-checks the current confirmed
   TOTP row."** Passa a checar fator confirmado de qualquer tipo, sujeito a
   `instance_policy.admin_second_factor`.

O invariante que **não** muda, e é o mais importante do documento: *"One channel must
never satisfy both factors."* `mailbox_already_proven` continua sticky e continua
recusando o fator e-mail em desafio nascido de reset.

---

## 12. Decisões (resolvidas em 2026-08-18)

As quatro questões foram respondidas pelo dono da instância. Ficam aqui com a
resposta e o que cada uma implica, porque duas delas mexem em invariante.

### 12.1 Force-reset administrativo passa a ser assíncrono — **aprovado**

O `503 mail_unavailable` deixa de existir. Token e mensagem commitam juntos e a
entrega é garantida-eventual, como todo o resto.

O que se preserva é a propriedade que motivava a regra — *um administrador nunca
instala uma credencial que o alvo não recebe* — agora por **durabilidade** em vez
de por acoplamento síncrono: a mensagem não pode ser perdida, então "commitou"
passa a implicar "vai chegar". O que se perde é a confirmação imediata: antes o
admin sabia na hora que o SMTP aceitou; agora ele sabe que a mensagem existe e
será entregue ou marcada `failed`. O ganho é que um blip transitório deixa de
negar ao administrador uma operação a que ele tem direito, descartando o token.

A exigência de `MAIL_DRIVER=smtp` continua: o driver `log` imprime o corpo no
stdout, e essa credencial não pode ir para lá.

### 12.2 `admin_second_factor` — piso **`any`**

E-mail conta como segundo fator para administrador. O dono pode apertar para
`totp_only` pela política da instância.

**A objeção que motivou a decisão está correta e vale registrar**, porque a
formulação anterior deste documento a tratava com menos rigor do que devia: com
senha + e-mail o atacante precisa **das duas** coisas. O fator e-mail não é
decorativo e isso não é "um fator disfarçado de dois".

O ponto que sobra é mais estreito: os dois fatores **não são independentes**,
porque a caixa postal é também o canal que reseta a senha — quem a controla ganha
um fator de graça e tem um caminho para tentar o outro no mesmo lugar. O que
impede esse fechamento é `mailbox_already_proven` (§7.2), que **permanece
intacto**. A diferença remanescente em relação ao TOTP não é aritmética de
fatores, é de superfície: comprometer caixa postal é muito mais comum que
comprometer um autenticador, e a caixa costuma ser protegida por senha — mesma
classe de credencial, com risco de reuso. Daí o knob existir, com piso no
comportamento mais permissivo.

### 12.3 `locale` vira preferência de perfil — **coluna, editável pelo usuário**

Mais forte que a opção que este documento recomendava. Não é só uma coluna em
`app_user`: é um campo **do perfil**, que o próprio usuário edita em
Configurações, ao lado do nome.

Consequências:

- Migration `000035_user_locale` adiciona `app_user.locale` (nullable — NULL
  significa "sem preferência" e mantém o comportamento atual).
- `PATCH /api/auth/profile` passa a aceitar `locale`, validado contra os
  catálogos que existem, com a mesma DTO estrita que já recusa campo desconhecido.
- A resolução passa a ser: **preferência do destinatário → `Accept-Language` de
  quem disparou → `en`**. Isso resolve o caso que o §10.1 registrou como custo
  conhecido: um convite disparado por administrador anglófono agora chega no
  idioma do convidado, se ele tiver escolhido um. Para um convite, o convidado
  ainda não tem conta — então ali o header continua sendo a única fonte, e isso
  é inerente, não uma lacuna de implementação.
- O seletor da topbar (hoje só `localStorage`) passa a refletir a preferência
  salva quando existe, para não haver duas verdades sobre o idioma.

### 12.4 Broker — **vhost dedicado**

`/foldex` com usuário próprio. Isola permissão, permite quota separada e mantém a
fila fora do alcance dos outros projetos que compartilham o servidor. O custo é
uma etapa de provisionamento a mais, documentada no README.

---

## 13. Ordem de execução do que resta

| Fase | Escopo | Depende de | Estado |
|---|---|---|---|
| **A** | `locale` no perfil (12.3) + force-reset assíncrono (12.1) | — | **entregue** |
| **B** | `MAIL_TRANSPORT=amqp`, sink AMQP, `cmd/mailer`, vhost dedicado (12.4) | — | **entregue** |
| **C** | `email_factor`, cadastro, `has_second_factor`, step-up, `admin_second_factor` (12.2) | A (locale) | **entregue** |

A e B eram independentes entre si. C usa o `locale` de A para o código de
cadastro, e foi a maior das três.

### 13.1 Onde a fase B divergiu deste documento

**A escada de retry virou uma fila por degrau.** §5 descrevia uma fila `.retry`
única com `x-message-ttl` escalonado (`60000 | 300000 | 1800000`), o que não é
implementável como escrito: uma fila tem UM TTL, e a alternativa — TTL por
mensagem — esbarra no fato de o RabbitMQ expirar mensagens apenas a partir da
CABEÇA. Uma mensagem de 30 minutos na frente seguraria todas as de 1 minuto
atrás dela, e o backoff passaria a ser "o maior degrau que alguém já pediu".
Ficaram três filas (`.retry.1m`, `.retry.5m`, `.retry.30m`), cada uma com o seu
próprio TTL, e cada degrau espera exatamente o que promete.

**O contador de tentativas não é `x-death`.** §5 propunha contar as passagens
pelo header `x-death`. Ele existe, mas sua semântica difere entre tipos de fila e
entre versões do broker, e o número que decide se um link de reset é abandonado
não deveria depender disso. O worker mantém `x-foldex-attempt` e o lê tolerando
qualquer largura de inteiro que um cliente AMQP tenha usado para codificá-lo.

**O worker republica em vez de dar nack.** §6 dizia `Nack(requeue=false)` para o
DLX cuidar do backoff. Isso só funciona com um destino fixo: o nack roteia pela
`x-dead-letter-routing-key` da fila, que não sabe escolher um degrau. O worker
publica explicitamente no DLX com a chave do degrau e só então dá `Ack`. O custo
é reentrega numa janela de crash, sobre uma mensagem que já havia FALHADO.

**Ganhou um `DeadLetterWatcher`, que o documento não previa.** §5 dizia que "ao
esgotar o escalonamento o worker roteia para `.dead` e o relay marca
`status='failed'`" — mas o relay não consome nada e §6 proíbe o worker de falar
com o Postgres, então ninguém marcaria. Sem alguém fechando esse laço,
`published` passaria a significar apenas "o broker aceitou", e uma mensagem morta
no último degrau deixaria a linha lendo `published` para sempre. O watcher roda
no **backend** (que já tem o banco), consome `foldex.mail.dead` e lê apenas id e
razão — ambos fora do blob cifrado —, então a proibição de §6 fica intacta.

**`config.LoadMailer` existe porque `config.Load` exige `DB_URL`.** O documento
não tratou disso, e sem um carregador separado o worker precisaria receber uma
DSN que nunca abre — desfazendo exatamente o isolamento que motivou o binário
separado.

### 13.2 Onde a fase C divergiu deste documento

**O step-up ganhou um endpoint de envio que o documento não previu.** §6 dizia
que os quatro call sites passariam a aceitar "TOTP, OTP por e-mail ou código de
recuperação", mas não disse de onde viria o OTP: esses caminhos são autenticados
por sessão e não têm `auth_challenge` para pendurar um código. Sem
`POST /2fa/email/send`, uma conta cujo único fator é e-mail teria de gastar um
código de recuperação — uma credencial de saída de lockout — para desligar o
próprio fator, e não conseguiria definir senha de jeito nenhum. O código sai sob
purpose próprio (`step_up_2fa`), separado de `enroll_email_2fa` porque um prova
uma caixa que ninguém aceitou ainda e o outro é um fator já aceito se
apresentando.

**O proof passou a ser VERIFICADO sem ser gasto.** O documento tratava o step-up
como uma checagem booleana. Os três métodos agora produzem um `SecondFactorProof`
consumido dentro da transação da operação que autoriza, porque a propriedade que
o CLAUDE.md §4 já exigia do TOTP vale ainda mais para código de recuperação: um
código queimado por uma escrita que falhou custa ao usuário uma volta para dentro
da própria conta, por uma operação que não aconteceu. `SetPassword` também
passou a checar `email_factor` na re-verificação dentro da transação — lendo só
`totp_secret`, ela dispensava o proof justamente para a conta que só tem e-mail.

**`admin_second_factor` precisou de um piso aplicado na leitura.** §6 propôs o
knob com piso `any`, mas validação estrita sobre um documento salvo antes desta
release recusaria **toda** escrita de política numa instância existente, por uma
chave que o owner nunca tocou. `WithDefaults()` roda no `Put` e no repositório.

**"Este fator pode ser removido?" virou uma função só.** O documento não tratou
do assunto, e a primeira implementação teve a regra duplicada e divergente entre
os dois endpoints de disable. `mayRemoveFactor` responde a pergunta uma vez e
alimenta também o `GET /2fa`, que passa a devolver `can_disable_totp` /
`can_disable_email` para a tela renderizar a resposta do servidor em vez de
recalcular a política no navegador.

**A escolha de método na tela de cadastro obrigatório é condicional.** §7 pedia
"a mesma escolha" na `EnrollTotpScreen`. Numa instância sem SMTP não há escolha
alguma, então a tela pula o seletor e começa o autenticador direto: uma pergunta
com uma resposta possível é fricção pura numa tela obrigatória da qual o admin
não pode sair.

**O broker dos testes virou compartilhado no pacote.** Não é da fase C, mas foi
encontrado ao rodar a suíte completa: `internal/mailoutbox` subia um contêiner
RabbitMQ por teste, seis deles dividindo o timeout único de dez minutos do Go.
Numa máquina carregada o pacote estourava e reportava um teste travado — falha
que não diz nada sobre o código. O contêiner agora é único por pacote
(`sync.Once`, como o `testdb.Shared`) e o isolamento passou para a topologia,
com exchange e filas por teste. O pacote caiu de >600 s para ~104 s.
