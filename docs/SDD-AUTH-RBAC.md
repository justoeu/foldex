# SDD — Autenticação, RBAC e segmentação multi-usuário

> Software Design Document. Status: **Draft · v1.0 · 2026-08-03**
> Owner: foldex
> Related ADRs: **ADR-30** (sessão / CSRF / RBAC), **ADR-31** (OAuth Google + portabilidade de conta),
> **ADR-32** (`/go/{id}` numérico vira opt-in) — todos em `docs/ARCHITECTURE.md`.
> Supersede: **ADR-3** (“Sem auth no MVP”).

---

## 1. Visão geral

### 1.1 Problema

O foldex é single-user **por design**, e isso está codificado em três camadas que hoje se sustentam mutuamente:

- **Não há identidade.** O único controle de acesso é `SHARED_SECRET` — um segredo estático comparado por HMAC em `internal/server/router.go:195-216`. Ele responde “sim” ou “não”, nunca “quem”. Não existe principal no `context`, não existe sessão, não existe cookie.
- **Não há posse.** Nenhuma das tabelas de conteúdo (`link`, `note`, `folder`, `tag`, `push_subscription`) tem coluna de dono. `tag.name` e `link.url` são UNIQUE **globais** — restrições que só fazem sentido quando existe exatamente um usuário.
- **Não há recuperação.** A master password do ADR-29 mora em `app_setting`, uma tabela KV singleton. Ela é *da instância*, não *de alguém*.

O pedido é transformar isso num sistema multi-usuário real: tela de autenticação, recuperação de senha, OTP, RBAC, administração de usuários e segmentação de links e notas por usuário.

Isso não é uma feature — é uma **mudança de postura do produto**. Toda linha de conteúdo passa a ter dono, ~60 métodos de repositório passam a precisar de escopo, e o threat model da §0 do `CLAUDE.md` (“single-user, local network, no public exposure”) deixa de valer. Um erro de escopo aqui não produz um bug de UI: produz vazamento de dados entre pessoas.

### 1.2 Decisão

Sete peças, entregues em quatro PRs encadeados (§15):

1. **Migration `000017`** — 12 tabelas de identidade, `user_id NOT NULL` nas cinco tabelas de conteúdo, troca das UNIQUEs globais por compostas, e FKs compostas que tornam vazamento cross-tenant **estruturalmente impossível** no banco.
2. **`internal/pkg/authctx`** — pacote folha com `UserID`, `Role` e `Principal`. Todo repositório passa a receber `uid` explícito.
3. **`internal/auth`** — sessão por cookie httpOnly com rotação de refresh e detecção de reuso, CSRF double-submit assinado, RBAC, convites, bootstrap do primeiro admin.
4. **`internal/mailer`** — SMTP com fallback para driver de log, Mailpit no compose de dev.
5. **2FA** — TOTP (RFC 6238) com códigos de recuperação, obrigatório para admins, mais OTP por e-mail como fallback.
6. **`internal/oauthgoogle`** — Authorization Code + PKCE, com portabilidade de conta senha→Google.
7. **Frontend** — gate de autenticação acima do `<App/>`, 9 telas, tela de admin de usuários.

### 1.3 Goals

- **Isolamento total por usuário**: nenhum usuário — inclusive admin — enxerga conteúdo de outro. A garantia é do **banco**, não da disciplina do handler.
- **Falha em compilação, não em vazamento**: a escolha de arquitetura é a que transforma “esqueci de filtrar” em erro de build.
- **Anti-enumeração**: nenhum endpoint público revela se um e-mail existe na base.
- **Migração sem perda**: a instalação single-user existente vira a conta do primeiro admin, com todos os dados adotados automaticamente.
- **Revogação instantânea**: desabilitar um usuário ou encerrar uma sessão vale na request seguinte, não no fim do TTL do token.

### 1.4 Non-goals (v1)

- **Compartilhamento entre usuários.** Não há “compartilhar pasta com fulano”. Justificativa: é uma feature de colaboração com modelo de permissão próprio (ACL por objeto), e o pedido aqui é segmentação. Workaround: exportar e importar.
- **Papéis além de `admin` e `user`.** A coluna é TEXT com CHECK, então adicionar um terceiro papel é uma migration de uma linha. Justificativa: não há caso de uso concreto para um papel intermediário, e RBAC especulativo envelhece mal.
- **SSO corporativo (SAML/OIDC genérico).** Só Google. Justificativa: `user_identity` já é `(provider, subject)`, então adicionar um provider é aditivo. Workaround para quem precisa hoje: senha + TOTP.
- **Auto-provisionamento por OAuth.** Entrar com Google numa conta que não existe **não** cria conta. Justificativa: self-signup está desligado por decisão de produto (§7.3).
- **Rotação automática das chaves de assinatura.** `AUTH_ENCRYPTION_KEY` é carregada e usada; trocar exige reenroll de TOTP. Justificativa: chave de instância single-tenant, com o mesmo status que `FOLDER_UNLOCK_KEY` já tem.

---

## 2. Arquitetura

### 2.1 Camadas e onde a identidade entra

```
                    ┌─────────────────────────────────────────────┐
   request  ───────▶│ RealIP · RequestID · Recoverer · bodyLimit   │
                    │ slogRequest · CORS                          │
                    └───────────────────┬─────────────────────────┘
                                        │
                    ┌───────────────────▼─────────────────────────┐
                    │ sharedSecretGuard   (perímetro, opcional)   │
                    └───────────────────┬─────────────────────────┘
                                        │
              ┌─────────────────────────┼──────────────────────────┐
              ▼                         ▼                          ▼
      ┌───────────────┐       ┌──────────────────┐       ┌─────────────────┐
      │  /api/auth/*  │       │ Authenticate     │       │ /healthz  /go/  │
      │  (público)    │       │ RequireCSRF      │       │ /n/   (público) │
      │  login, otp,  │       └────────┬─────────┘       └─────────────────┘
      │  forgot,      │                │  Principal{UserID, Role} no context
      │  oauth, setup │                ▼
      └───────────────┘       ┌──────────────────┐      ┌────────────────────┐
                              │ handlers         │      │ RequireRole(admin) │
                              │ uid := MustUser  │─────▶│ /api/admin/*       │
                              └────────┬─────────┘      └────────────────────┘
                                       │ uid explícito
                                       ▼
                              ┌──────────────────┐
                              │ repositories     │  WHERE user_id = $1
                              │ (uid authctx.…)  │  ── sempre o 1º predicado
                              └────────┬─────────┘
                                       ▼
                              ┌──────────────────┐
                              │ Postgres         │  FKs compostas (user_id, id)
                              │                  │  ── rede de segurança final
                              └──────────────────┘
```

O ponto importante é que existem **três** barreiras independentes: o middleware que resolve quem é, o predicado `user_id` em cada query, e as FKs compostas no banco. Uma falha em qualquer uma delas ainda é contida pelas outras duas. Isso é deliberado: segmentação multi-tenant é a classe de bug em que uma única linha esquecida é um incidente.

### 2.2 Fluxo de login

```
POST /api/auth/login {email, password}
        │
        ├─ rate limit: login:ip:<RealIP>  e  login:em:<sha256(email)>
        │                                    (o de e-mail incrementa mesmo se o e-mail não existir)
        ▼
  ByEmail(email_normalized)
        │
        ├── miss ──▶ bcrypt contra hash DUMMY  ──┐   (nunca pular: é o oráculo de ~80 ms)
        └── hit  ──▶ bcrypt contra password_hash ┤
                                                 ▼
                              piso de 250 ms antes de responder
                                                 │
        ┌────────────────────────────────────────┴─────────────────────────────┐
        │                                                                      │
   falha (qualquer motivo: e-mail inexistente,                            sucesso
   senha errada, conta desabilitada)                                          │
        │                                                                      ▼
        ▼                                              ┌───────────────────────────────────────┐
  401 invalid_credentials                              │ admin sem TOTP confirmado?            │
  ── byte-idêntico nos três casos ──                   │   → challenge 'enroll_2fa' + fx_pa    │
                                                       │ TOTP confirmado?                      │
                                                       │   → challenge 'totp' + fx_pa          │
                                                       │ nenhum dos dois?                      │
                                                       │   → emite sessão                      │
                                                       └───────────────┬───────────────────────┘
                                                                       ▼
                                                       POST /api/auth/2fa/verify {code}
                                                                       │
                                                       TOTP → recovery code → email OTP
                                                                       │
                                                                       ▼
                                                   Set-Cookie: fx_at, fx_rt, fx_csrf
```

Nenhum caminho emite sessão antes do segundo fator quando ele se aplica. O cookie `fx_pa` que carrega o estado intermediário só autoriza `/api/auth/2fa/*` — ele não alcança endpoint de dados nenhum, e isso é travado por teste (§14).

### 2.3 Rotação de refresh com detecção de reuso

```
POST /api/auth/refresh  (cookie fx_rt)
        │
        ▼  uma transação SERIALIZABLE
   h := sha256(raw)
        │
        ├─ h ∈ session_used_token ?
        │       │
        │       ├─ SIM, e used_at > now() - 10s, família viva
        │       │     └──▶ ABA CORRENDO EM PARALELO, não ataque.
        │       │          Reemite os tokens correntes da família. Não rotaciona.
        │       │
        │       └─ SIM, fora da janela  ──▶  REUSO DETECTADO
        │             UPDATE session SET revoked_at=now(), revoked_reason='reuse_detected'
        │               WHERE family_id = <família>
        │             DELETE FROM session_used_token WHERE family_id = <família>
        │             log de segurança + e-mail "sua sessão foi encerrada"
        │             └──▶ 401 session_revoked, limpa todos os cookies
        │
        └─ NÃO
             SELECT ... FROM session WHERE refresh_token_hash = h FOR UPDATE
                ├─ miss / revogada / expirada         ──▶ 401 (respostas distintas por código,
                │                                          mas nenhuma revela e-mail)
                ├─ created_at < now() - 90d           ──▶ 401 session_expired (teto absoluto)
                ├─ dono não está 'active'             ──▶ revoga família, 401
                └─ ok:
                     INSERT INTO session_used_token (h, family_id, session_id)
                     UPDATE session SET access/refresh/csrf = novos hashes,
                                        rotated_at = now(), refresh_expires_at = now() + 30d
                     └──▶ 200 + três Set-Cookie novos
```

A **janela de graça de 10 s** é a diferença entre um app que funciona e um que desloga o usuário aleatoriamente: qualquer duplo-mount do SPA (StrictMode, duas abas, um reload rápido) dispara dois `/refresh` com o mesmo token. Sem a janela, o segundo é classificado como reuso e mata a sessão.

O teto absoluto de 90 dias existe porque `refresh_expires_at` **desliza** a cada rotação. Sem teto, um refresh token roubado e rotacionado indefinidamente é imortal.

---

## 3. Modelo de dados (migration `000017_multi_user_auth`)

### 3.1 Identidade

```sql
CREATE TABLE app_user (
    id                   BIGSERIAL PRIMARY KEY,
    email                TEXT NOT NULL,
    email_normalized     TEXT NOT NULL,   -- lower(btrim(email)); chave de lookup e de unicidade
    email_verified_at    TIMESTAMPTZ,
    name                 TEXT NOT NULL DEFAULT '',
    password_hash        TEXT,            -- NULL = conta não reivindicada OU Google-only
    role                 TEXT NOT NULL DEFAULT 'user',
    status               TEXT NOT NULL DEFAULT 'pending',
    master_password_hash TEXT,            -- ADR-29 sai de app_setting e vem para cá
    master_password_hint TEXT,
    token_version        INTEGER NOT NULL DEFAULT 0,
    last_login_at        TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT app_user_role_check     CHECK (role   IN ('admin','user')),
    CONSTRAINT app_user_status_check   CHECK (status IN ('pending','active','disabled')),
    CONSTRAINT app_user_email_norm_chk CHECK (email_normalized = lower(btrim(email)))
);
CREATE UNIQUE INDEX app_user_email_norm_uniq ON app_user (email_normalized);
```

`email_normalized` é **coluna armazenada**, não índice de expressão, porque três caminhos diferentes precisam concordar sobre a normalização: o lookup do login, o match do convite e a regra de vínculo do OAuth. Uma divergência entre eles é uma falha de segurança, não um bug de busca.

**Por que a master password migra para `app_user`.** No ADR-29 ela é da instância. Num mundo multi-tenant, uma master global deixaria qualquer admin limpar a senha de pasta de **outro** usuário — exatamente o bypass que o ADR-28 recusou. Ela passa a ser por-usuário, e `app_setting` fica vazia (mas sobrevive como tabela, para config genuinamente de instância).

```sql
CREATE TABLE user_identity (
    id            BIGSERIAL PRIMARY KEY,
    user_id       BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    provider      TEXT   NOT NULL,
    subject       TEXT   NOT NULL,   -- o `sub` imutável do provider; NUNCA o e-mail
    email_at_link TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ,
    CONSTRAINT user_identity_provider_check CHECK (provider IN ('google'))
);
CREATE UNIQUE INDEX user_identity_provider_subject_uniq ON user_identity (provider, subject);
CREATE UNIQUE INDEX user_identity_user_provider_uniq    ON user_identity (user_id, provider);
```

As duas UNIQUEs são as duas metades da regra anti-takeover: uma conta Google mapeia para no máximo um usuário foldex, e um usuário foldex vincula no máximo uma conta por provider.

**Invariante de credencial.** Uma conta `status='active'` precisa ter `password_hash IS NOT NULL` **ou** ao menos uma linha em `user_identity`. É verificado na aplicação (dentro das txs de conversão e de unlink) e travado por teste, não por CHECK — um CHECK teria de olhar outra tabela, o que exigiria trigger. Sem essa invariante, um bug na conversão cria contas sem credencial alguma, irrecuperáveis pela UI.

### 3.2 Sessões

```sql
CREATE TABLE session (
    id                 BIGSERIAL PRIMARY KEY,
    user_id            BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    family_id          UUID   NOT NULL,
    access_token_hash  BYTEA NOT NULL,
    access_expires_at  TIMESTAMPTZ NOT NULL,
    refresh_token_hash BYTEA NOT NULL,
    refresh_expires_at TIMESTAMPTZ NOT NULL,
    csrf_token_hash    BYTEA NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at         TIMESTAMPTZ,
    last_seen_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at         TIMESTAMPTZ,
    revoked_reason     TEXT,
    ip                 INET,
    user_agent         TEXT,
    CONSTRAINT session_revoked_reason_check CHECK (revoked_reason IS NULL OR revoked_reason IN
        ('logout','logout_all','reuse_detected','password_changed','admin_revoked','user_disabled','expired'))
);
CREATE UNIQUE INDEX session_access_hash_uniq  ON session (access_token_hash);
CREATE UNIQUE INDEX session_refresh_hash_uniq ON session (refresh_token_hash);
CREATE INDEX        session_user_idx          ON session (user_id, revoked_at);
CREATE INDEX        session_family_idx        ON session (family_id);
CREATE INDEX        session_refresh_exp_idx   ON session (refresh_expires_at);

CREATE TABLE session_used_token (
    token_hash BYTEA PRIMARY KEY,
    family_id  UUID   NOT NULL,
    session_id BIGINT NOT NULL REFERENCES session(id) ON DELETE CASCADE,
    used_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**Tokens são guardados como `sha256` em `BYTEA`, nunca em claro.** Um dump do banco não pode ser um kit de sequestro de sessão. E é `sha256`, não bcrypt, por dois motivos: os tokens são 256 bits aleatórios (não há dicionário a atacar, então o custo de bcrypt não compra nada) e a resolução acontece no hot path de **toda** request.

### 3.3 Convites, reset, desafios e 2FA

```sql
CREATE TABLE invite (
    id BIGSERIAL PRIMARY KEY, email TEXT NOT NULL, email_normalized TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'user', token_hash BYTEA NOT NULL,
    invited_by BIGINT REFERENCES app_user(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), expires_at TIMESTAMPTZ NOT NULL,
    accepted_at TIMESTAMPTZ, accepted_user_id BIGINT REFERENCES app_user(id) ON DELETE SET NULL,
    revoked_at TIMESTAMPTZ,
    CONSTRAINT invite_role_check CHECK (role IN ('admin','user'))
);
CREATE UNIQUE INDEX invite_token_hash_uniq ON invite (token_hash);
CREATE UNIQUE INDEX invite_open_email_uniq ON invite (email_normalized)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;   -- no máximo 1 convite vivo por e-mail

CREATE TABLE password_reset (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL, consumed_at TIMESTAMPTZ, requested_ip INET
);
CREATE UNIQUE INDEX password_reset_token_hash_uniq ON password_reset (token_hash);

-- Estado PRÉ-AUTH entre "senha OK" e "2FA OK". Não concede acesso a dado nenhum.
CREATE TABLE auth_challenge (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL,
    purpose TEXT NOT NULL,
    attempts SMALLINT NOT NULL DEFAULT 0,
    sends    SMALLINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ, ip INET, user_agent TEXT,
    CONSTRAINT auth_challenge_purpose_check
        CHECK (purpose IN ('totp','enroll_2fa','convert_google'))
);
CREATE UNIQUE INDEX auth_challenge_token_hash_uniq ON auth_challenge (token_hash);

CREATE TABLE email_otp (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    challenge_id BIGINT REFERENCES auth_challenge(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL, code_hash BYTEA NOT NULL, attempts SMALLINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    CONSTRAINT email_otp_purpose_check CHECK (purpose IN ('login_2fa','verify_email'))
);

CREATE TABLE totp_secret (
    user_id           BIGINT PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
    secret_ciphertext BYTEA NOT NULL,       -- AES-256-GCM
    secret_nonce      BYTEA NOT NULL,
    algorithm      TEXT     NOT NULL DEFAULT 'SHA1',
    digits         SMALLINT NOT NULL DEFAULT 6,
    period_seconds SMALLINT NOT NULL DEFAULT 30,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    confirmed_at TIMESTAMPTZ,
    last_used_counter BIGINT                -- guarda contra replay dentro da própria janela
);

CREATE TABLE recovery_code (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    code_hash BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), used_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX recovery_code_hash_uniq ON recovery_code (code_hash);
```

**O seed TOTP é cifrado, não hasheado.** Verificar TOTP exige o seed em claro, então hash está fora de questão; e um seed base32 em claro num `pg_dump` é um bypass permanente de 2FA. AES-256-GCM com chave carregada por `AUTH_ENCRYPTION_KEY` (env base64 → arquivo → autogeração em `0600`), exatamente a forma de `FOLDER_UNLOCK_KEY`.

**Códigos de recuperação usam `sha256`, não bcrypt** — e isso é uma decisão, não um descuido. A verificação precisa ser um lookup indexado `WHERE code_hash = $1`; com bcrypt seria um scan da tabela inteira comparando sequencialmente contra as 10 linhas (~1 s no `DefaultCost`), e ainda assim sem ganho: os códigos têm ~50 bits de entropia, gerados por `crypto/rand`. É a entropia que os torna seguros, não o custo do hash.

O consumo é `UPDATE ... WHERE code_hash=$1 AND user_id=$2 AND used_at IS NULL RETURNING id` — o UPDATE condicional é o que torna “uso único” atômico sob concorrência, em vez de um read-then-write com corrida.

### 3.4 Tokens de API e estado OAuth

```sql
CREATE TABLE api_token (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    name TEXT NOT NULL, token_hash BYTEA NOT NULL,
    scope TEXT NOT NULL DEFAULT 'content',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ, expires_at TIMESTAMPTZ, revoked_at TIMESTAMPTZ,
    CONSTRAINT api_token_scope_check CHECK (scope IN ('content'))
);

CREATE TABLE oauth_state (
    id BIGSERIAL PRIMARY KEY,
    state_hash BYTEA NOT NULL, code_verifier TEXT NOT NULL,
    provider TEXT NOT NULL, purpose TEXT NOT NULL,
    user_id   BIGINT REFERENCES app_user(id) ON DELETE CASCADE,
    invite_id BIGINT REFERENCES invite(id)   ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(), expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    CONSTRAINT oauth_state_purpose_check CHECK (purpose IN ('login','link','accept_invite'))
);
CREATE UNIQUE INDEX oauth_state_hash_uniq ON oauth_state (state_hash);
```

O token de API é apresentado como `Authorization: Bearer fx_<id>_<secret>`. O prefixo com id faz o lookup ser um hit de PK em vez de scan, e torna tokens vazados **grepáveis** em logs e CI.

O `code_verifier` do PKCE mora no servidor, nunca num cookie. O cookie `fx_oauth` carrega só o `state`, e **os dois** precisam bater no callback.

### 3.5 Posse e integridade cross-tenant

```sql
ALTER TABLE tag               ADD COLUMN user_id BIGINT REFERENCES app_user(id) ON DELETE CASCADE;
ALTER TABLE folder            ADD COLUMN user_id BIGINT REFERENCES app_user(id) ON DELETE CASCADE;
ALTER TABLE link              ADD COLUMN user_id BIGINT REFERENCES app_user(id) ON DELETE CASCADE;
ALTER TABLE note              ADD COLUMN user_id BIGINT REFERENCES app_user(id) ON DELETE CASCADE;
ALTER TABLE push_subscription ADD COLUMN user_id BIGINT REFERENCES app_user(id) ON DELETE CASCADE;
-- ... UPDATE ... SET user_id = <admin de bootstrap> ... ; ALTER ... SET NOT NULL;

ALTER TABLE link DROP CONSTRAINT link_url_unique;
ALTER TABLE link ADD  CONSTRAINT link_user_url_unique UNIQUE (user_id, url);
ALTER TABLE tag  DROP CONSTRAINT tag_name_key;
ALTER TABLE tag  ADD  CONSTRAINT tag_user_name_unique UNIQUE (user_id, name);

-- Integridade cross-tenant pelo BANCO, não pelo handler.
ALTER TABLE folder ADD CONSTRAINT folder_user_id_unique UNIQUE (user_id, id);

ALTER TABLE folder DROP CONSTRAINT folder_parent_id_fkey;
ALTER TABLE folder ADD  CONSTRAINT folder_parent_same_user_fkey
    FOREIGN KEY (user_id, parent_id) REFERENCES folder (user_id, id)
    ON DELETE SET NULL (parent_id);          -- lista de colunas: PG15+, estamos no 18

ALTER TABLE link DROP CONSTRAINT link_folder_id_fkey;
ALTER TABLE link ADD  CONSTRAINT link_folder_same_user_fkey
    FOREIGN KEY (user_id, folder_id) REFERENCES folder (user_id, id)
    ON DELETE SET NULL (folder_id);
-- idem para note.
```

A lista de colunas no `ON DELETE SET NULL (folder_id)` é obrigatória: sem ela, apagar uma pasta tentaria anular também `user_id`, que é `NOT NULL`.

**O que fica de fora, e por quê:**

- **`link.slug` e `note.slug` continuam UNIQUE globais.** `/go/{slug}` e `/n/{slug}` resolvem **sem sessão**, portanto sem tenant. Se o slug fosse único por usuário, a rota pública não teria como escolher entre dois donos. Colisão entre usuários é resolvida pelo sufixo `-2`/`-3` que já existe.
- **`push_subscription.endpoint` continua UNIQUE global.** Um endpoint de Push é um canal físico de browser, não dado de usuário. Dois usuários no mesmo perfil de browser produzem o mesmo endpoint; o upsert repontua `user_id` para quem assinou por último, que é o comportamento correto (o dono anterior não está mais logado ali).
- **`link_tag` não ganha FK composta.** Ela perdeu a FK para `link(id)` na migration 000014, ao ser polimorfizada, e `tag_id` não carrega um `user_id` para compor. Anexar tag de outro tenant é barrado no repositório (`tags.SetEntityTags` valida posse) e travado por teste — é o **único** buraco que as FKs não fecham, e por isso está nomeado aqui.
- **`click_log` não ganha `user_id`.** Sem FK para `link`/`note`, a posse viraria segunda fonte de verdade, sujeita a divergir. As queries alcançam o dono por semi-join. Ver §12 para o risco de performance aceito.

### 3.6 Índices

Todo índice cuja coluna líder assumia “um usuário só” ganha `user_id` na frente: `link_user_created_idx`, `link_user_pinned_created_idx`, `link_user_title_lower_idx`, `link_user_change_recent_idx`, e os equivalentes de `note`. Os trigramas passam a `gin (user_id, title gin_trgm_ops)` via `btree_gin`, para que uma busca não varra os trigramas dos outros tenants.

Duas exceções deliberadas:

- **`link_check_due_idx` fica sem escopo.** O worker de change-check varre **todos** os tenants; prepender `user_id` deixaria `FindDueForCheck` sem índice.
- **`link_folder_preview_idx` fica como está.** `folder_id` já implica o dono via `folder_user_id_unique`; prepender `user_id` só alargaria a chave.

### 3.7 Bootstrap e reversibilidade

A migration insere incondicionalmente um `app_user` com `status='pending'` e `password_hash IS NULL`, e adota nele todas as linhas pré-existentes antes de aplicar `NOT NULL`. Em banco novo, é simplesmente a linha que a tela de setup reivindica. Em banco existente, o dono atual reencontra todos os seus dados na primeira entrada.

O `.down.sql` é **fail-loud**. Restaurar `UNIQUE(url)` e `UNIQUE(name)` globais é impossível se dois usuários salvaram a mesma URL, então ele começa com um bloco `DO $$ ... RAISE EXCEPTION` que conta as duplicatas e imprime o remédio manual. A alternativa — apagar silenciosamente as linhas de um tenant para o rollback caber — é inaceitável.

Reversibilidade honesta: **schema volta, dados de identidade não.** Todo `app_user`, `session`, `totp_secret`, `api_token` e afins é destruído; o conteúdo sobrevive mas perde a fronteira de posse.

---

## 4. API surface

### 4.1 Autenticação

| Método | Rota | Auth | Descrição |
|---|---|---|---|
| `GET` | `/api/auth/bootstrap-status` | — | `{needs_bootstrap}`. Único vazamento: se a instância já foi configurada. |
| `POST` | `/api/auth/bootstrap` | — | Reivindica o admin placeholder. Recusa se já houver conta `active`. |
| `GET` | `/api/auth/me` | opcional | **Sempre 200.** `{status: anonymous\|setup_required\|twofa_pending\|authenticated, ...}` |
| `POST` | `/api/auth/login` | — | Senha. Devolve sessão **ou** estado de 2FA pendente. |
| `POST` | `/api/auth/logout` | sessão | Sempre 204, mesmo para sessão já morta. |
| `POST` | `/api/auth/logout-all` | sessão + CSRF | Revoga todas as famílias, incrementa `token_version`. |
| `POST` | `/api/auth/refresh` | cookie `fx_rt` | Rotaciona. Ver §2.3. |
| `GET` | `/api/auth/sessions` | sessão | Lista as sessões vivas do próprio usuário. |
| `DELETE` | `/api/auth/sessions/{id}` | sessão + CSRF | Revoga uma. |

**`GET /api/auth/me` devolver 200 para anônimo é contrato, não detalhe.** Um 401 ali recursaria no interceptor de refresh do axios a cada boot frio do SPA.

```jsonc
// 200 — anônimo
{ "status": "anonymous", "features": { "google_oauth": true, "email_delivery": true } }

// 200 — autenticado
{ "status": "authenticated",
  "user": { "id": 1, "email": "a@b.c", "name": "Ana", "role": "admin",
            "totp_enabled": true, "email_verified": true, "google_only": false },
  "csrf_token": "…", "features": { … } }

// 200 — login OK, 2FA pendente
{ "status": "twofa_pending", "method": "totp", "masked_email": "a••@b.c",
  "email_otp_available": true, "expires_at": "2026-08-03T12:05:00Z" }
```

**Erros do login** — todos `401` com o mesmo corpo `{"error":{"code":"invalid_credentials","message":"invalid e-mail or password"}}`, para e-mail inexistente, senha errada e conta desabilitada. `429 too_many_attempts` com header `Retry-After` quando um bucket estoura.

### 4.2 Senha, convites e verificação

| Método | Rota | Auth | Notas |
|---|---|---|---|
| `POST` | `/api/auth/password/forgot` | — | **Sempre 202.** Envia só se a conta existir e estiver ativa. |
| `POST` | `/api/auth/password/reset` | token | Consome o token; revoga todas as sessões. |
| `POST` | `/api/auth/password/change` | sessão + CSRF | Exige senha atual. Revoga as demais sessões. |
| `POST` | `/api/auth/password/set` | sessão + CSRF + step-up | Conta Google-only readquire senha. Saída de lockout (§7.5). |
| `GET` | `/api/auth/invites/{token}` | — | `{email, role, expires_at}` ou **404**. |
| `POST` | `/api/auth/invites/accept` | token | Define nome + senha, ativa a conta, emite sessão. |
| `POST` | `/api/auth/email/verify` | token | Marca `email_verified_at`. |
| `POST` | `/api/auth/email/resend` | sessão ou `fx_pa` | Cooldown de 60 s. |

### 4.3 Segundo fator

| Método | Rota | Auth |
|---|---|---|
| `POST` | `/api/auth/2fa/totp/start` | sessão + CSRF + senha, **ou** `fx_pa` com `purpose='enroll_2fa'` |
| `GET` | `/api/auth/2fa/totp/qr.png` | idem — `Cache-Control: no-store` |
| `POST` | `/api/auth/2fa/totp/confirm` | idem — devolve os 10 códigos de recuperação **uma vez** |
| `POST` | `/api/auth/2fa/totp/disable` | sessão + CSRF + senha + código. **403 para admin.** |
| `POST` | `/api/auth/2fa/verify` | `fx_pa` — aceita TOTP, código de recuperação **ou** OTP de e-mail |
| `POST` | `/api/auth/2fa/email` | `fx_pa` — dispara OTP. Máx. 3 envios, intervalo de 60 s |
| `POST` | `/api/auth/2fa/recovery-codes/regenerate` | sessão + CSRF + senha + código |

Um endpoint de verificação, três fontes de código. O frontend tem uma tela de 6 dígitos só.

### 4.4 OAuth Google

| Método | Rota | Auth | `purpose` |
|---|---|---|---|
| `GET` | `/api/auth/oauth/google/start` | — / sessão | `login` · `link` (exige sessão) · `accept_invite` |
| `GET` | `/api/auth/oauth/google/callback` | — | — |
| `POST` | `/api/auth/oauth/google/convert` | `fx_pa` + CSRF | Confirma a senha e converte (§7.4) |
| `DELETE` | `/api/auth/oauth/google` | sessão + CSRF + senha | Desvincula |

### 4.5 Administração

Tudo sob `RequireRole(admin)`. Tokens de API são **rejeitados** aqui.

| Método | Rota |
|---|---|
| `GET` / `POST` | `/api/admin/users` (listar / criar) |
| `GET` / `PATCH` / `DELETE` | `/api/admin/users/{id}` |
| `POST` | `/api/admin/users/invite` |
| `POST` | `/api/admin/users/{id}/force-password-reset` |
| `POST` | `/api/admin/users/{id}/sessions/revoke` |

Guardas travadas no servidor (o frontend só espelha): não é possível rebaixar, desabilitar ou apagar **a si mesmo**, nem **o último admin ativo**.

---

## 5. Sessão, CSRF e cookies

### 5.1 Matriz de cookies

| Nome | httpOnly | Secure | SameSite | Path | Max-Age | Conteúdo |
|---|---|---|---|---|---|---|
| `fx_at` | sim | sim | Lax | `/` | 900 s | 32 bytes aleatórios |
| `fx_rt` | sim | sim | **Strict** | `/api/auth` | 30 d | 32 bytes aleatórios |
| `fx_csrf` | **não** | sim | Lax | `/` | 30 d | 32 bytes aleatórios |
| `fx_pa` | sim | sim | Strict | `/api/auth` | 300 s | 32 bytes aleatórios |
| `fx_oauth` | sim | sim | **Lax** | `/api/auth/oauth` | 600 s | o `state` |

`SameSite=Lax` no `fx_at` e não `Strict`: web (`:9088`) e API (`:9089`) são o **mesmo site** (site = esquema + eTLD+1; porta é irrelevante), então Lax funciona tanto no dev de portas separadas quanto no prod same-origin atrás do nginx, e ainda sobrevive a uma navegação vinda de um link de e-mail.

`fx_oauth` **precisa** ser Lax: o redirect do Google é um GET top-level cross-site, e Strict o descartaria — o callback quebraria 100% das vezes.

`fx_rt` combina Strict com `Path=/api/auth` porque só é enviado para `/refresh` e `/logout`. Não se perde nada tornando-o o mais restrito possível.

Toda resposta de `/api/auth/*` sai com `Cache-Control: no-store`; `/api/*` ganha `Vary: Cookie`.

### 5.2 CSRF — double-submit **assinado**

O header `X-Foldex-CSRF` é comparado, em tempo constante, contra `session.csrf_token_hash` — **a linha da sessão**, não o cookie.

Isso importa: double-submit ingênuo (header == cookie) é derrotado por cookie injection de um subdomínio irmão, porque o atacante escolhe os dois lados. Amarrando à linha da sessão, ele precisaria forjar um valor cujo `sha256` bata com o que o servidor guardou — o que é o problema original.

Regras: só verbos inseguros; só quando `Principal.Via == "session"` (bearer não tem credencial ambiente para carona); isentos os endpoints que rodam **antes** de existir sessão (`login`, `refresh`, `bootstrap`, `invites/accept`, `password/forgot`, `password/reset`).

### 5.3 Resolução do principal

Ordem, fail-closed:

1. `Authorization: Bearer fx_<id>_<secret>` → lookup por PK, `hmac.Equal` no `sha256`, checa `revoked_at`/`expires_at` e `status='active'` do dono. `Via="api_token"`, **pula CSRF**, é rejeitado em `/api/auth/*`, `/api/admin/*`, `/api/tokens` e `/api/backup/*`.
2. `fx_at` → `ResolveAccess`, com join em `app_user` para rejeitar conta não-ativa na **mesma** query. `last_seen_at` é atualizado no máximo uma vez por 60 s (um UPDATE por request no hot path é a amplificação de escrita clássica de tabela de sessão).
3. Nada → `401 unauthorized`.

**Revogação é instantânea** para os dois tipos, porque ambos são resolvidos no banco a cada request. É a troca deliberada de um lookup indexado por request em favor de revogação exata — na escala do foldex, de graça.

---

## 6. Segundo fator

### 6.1 TOTP

`github.com/pquerna/otp v1.5.0` (release 2025-05-16, último commit 2025-08-07 — ativo). Parâmetros fixos em SHA1 / 6 dígitos / 30 s: Google Authenticator, Authy, 1Password e Bitwarden **ignoram silenciosamente** `algorithm` e `digits` não-padrão na URI `otpauth://`, produzindo códigos que nunca validam. O schema admite outros valores para o futuro; o código de enrollment fixa os defaults.

Validação com `Skew: 1` (±30 s de deriva de relógio), seguida da guarda de replay: o contador do passo consumido precisa ser **maior** que `last_used_counter`. Sem isso, um código visto por cima do ombro é reutilizável dentro da sua própria janela.

**O QR é gerado no servidor** — `otp.Key.Image()` → PNG em `GET /api/auth/2fa/totp/qr.png`. Custo: `boombuler/barcode` entra como dep indireta. Ganho: zero dependência nova no frontend, e o seed base32 nunca precisa chegar a uma lib JS de QR.

### 6.2 OTP por e-mail

Seis dígitos gerados por `crypto/rand` com **rejection sampling** — nunca `%`. Módulo sobre um byte enviesa os dígitos 0–5, e num código de 6 posições isso é entropia real perdida.

TTL de 5 min, máximo 3 attempts no próprio registro, máximo 3 envios por challenge com intervalo mínimo de 60 s. Responde `202` mesmo quando recusado por rate limit, para não virar sonda.

### 6.3 O contador de tentativas mora no banco

`auth_challenge.attempts` é incrementado por `UPDATE ... RETURNING attempts` — incremento atômico, então N palpites paralelos não correm mais rápido que o cap.

Ele fica no **banco**, e não no `attemptlimit` em memória, deliberadamente: `CLAUDE.md §4` aceita explicitamente estado em memória para o unlock de pasta (“restart clears the state — acceptable”), mas um restart do backend **não pode** zerar o orçamento de tentativas de um segundo fator. É a diferença entre um limitador de conveniência e um controle de segurança.

---

## 7. OAuth Google e portabilidade de conta

### 7.1 PKCE e state

`code_verifier` de 64 bytes → base64url; `code_challenge` = base64url(sha256(verifier)); método **`S256` apenas**, nunca `plain`. O verifier fica em `oauth_state` no servidor; o cookie `fx_oauth` carrega só o `state`. No callback, os dois precisam bater — é isso que impede login-CSRF (um atacante enxertar *a conta Google dele* no seu browser).

Uso único é garantido por `UPDATE ... SET consumed_at=now() WHERE state_hash=$1 AND consumed_at IS NULL AND expires_at > now() RETURNING ...` — UPDATE condicional, não read-then-write.

### 7.2 O `id_token` não é parseado

Chama-se `https://openidconnect.googleapis.com/v1/userinfo`. O foldex não tem lib JWT como dependência direta (`golang-jwt` está no `go.sum` só como indireta do `minio/madmin-go`), e adotar uma significa assumir fetch e rotação de JWKS, `alg` confusion, e validação de `aud`/`iss`/`exp` — uma classe inteira de CVEs — para economizar uma chamada HTTPS num fluxo que acontece uma vez por mês.

### 7.3 `sub` é a chave de login

`user_identity.subject` é a **única** chave usada para encontrar uma conta. E-mail nunca resolve login: trocar o e-mail no Google não move vínculo nenhum.

Se não existe identidade para aquele `sub` **e** não existe conta com aquele e-mail → `403 oauth_not_linked`. Sem auto-provisionamento.

### 7.4 Conversão senha → Google

Quando o `sub` é desconhecido mas o e-mail bate com uma conta existente, o fluxo **não loga e não recusa**: entra em conversão.

```
callback (purpose=login), sub desconhecido, e-mail bate
   ├─ google.email_verified != true          → 403 oauth_email_unverified
   ├─ app_user.status != 'active'            → 403 oauth_not_linked
   │                                            ↑ MESMA resposta do caso inexistente:
   │                                              não confirma que a conta existe
   ├─ sub já vinculado a OUTRA conta         → 409 oauth_already_linked
   └─ ok → auth_challenge('convert_google', 10 min) + fx_pa
           200 {"status":"convert_password_account","email":"a••@b.c"}
              │
              └─ POST /api/auth/oauth/google/convert {"password":"…"}
                   ├─ errada  → 401 invalid_credentials (conta no cap de 5)
                   └─ correta → uma tx:
                        INSERT user_identity(user_id,'google',sub)
                        UPDATE app_user SET password_hash = NULL      ← vira Google-only
                        RevokeAllForUser(uid,'password_changed')
                        consome o challenge
                      → segue para o 2FA normal; NUNCA emite sessão direto
```

**A senha atual é a prova, não o e-mail.** Isso é deliberadamente mais estrito do que o argumento “o e-mail já é a raiz da identidade” permitiria — afinal o fluxo de reset de senha já entrega a conta a quem controla a caixa postal. A escolha foi tornar a conversão uma migração deliberada, não um caminho de recuperação. Consequência aceita: **“esqueci a senha, entro com Google” não funciona.** Quem esqueceu usa o reset e converte depois.

**OAuth nunca pula 2FA.** O retorno do Google — login normal ou conversão — desemboca no mesmo `auth_challenge`. Sem isso, com TOTP obrigatório para admin, o OAuth seria um furo direto na regra.

### 7.5 Vínculo, desvínculo e saídas de lockout

Vincular a uma conta já logada (`purpose=link`) exige sessão viva + CSRF, e a conta vinculada é **a da sessão** — nunca uma derivada do e-mail do Google. Os e-mails não precisam coincidir: é legítimo vincular um Gmail pessoal a uma conta de trabalho, *porque a sessão já provou a posse*. É exatamente por isso que vincular sem sessão nunca pode acontecer.

Aceitar convite via Google exige `email_verified == true` **e** e-mail idêntico ao do convite: o convite é emitido para um endereço específico, e permitir aceitá-lo com outra conta Google deixaria um link vazado ser reivindicado em silêncio.

Desvincular exige senha e é recusado se deixasse a conta **sem credencial alguma**.

Uma conta Google-only cujo acesso ao Google se perde tem três saídas, todas construídas de propósito:

1. `POST /api/admin/users/{id}/force-password-reset` — o admin devolve uma senha à conta.
2. Logado via Google, **Settings → “Definir senha”** readquire a credencial (step-up com código TOTP quando houver). Só depois disso desvincular passa a ser permitido.
3. `/api/auth/password/forgot` numa conta Google-only responde o mesmo `202` (anti-enumeração) mas envia **“esta conta entra pelo Google”** em vez de um link de reset. Deixar o link seria ressuscitar, só com a caixa postal, exatamente a credencial que a exigência de senha na conversão descartou.

Resta um caso sem saída pela UI: **o último admin, Google-only, que perde o Google** — sai por edição direta no banco. É o mesmo status que a master password esquecida já tem hoje (ADR-29), e precisa estar no README, não só aqui.

---

## 8. Segmentação multi-tenant

### 8.1 A escolha: parâmetro explícito, não RLS

`context` transporta o principal; **todo método de repositório recebe `uid authctx.UserID` explícito**. `authctx.UserID` é um tipo distinto, não um `int64` puro, então `Get(ctx, linkID, userID)` com argumentos trocados é erro de compilação, não vazamento.

**Por que não RLS com `SET LOCAL app.user_id`** — três motivos concretos:

1. Quase todo método hoje é `r.pool.Query(...)` direto numa conexão pooled (`links/repository.go:163,266`, `tags/repository.go:43`, `folders/repository.go:155`). RLS exigiria converter ~60 métodos para `BEGIN` / `SET LOCAL` / query / `COMMIT` — **mais** churn mecânico que adicionar um parâmetro, com uma transação por leitura de brinde.
2. RLS **falha em vazio**: esquecer o `SET LOCAL` devolve zero linhas, indistinguível de “usuário sem dados”. Isso vai para produção. O parâmetro explícito **falha na compilação**, e `go build ./...` enumera todos os call sites, incluindo os 88 arquivos de teste.
3. Existem leitores cross-tenant legítimos (`preview.Worker.requeuePending:350`, `links.FindDueForCheck`). Sob RLS precisariam de `BYPASSRLS`, segundo role, segundo pool e segunda DSN — uma classe nova de erro de configuração. Com parâmetro explícito, são só métodos que não recebem `uid`.

**Por que não um wrapper `repo.For(uid)`** — é a mesma coisa com passos a mais. Os repositórios são construídos uma vez no boot (`router.go:103-116`) e entregues a handlers de vida longa, então `For()` teria de ser chamado dentro de cada handler de qualquer jeito, a partir de um `uid` que ele já tirou do contexto. E um `For()` esquecido **não** é erro de compilação, se o método base continuar exportado.

**O custo, honestamente:** ~60 assinaturas e 33 arquivos de teste de integração mudam. É o preço, e é o preço certo, porque o modo de falha passa a ser o build quebrado em vez do vazamento silencioso.

### 8.2 Convenção: `repository_system.go`

Toda query legitimamente sem escopo mora num arquivo `repository_system.go` por pacote, com métodos prefixados `System*`. Um revisor que vir `FROM link` sem predicado de `user_id` fora desses arquivos sabe que é bug.

Reforço barato no CI:

```bash
! grep -rn 'FROM link \|FROM note \|FROM folder\b' backend/internal --include='*.go' \
  | grep -v repository_system.go | grep -v _test.go | grep -v user_id
```

Habitantes: `ClickAndResolve*` e `ViewAndResolve` (rotas públicas), `FindDueForCheck` e `SystemGet` (change-check), `requeuePending` e `UpdatePreview` (preview worker).

### 8.3 O padrão mecânico

Para cada método: `uid authctx.UserID` logo após `ctx`; `user_id = $N` como **primeiro** predicado do WHERE (para o índice composto liderar); `user_id` em toda lista de colunas de INSERT; `AND user_id = $N` em todo UPDATE/DELETE.

**Linha alheia responde 404, nunca 403.** Um 403 confirmaria que o id existe, transformando a rota em oráculo de enumeração cross-tenant. Como os repositórios já devolvem `httperr.ErrNotFound` quando o `RETURNING` não traz linha, isso sai de graça — mas é intencional e está travado por teste.

Exemplo, `entries.appendScopeFilters` — o caso mais delicado, porque os dois braços do `UNION ALL` compartilham um único slice `args` e os índices dos placeholders vêm de `len(*args)`:

```go
// uid entra PRIMEIRO para que o predicado de tenant lidere o WHERE de cada braço
// e os índices compostos da 000017 possam ser usados. Vai pelo mesmo slice `args`
// compartilhado — cada braço ganha seu PRÓPRIO $n, porque são chamadas separadas.
func appendScopeFilters(where *[]string, args *[]any, uid authctx.UserID,
                        alias, kind string, q ListQuery, linkSearch bool) {
	*args = append(*args, int64(uid))
	*where = append(*where, fmt.Sprintf("%s.user_id = $%d", alias, len(*args)))
	// … demais filtros inalterados
}
```

A conversão explícita `int64(uid)` na fronteira do pgx é proposital — não dependemos do fallback de reflexão do pgx v5 para tipos inteiros nomeados.

O sub-select de tags (`link_tag WHERE entity_kind=… AND tag_id = ANY($n)`) **não** ganha filtro: `link_tag` não tem `user_id`, e o `user_id` do braço externo já restringe o resultado. Quem precisa validar posse é o caminho de **escrita**, `tags.SetEntityTags`.

### 8.4 Workers e rotas públicas

O preview worker e o change-check worker são cross-tenant por natureza. `FindDueForCheck` passa a devolver `[]DueLink{ID, UserID}` para que o push resultante vá **só** para o dono do link — `push.Notification` ganha `UserID`, propagado pelo `pushSenderAdapter` que já existe em `main.go`.

`/go/{id-or-slug}` e `/n/{id-or-slug}` continuam públicos e tenant-blind, mantendo o predicado `folders.SQLNotInLockedFolder` que já impede vazamento de pasta trancada.

**Mas o ramo numérico vira problema (ADR-32).** `/go/42` permite a um visitante anônimo caminhar por ids e descobrir a URL de destino de todos os usuários. Com um usuário isso é aceitável; com vários, não. Decisão: PR1 mantém por compatibilidade; PR4 põe atrás de `PUBLIC_ID_REDIRECT_ENABLED`, default `false`, de modo que só o slug resolve. Slugs são a superfície de compartilhamento documentada (`CLAUDE.md §4`: “The slug IS exposed in LinkDialog”).

---

## 9. Rate limiting e anti-enumeração

### 9.1 Buckets

`internal/folders/ratelimit.go` é promovido a `internal/pkg/attemptlimit`, mudando só a chave (`int64` → `string`) e transformando as constantes em campos de `New(max, lockout)`. A API de reserva de slot (`Begin`/`Release`/`CommitFail`/`CommitSuccess`) e o `now` injetável são preservados — é aquele desenho sob um mutex só que impede N palpites paralelos de passarem do cap enquanto o bcrypt roda (RACE-HER-004).

| Superfície | Chave | Orçamento |
|---|---|---|
| `login` | `login:ip:<RealIP>` | 20 falhas / 15 min |
| `login` | `login:em:<sha256(email)>` | 5 falhas / lockout de 15 min |
| `2fa/verify` | `auth_challenge.attempts` (**banco**) | 5 no total |
| `2fa/email` | `auth_challenge.sends` (**banco**) | 3 + intervalo de 60 s |
| `password/forgot` | `pwreset:ip` / `pwreset:em` | 10/h e 3/h |
| `password/reset`, `invites/accept` | `:ip` | 20/h |
| bearer inválido | `apitoken:ip` | 30/min |
| `bootstrap` | `bootstrap:ip` | 5/h |
| `oauth/callback` | `oauthcb:ip` | 30 / 15 min |

**`middleware.RealIP` confia em `X-Forwarded-For` incondicionalmente.** Atrás do nginx está certo; em bind direto é spoofável, o que torna os buckets por IP decorativos. Entra `TRUSTED_PROXY_IPS` (CSV): `X-Forwarded-For` só é honrado vindo desses peers, senão vale `RemoteAddr`. É por isso que o desenho tem **duas** chaves — a de e-mail e as do banco seguram mesmo se a de IP falhar.

### 9.2 Login indistinguível

Quatro mecanismos, todos necessários:

1. **bcrypt sempre roda.** Quando `ByEmail` não acha, compara contra um hash dummy calculado uma vez no `init()`. Pular bcrypt para e-mail desconhecido é o oráculo clássico de ~80 ms.
2. **Resposta idêntica** nos três casos, inclusive conta desabilitada — um código `account_disabled` distinto confirmaria que o endereço existe.
3. **O bucket por e-mail incrementa também para e-mails inexistentes.** Não incrementar é, por si só, um oráculo: o atacante aprende quais endereços podem ser bloqueados.
4. **Piso de duração de 250 ms**, não jitter:

```go
const loginFloor = 250 * time.Millisecond
start := time.Now()
defer func() {
    if d := loginFloor - time.Since(start); d > 0 { time.Sleep(d) }
}()
```

Um piso elimina o sinal enquanto exceder o pior caso de trabalho real. Jitter só adiciona variância, que o atacante remove com média sobre amostras repetidas.

`/password/forgot` sempre `202`. `GET /invites/{token}` é lookup `sha256` em tempo constante, `404` no miss, sem nenhuma query por e-mail.

`internal/pkg/logsafe` ganha redação para `password`, `code`, `recovery_code`, `token`, `refresh_token`, `access_token`, `code_verifier`, `state`, `secret_base32` e `sub`, e passa a logar `user_id` em vez de e-mail em nível info.

---

## 10. Backup

### 10.1 Nenhuma tabela de auth entra no ZIP

Não vão `app_user`, `session`, `totp_secret`, `recovery_code`, `api_token`, `invite`, `user_identity`, `password_reset`. Três motivos:

1. O ZIP é um arquivo que o usuário baixa, manda por e-mail e joga no Drive. Colocar hashes bcrypt, seeds TOTP e **refresh tokens vivos** ali converte uma conveniência numa primitiva de roubo de credencial com audiência ilimitada. Hoje um backup roubado custa seus bookmarks; não pode passar a custar todas as contas da instância.
2. Sessões e tokens de API estão amarrados a cookies em browsers específicos. “Restaurar” isso é semanticamente vazio.
3. `folder.password_hash` já circula verbatim (ADR-29). É discutível, mas é um segredo por-pasta de baixo valor e removê-lo é breaking change de formato — fora de escopo, registrado em §12.

### 10.2 O restore sempre escreve para quem chamou

`user_id` **nunca** vem do ZIP. É isso que torna impossível forjar um backup que planta linhas na conta de outro tenant. `Snapshot.OwnerEmail` é informativo: se diferir, o restore prossegue e adiciona um warning.

### 10.3 Mudanças concretas

| Alvo | Mudança |
|---|---|
| `model.go` | `CurrentSchemaVersion` 11 → **12**; `DatabaseSnapshotVersion` 5 → **6**; `Snapshot.OwnerEmail` |
| `readSnapshot` | recebe `uid`; `tag`/`folder`/`link`/`note` ganham `WHERE user_id=$1`; `link_tag`×2 e `click_log`×2 ganham semi-join; a query de `app_setting` **sai** |
| `countConflicts` | recebe `uid`; conflitos de `url` e `name` passam a ser por usuário |
| `db_wipe.go` | `wipeAll` (TRUNCATE global) → `wipeUser(uid)` com `DELETE ... WHERE user_id`, **sem** `RESTART IDENTITY` (as sequências são compartilhadas) |
| `db_restore_identity.go` | **removido** — ver abaixo |
| `db_restore_skip.go` | `ON CONFLICT (user_id, name)` e `(user_id, url)` |
| `db_slugs.go` | `uniqueLinkSlug`/`uniqueNoteSlug` ficam **globais**; `nextAvailableTagName` ganha `user_id` |
| `Snapshot.AppSettings` | campo mantido para decodificar ZIPs pré-v6, mas **ignorado** no restore, com warning |

**O modo wipe deixa de preservar ids.** `restoreIdentity` existia para manter os ids originais e depois dar `setval` nas sequências; nenhuma das duas coisas é válida agora (outro tenant pode ter aqueles ids, e mexer numa sequência global a partir do restore de um usuário é errado). Depois do `wipeUser`, o caminho mapeado do modo skip roda com zero conflitos e produz o resultado certo com ids novos. `CLAUDE.md §4` e `docs/SDD-BACKUP-RESTORE.md` precisam ser reescritos por causa disso.

### 10.4 Object store — as quatro superfícies

**As chaves são planas** — `screenshots/{link_id}.ext`, `images/{link_id}.ext`, `notes/{uuid}.ext`. Não há segmento de tenant, então **a posse de um arquivo é estabelecida pela LINHA que o referencia**, nunca pela chave. Re-particionar por usuário exigiria reescrever `og_image_url` em todas as linhas e mover objetos vivos — desproporcional. A alternativa adotada escopa as quatro superfícies onde a chave é lida ou escrita. Cada uma falhava de forma independente, e **a suíte single-tenant passava em todas elas**:

| Superfície | Falha | Correção |
|---|---|---|
| `Export` | empacotava o bucket inteiro (`ListObjects` por prefixo) — o ZIP que o usuário baixa e repassa levava screenshots dos outros | intersecta com `userObjectKeys`; objeto que nenhuma linha referencia não pertence a ninguém e fica para trás |
| `GET /api/files/*` | id denso e enumerável ⇒ qualquer autenticado varria o range e lia imagem alheia | `repo.Get(uid, id)` para chaves derivadas de id; chave alheia devolve o **404 byte a byte idêntico** ao de chave inexistente (§4 404-não-403 vale para chave, não só para linha) |
| `POST /api/links/{id}/image` | `Upload` + `purgeLegacyVariants` rodavam **antes** do check; o `UpdateOGImage` escopado devolvia 404 depois de o objeto da vítima já ter sido sobrescrito e as variantes irmãs apagadas | posse verificada no topo, antes de ler um byte |
| `restore` (`applyFiles`) | chave declarada pelo ZIP era honrada quando o remap não casava ⇒ ZIP forjado sobrescrevia objeto de quem detém aquele id | só grava chave que saiu de `mapping.remapFileKey`; entrada órfã é descartada |

**`notes/` é deliberadamente exceção**: a página pública `/n/{slug}` renderiza `body_html` **sem sessão**, e o browser busca essas imagens sem principal nenhum. Gatear quebraria toda nota publicada. A proteção é o UUIDv4 de 122 bits, que só aparece dentro da nota dona — é uma *capability URL*, e está registrado como tal, não como esquecimento.

**Consequência do "nenhum modo preserva id"**: uma linha restaurada com o `og_image_url` do snapshot aponta para um id que agora é de outra pessoa, ou de ninguém. `realignLinkImageURLs` re-aponta cada `og_image_url` para o id da própria linha (o id dentro de uma chave derivada de id é sempre o da linha dona — a reescrita é posicional, não precisa consultar o mapping). Sem isso o wipe termina "com sucesso" e **toda imagem quebra**.

`db_slugs.go` é a assimetria a comentar no código: slug global, nome de tag por usuário. É exatamente o ponto que um leitor futuro “corrige” errado.

---

## 11. Decisões de design

### 11.1 Por que `sha256` para tokens e `bcrypt` para senhas

São problemas diferentes. Senha é escolhida por humano, tem entropia baixa e cabe em dicionário — o custo do bcrypt é a defesa. Token de sessão é 256 bits de `crypto/rand`: não há dicionário, e o hash existe só para que um dump do banco não seja utilizável. Usar bcrypt em token adicionaria ~80 ms **por request** sem ganho de segurança. Trade-off: nenhum, é a escolha certa nas duas pontas.

### 11.2 Por que a janela de graça de 10 s no refresh

Sem ela, rotação com detecção de reuso é hostil: duas abas, um StrictMode, um reload rápido — qualquer duplo `/refresh` com o mesmo token vira “reuso” e mata a sessão. Com ela, um hit recente na tabela de consumidos com a família viva é tratado como corrida legítima e reemite os tokens correntes. Trade-off: uma janela de 10 s em que um token roubado **e** replayado muito rápido não é detectado. Aceito — o atacante precisaria vencer o browser legítimo por menos de 10 s, e fora disso a família inteira morre.

### 11.3 Por que o pré-auth é um cookie e não um token no corpo

O estado entre “senha OK” e “2FA OK” precisa sobreviver a um reload e não pode ser legível por JS (é um bearer parcial). Cookie httpOnly com `Path=/api/auth` dá as duas coisas e reusa o mesmo maquinário de limpeza dos outros. Trade-off: mais um cookie na matriz. Aceito.

### 11.4 Por que 404 e não 403 para linha alheia

403 confirma que o id existe. Num espaço de ids denso e sequencial (BIGSERIAL), isso é enumeração de todo o conteúdo dos outros usuários, sem ler nada. Trade-off: um usuário que realmente perdeu acesso a algo seu vê “não encontrado” em vez de “sem permissão”. Aceito — só acontece com dado que nunca foi dele.

### 11.5 Por que `AUTH_ENABLED` começa em `0`

O PR1 é ~70% do diff (a segmentação inteira) e ~5% do risco. Separá-lo de qualquer mudança de comportamento significa que ele pode ser revisado, mergeado e rodado em produção **sem** ligar autenticação — e a suíte cross-user já prova o isolamento antes de existir uma tela de login. Trade-off: existe uma janela de releases em que o código de auth está no binário mas desligado. Aceito, e é o motivo de o shim injetar explicitamente o admin de bootstrap em vez de deixar `uid` zerado.

---

## 12. Trade-offs e limitações

| Limitação | Mitigação |
|---|---|
| ~60 assinaturas e 33 arquivos de teste mudam no PR1 | É o preço de ter o modo de falha na compilação, não no vazamento. Concentrado num PR sem mudança de comportamento. |
| `stats.Daily` ganha semi-join em `click_log`, que perdeu a FK para `link` na 000014 — o planner não tem estatística cruzada | `click_log_entity_ts` deve segurar. Se regredir sob volume real, a `000018` desnormaliza `user_id`. **Não** agora: criaria segunda fonte de verdade de posse, sujeita a divergir. |
| `folder.password_hash` continua indo verbatim no backup | Segredo por-pasta de baixo valor; remover é breaking change de formato. Registrado no ADR-30. |
| Conta Google-only pode trancar o usuário para fora | Três saídas (§7.5). O último admin Google-only sem Google sai por edição direta no banco — mesmo status da master password esquecida. Precisa estar no README. |
| “Esqueci a senha, entro com Google” não funciona | Consequência explícita de exigir a senha atual na conversão. Caminho: reset de senha, depois converter. |
| Buckets por IP são spoofáveis em bind direto sem `TRUSTED_PROXY_IPS` | Buckets por e-mail e os contadores em banco não dependem de IP. |
| `btree_gin` não foi executado contra `postgres:18.4-alpine` nesta análise | Vem do `postgresql-contrib`, que a imagem oficial inclui. Verificar no primeiro run de integração; fallback é deixar os trigramas sem escopo e deixar o planner fazer bitmap-AND com o btree de `user_id`. |
| `AUTH_ENCRYPTION_KEY` não tem rotação | Trocar exige reenroll de TOTP de todos. Mesmo status de `FOLDER_UNLOCK_KEY`. |

---

## 13. Segurança

- **Threat model muda.** A §0 do `CLAUDE.md` (“single-user, local network, no public exposure”) deixa de valer: com auth, expor na LAN passa a ser um caso suportado. `validateSecureDefaults` acompanha — passa a aceitar bind não-loopback quando `AUTH_ENABLED=1`, e a **recusar** o boot com `CORS_ORIGINS=*` combinado com `AllowCredentials: true` (incompatíveis por spec), e `AUTH_COOKIE_SECURE=0` com bind não-loopback.
- **`SHARED_SECRET` coexiste e é rebaixado.** No PR4 vira header de perímetro, documentado como “não autentica ninguém e não identifica ninguém”; quando os dois estão configurados, a request precisa do header **e** da sessão. Removido no release seguinte.
- **Tokens de API têm escopo.** `scope='content'` é aceito em `/api/{links,notes,folders,tags,entries,push,stats}` e rejeitado com `403 token_scope` em `/api/auth/*`, `/api/admin/*`, `/api/tokens`, `/api/backup/*` e `/api/settings/*`. Um token de extensão roubado não pode cunhar sessão, desligar 2FA nem exfiltrar um backup completo.
- **CORS.** `AllowCredentials: true`, `AllowedHeaders` ganha `Authorization` e `X-Foldex-CSRF`. Aproveita-se para corrigir um **bug pré-existente**: `router.go:90` não lista `PUT`, embora `settings/handler.go:20` monte `r.Put("/master-password", …)` — invisível hoje porque prod é same-origin via nginx.
- **Nada de secretos em log.** Ver §9.2.
- Sanitização de `note.body_html` (§4 do `CLAUDE.md`) permanece intocada e continua sendo o que torna seguro renderizar HTML de usuário — agora com o agravante de que o HTML de um usuário nunca é renderizado no contexto de outro.

---

## 14. Testing strategy

### Backend

**`internal/testdb`.** `Reset` hoje trunca 6 tabelas e **já esquece `app_setting`** — passa a listar as 20 e ganha, em `drift_test.go`, um teste que compara a lista com `information_schema.tables` (menos `schema_migrations`), para que a próxima tabela adicionada não seja esquecida de novo. Novo helper `SeedUser(t, pool, email, role) authctx.UserID`, usado por todo teste de conteúdo — a linha placeholder da migration é truncada pelo `Reset`.

**`internal/security/crossuser_integration_test.go`** — a suíte que importa. Pacote próprio, fora de todo domínio, exercitando os handlers reais pelo router. Segue o truque de `TestCrossContamination_LinkAndNoteRowsDoNotLeak` (`internal/notes`): cria A e B **intercalados**, para que os ids fiquem adjacentes, e **afirma a colisão de id explicitamente** — senão o teste passa por motivo trivial. ~20 sub-testes:

- listagem só devolve linhas próprias (table-driven sobre links, notes, entries, folders, tags, stats, export)
- GET de linha alheia é **404, não 403**; UPDATE e DELETE idem, e a linha sobrevive
- mesma URL e mesmo nome de tag permitidos entre usuários
- slug continua global (B criando o slug de A recebe `-2`)
- não dá para anexar tag alheia (o único buraco fora das FKs)
- não dá para mover link para pasta alheia nem apontar `parent_id` para pasta alheia (prova que as FKs compostas disparam)
- busca por trigrama não casa conteúdo alheio
- stats não contam cliques do outro
- export não contém tabela de auth nenhuma
- **ZIP forjado**: exportar como A, restaurar como B, afirmar que tudo caiu em B e que A ficou intacto
- token de unlock de pasta de A é rejeitado na pasta de B; o lockout é por (usuário, pasta)
- push do change-check vai só para o dono do link

**`internal/auth`** — ~70 sub-testes de integração distribuídos em `handler_bootstrap`, `handler_login`, `handler_session`, `csrf`, `handler_invite`, `handler_2fa`, `handler_admin`, `handler_tokens`. Os que travam propriedades e não só caminhos felizes:

- login byte-idêntico nos três casos de falha, e as duas ramificações dentro de 30 ms de média sobre 20 amostras, ambas acima do piso de 250 ms
- reuso de refresh fora da graça revoga a família; dentro da graça reemite o head
- teto absoluto de 90 d respeitado mesmo rotacionando
- desabilitar usuário invalida access token vivo na request seguinte
- CSRF com header de **outra** sessão é 403 (a propriedade que double-submit ingênuo falha)
- código TOTP não pode ser replayado dentro da própria janela
- uso concorrente do mesmo código de recuperação sucede exatamente uma vez
- o cap de tentativas do challenge **sobrevive a restart** (reconstrói o handler contra o mesmo pool)
- `fx_pa` não alcança endpoint de dados nenhum
- admin sem TOTP recebe challenge de enrollment, não sessão

**OAuth + conversão** — ~16 sub-testes com um Google falso via `httptest.Server`. Os de takeover foram **redesenhados** para travar as condições da conversão, já que a recusa incondicional deixou de existir:

- `TestOAuth_EmailMatchAloneNeverIssuesASession` ← e-mail coincidente com `email_verified=true` para em `convert_password_account`, sem sessão e sem cookie de sessão
- `TestOAuth_ConvertRequiresTheCurrentPassword` e `..._WrongPasswordCountsAgainstTheChallengeCap`
- `TestOAuth_ConvertRetiresPasswordAndMakesAccountGoogleOnly`
- `TestOAuth_ConvertStillRequiresTOTPBeforeIssuingASession` ← o teste de bypass de 2FA
- `TestOAuth_GoogleLoginOnALinkedAccountDoesNotBypassTOTP`
- `TestOAuth_ConvertRefusedWhenGoogleEmailUnverified` / `..._WhenSubAlreadyBoundToAnotherUser`
- `TestOAuth_DisabledAccountReturnsTheSameErrorAsNonexistent`
- `TestOAuth_StateIsSingleUse` / `..._MismatchIs400` / `..._ExpiredIs400`
- `TestOAuth_UnlinkRefusedWhenItWouldLeaveNoCredential`
- `TestForgotPassword_GoogleOnlyAccountSendsNoResetLinkButStillReturns202`
- `TestSettings_SetPasswordOnGoogleOnlyAccountThenUnlinkIsAllowed`
- `TestInvariant_NoActiveUserEndsUpWithoutAnyCredential`

Unitários novos: `attemptlimit` (portado de `folders/ratelimit_test.go`), `secrets` (incluindo uniformidade de `NewNumericCode` sobre 100k amostras — a guarda de viés de módulo), `cookies`, `csrf`, `totp` (vetores da RFC 6238 com relógio fixo), `config` (as novas ramificações de `validateSecureDefaults`).

### Frontend

- `AuthProvider.test.tsx` — estados do bootstrap; logout esvazia o cache e limpa só as chaves de `localStorage` do tenant; `useAuth` fora do provider lança.
- `AuthGate.test.tsx` — splash sem piscar o formulário; erro de rede vira splash offline, **não** tela de login; `?reset=` e `?invite=` abrem as telas certas.
- `client.test.ts` (reescrito — o atual testa o `window.prompt` removido) — CSRF só em verbos inseguros; quatro 401 simultâneos coalescem em **um** refresh; `X-Foldex-Folder-Unlock` sobrevive ao retry.
- `OtpInput.test.tsx` — o de maior valor: auto-avanço, backspace que limpa e volta, colar `123-456`, colar parcial, auto-submit exatamente uma vez, `aria-label` por célula, `autoComplete="one-time-code"` **só** na primeira célula (nas seis, o Safari preenche todas com o mesmo dígito).
- `AdminUsersPage.test.tsx` — cada guarda com caso positivo e negativo; delete confirma duas vezes e reporta contagens.
- `locales.test.ts` — paridade de chaves en/pt/es e profundidade máxima 2. Novo, e necessário: a superfície de i18n cresce ~24%.

`renderWithProviders` passa a aceitar `auth?: SessionState | null` com **admin autenticado por default**, de modo que os ~60 arquivos de teste existentes seguem passando sem edição.

### Coverage gate (`CLAUDE.md` §2)

Backend ≥ 85% (`-covermode=atomic -coverpkg` sobre `./internal/...`); frontend ≥ 85% linhas / 80% branches. `internal/auth` é pesado em handler, então a cobertura vem dos testes de integração para handlers e de unitários para `cookies.go`, `csrf.go`, `totp.go` e `ratelimit.go`. `src/api/client.ts` já está no `exclude` do vitest, então a reescrita não custa cobertura.

---

## 15. Faseamento

| PR | Escopo | Flag / fronteira |
|---|---|---|
| **1 — Segmentação** ✅ | Migration 000017; `pkg/{authctx,keyfile,attemptlimit,secrets}`; ~60 métodos com `uid`; convenção `repository_system.go`; rework completo do backup; `testdb.Reset` + `SeedUser`; suíte cross-user inteira | `AUTH_ENABLED=0`: um shim injeta o admin de bootstrap. **Zero mudança visível.** Fecha quando o build está limpo, a cobertura ≥ 85% e a suíte cross-user passa com dois usuários reais no lugar do shim |
| **2 — Identidade** ✅ | `internal/mailer` + Mailpit; `internal/auth` core (sessão, CSRF, RBAC); bootstrap; convites; login/logout/refresh; `/api/auth/me`; `/api/admin/users`; sweeper. CORS credencial + `PUT` + headers novos. Master password migra para `app_user`. Frontend: `AuthProvider`/`AuthGate`, `client.ts`, telas login + setup, `auth.css`, `useDarkMode` | `AUTH_ENABLED` ainda `0` (opt-in do operador). Fecha quando dá para configurar o primeiro admin, convidar um segundo e cada um só ver o seu |
| **3 — 2FA** ✅ | `pquerna/otp`; TOTP + QR server-side + AES-GCM no seed; códigos de recuperação; `auth_challenge`; OTP por e-mail; obrigatório para admin; todos os buckets; piso de timing; redações do `logsafe`. Frontend: `OtpInput` e as telas otp/forgot/sent/reset/verify/invite | `AUTH_REQUIRE_2FA_FOR_ADMINS=1`. Fecha quando admin não consegue sessão sem TOTP e todo caminho de recuperação tem teste |
| **4 — Federação + default-on** | `internal/oauthgoogle` + 6 endpoints (inclusive `/convert`); fluxo de conversão + tela `convert` + “Definir senha”; `api_token` + bearer + escopos; extensão MV3 para `Authorization: Bearer` (transição com as duas credenciais); `PUBLIC_ID_REDIRECT_ENABLED=0`; `SHARED_SECRET` deprecado; tela de admin de usuários; docs | **`AUTH_ENABLED` passa a `1`.** Fecha quando a extensão funciona só com token e o aviso de depreciação dispara no boot |

Cada PR passa pelo gate pré-push do `CLAUDE.md` §6.1 exatamente como o CI roda, pelo sweep obrigatório dos 5 agentes (§9), por `graphify update .` e por um bump de versão.

### 15.1 Desvios do plano, registrados na entrega

Três coisas saíram diferentes do desenho acima e valem registro, porque um leitor futuro tropeçaria nelas:

1. **A janela de graça emite uma sessão IRMÃ, não novos tokens na mesma linha** (§2.3 descrevia "reemite os tokens correntes da família", que é irrealizável: o servidor guarda só hashes). Ver ADR-30 em `ARCHITECTURE.md` para o raciocínio completo — resumidamente, regravar a linha invalida o trio que a requisição vencedora está segurando, e como as duas vêm do mesmo cookie jar a aba perdedora é deslogada. A irmã herda `family_id` **e** `created_at`.

2. **`internal/pkg/keyfile` não foi extraído no PR2, e foi no PR3.** O PR2 não precisou de nenhuma chave em arquivo — o segredo de sessão são os próprios tokens aleatórios, não uma chave derivada. O PR3 precisou (`AUTH_ENCRYPTION_KEY`), e aí o pacote nasceu com dois consumidores reais, como `CLAUDE.md` §7 pede. Ele ganhou um parâmetro que o plano não previa: **`AllowEphemeral`** — ver §15.2.

3. **A master password já havia migrado no PR1**, não no PR2 como a tabela de faseamento sugere: a 000017 criou `app_user.master_password_{hash,hint}`, moveu os valores e esvaziou as chaves correspondentes de `app_setting`, e `internal/settings` lê e escreve nas colunas do usuário desde então. Nada a fazer aqui — o item só está registrado porque a linha do PR2 na tabela acima o menciona.

4. **`POST /api/auth/password/change` entrou no PR2**, embora a §4.2 o liste junto do resto do grupo de senha. Ele não precisa de e-mail nem de token — só da senha atual — então segurá-lo até o PR3 deixaria uma conta sem nenhuma forma de trocar a própria senha. `forgot`/`reset` seguem no PR3, que é onde a entrega por e-mail passa a ser obrigatória.

### 15.2 Desvios do PR3, registrados na entrega

Cinco, e os dois primeiros são correções de desenho:

1. **A migration do PR3 é a `000019`, e ela NÃO cria tabela nenhuma.** A §3.3 descrevia `auth_challenge`, `email_otp`, `totp_secret`, `recovery_code` e `password_reset` como parte da 000017 — e elas realmente entraram lá, então o PR3 começou sem migration. A `000019_two_factor_indexes` nasceu depois, do sweep: dois índices que as queries novas precisavam (`email_otp(challenge_id, created_at DESC)` para o cooldown de reenvio, `auth_challenge(user_id, purpose, consumed_at)` para o supersede no login) e a coluna `auth_challenge.mailbox_already_proven` que fecha o takeover do item 6. A numeração é 19 e não 18 porque o PR1 já havia entregue uma `000018_click_log_user_id` — versão duplicada faz o `golang-migrate` recusar o boot, e o applier do `testdb` (que só ordena nomes de arquivo) esconderia isso até produção.

2. **`keyfile.Config.AllowEphemeral` separa duas classes de chave que o plano tratava como uma.** `folders.LoadOrGenerateFolderUnlockKey` sempre aceitou seguir com uma chave só-de-sessão quando não conseguia persistir, e isso está certo lá: perder a chave invalida tokens de unlock e o usuário simplesmente redigita a senha da pasta. Para `AUTH_ENCRYPTION_KEY` o mesmo comportamento é destrutivo — a chave cifra dado em repouso, e um boot que gera uma nova torna todo seed TOTP indecifrável, trancando cada usuário para fora da própria conta. Com `AllowEphemeral: false` o backend **recusa subir**, o que é o resultado correto: falhar na hora é reparável, subir e descobrir no próximo restart não é.

3. **O discriminador entre código numérico e código de recuperação é o comprimento SEM SEPARADORES, não a contagem de dígitos.** A primeira implementação filtrava a entrada para dígitos e perguntava "tem seis?". Um código de recuperação tem 10 símbolos de um alfabeto de 32, dos quais 10 são dígitos — então **cerca de 1 em cada 23 códigos contém exatamente seis dígitos** e era roteado para o caminho TOTP, onde nunca casa. O portador simplesmente não conseguia usar aquele código, sem nada na resposta explicando. Locked por `TestRecoveryCode_WithSixDigitsIsNotMistakenForATOTPCode`, que **constrói** o caso em vez de torcer para o acaso — foi assim que ele chegou à suíte como flake de 1-em-20 em vez de teste vermelho.

4. **`totp_enabled` é derivado com `EXISTS`, não uma coluna.** Uma coluna precisaria ser atualizada em quatro lugares (enroll, confirm, disable, reset administrativo) e discordaria da realidade na primeira vez que um deles fosse esquecido — e a direção do erro decide se o login exige um código que o usuário não consegue produzir.

5. **Uma caixa postal comprometida chegava a emitir sessão — e a correção virou schema.** O reset de senha diverge corretamente para o desafio, porque provou só um fator. Mas o fallback de OTP por e-mail mandava o código de seis dígitos para **o mesmo endereço** que recebeu o link de reset: os dois passos fechavam num canal só e o segundo fator não comprava nada. `auth_challenge.mailbox_already_proven` marca os desafios nascidos de um reset, e para esses o fator e-mail é recusado — só autenticador ou código de recuperação, credenciais que a caixa postal não contém. Achado pelo sweep de segurança, não por revisão minha. Travado por `TestReset_MailboxAloneCannotSatisfyBothFactors`.

6. **O fator e-mail exige `MAIL_DRIVER=smtp`.** O driver `log` imprime o corpo da mensagem no stdout, o que é uma troca deliberada e documentada para links de CONVITE — numa instância sem SMTP o log É a caixa postal — mas um segundo fator escrito no log do container é legível por qualquer um no grupo `docker` ou por qualquer coletor de logs, e aí deixa de ser um fator.

7. **`Optional` passou a verificar CSRF.** Ele resolve o principal sem exigir um, e o PR3 montou dois POSTs nele (`/2fa/totp/start` e `/confirm`). Sem a verificação, "autenticação opcional" viraria uma forma de montar verbo inseguro fora da proteção de CSRF por completo: o browser anexa o cookie de sessão num POST cross-site de qualquer jeito. Métodos seguros seguem intocados, que é por que `/me` não muda.

8. **Os caminhos de step-up autenticados por sessão ganharam teto de tentativas.** `Verify2FA` é limitado por `auth_challenge.attempts`, mas `/2fa/totp/disable` e `/2fa/recovery-codes/regenerate` não têm desafio — nada limitava o palpite de TOTP ali. `attemptlimit` por id de usuário, 5 em 15 min.

9. **A tela de segundo fator fica `busy` para sempre depois de um sucesso.** O `AuthGate` a desmonta assim que a sessão é adotada, mas até esse render chegar o formulário continuaria aceitando submit sobre um código de uso único já gasto — o segundo request falharia e pintaria erro por cima de um login que deu certo. Locked por `TwoFactorScreen.test.tsx`.

---

## 16. Open questions (resolvidas em revisão)

- ~~Auto-vincular conta Google por e-mail coincidente?~~ → **Não automaticamente.** E-mail coincidente abre a **conversão**, que exige a senha atual e aposenta a senha (§7.4).
- ~~OAuth pode contar como segundo fator?~~ → **Não.** O retorno do Google cai no mesmo `auth_challenge` (§7.4).
- ~~Admin pode ver conteúdo de outro usuário?~~ → **Não.** Só administra contas; nenhuma rota devolve conteúdo alheio (§8).
- ~~Slug por usuário ou global?~~ → **Global**, porque `/go/` e `/n/` resolvem sem sessão (§3.5).
- ~~Segredos entram no backup?~~ → **Não**, e o restore ignora o dono declarado no ZIP (§10).
- ~~RLS ou parâmetro explícito?~~ → **Parâmetro explícito**, para falhar na compilação (§8.1).
- ~~`click_log` ganha `user_id`?~~ → **Não em v1**; semi-join, com a `000018` como plano B se regredir (§12).
