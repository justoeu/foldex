-- 000017_multi_user_auth.up.sql
--
-- Turns Foldex from single-user into multi-tenant. See docs/SDD-AUTH-RBAC.md
-- and ADR-30/31/32 in docs/ARCHITECTURE.md.
--
-- Five content tables gain `user_id NOT NULL`; the two global uniques that
-- assumed one user (link.url, tag.name) become per-user composites. link.slug
-- and note.slug stay GLOBALLY unique because /go/{slug} and /n/{slug} resolve
-- with no session — and therefore with no tenant to disambiguate by.
--
-- A `pending` bootstrap admin is created unconditionally and adopts every
-- pre-existing row. On a fresh DB it is simply the row the setup screen claims.
--
-- Cross-tenant FK integrity is enforced by the DATABASE, not by handlers:
-- folder gains UNIQUE (user_id, id) so link/note/folder-parent reference
-- (user_id, folder_id) instead of (folder_id). A row can never point at another
-- tenant's folder even if a repository forgets its scope filter.

-- ─────────────────────────────────────────────────────────────────────
-- 1. Identity core
-- ─────────────────────────────────────────────────────────────────────

CREATE TABLE app_user (
    id                   BIGSERIAL PRIMARY KEY,
    email                TEXT NOT NULL,
    -- lower(btrim(email)) — the uniqueness + lookup key. A stored column, not
    -- an expression index, because three paths must agree on ONE normalization:
    -- the login lookup, the invite match and the OAuth linking rule. A drift
    -- between them is a security failure, not a search bug.
    email_normalized     TEXT NOT NULL,
    email_verified_at    TIMESTAMPTZ,
    name                 TEXT NOT NULL DEFAULT '',
    -- NULL = account not yet claimed (bootstrap/invite pending) OR Google-only
    -- after an ADR-31 conversion.
    password_hash        TEXT,
    role                 TEXT NOT NULL DEFAULT 'user',
    status               TEXT NOT NULL DEFAULT 'pending',
    -- ADR-29's master folder-recovery password moves OFF the global app_setting
    -- KV and onto the user: a global master would let any admin reset another
    -- tenant's folder passwords, which is exactly the bypass ADR-28 refused.
    master_password_hash TEXT,
    master_password_hint TEXT,
    -- Bumped by "log out everywhere" / password change / role change.
    token_version        INTEGER NOT NULL DEFAULT 0,
    last_login_at        TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT app_user_role_check     CHECK (role   IN ('admin', 'user')),
    CONSTRAINT app_user_status_check   CHECK (status IN ('pending', 'active', 'disabled')),
    CONSTRAINT app_user_email_norm_chk CHECK (email_normalized = lower(btrim(email))),
    CONSTRAINT app_user_email_len_chk  CHECK (length(email) BETWEEN 3 AND 320)
);
CREATE UNIQUE INDEX app_user_email_norm_uniq ON app_user (email_normalized);
CREATE INDEX app_user_role_status_idx ON app_user (role, status);

-- Federated identities. subject = the provider's immutable `sub`, never the
-- e-mail: a Google e-mail change must not move the binding.
CREATE TABLE user_identity (
    id            BIGSERIAL PRIMARY KEY,
    user_id       BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    provider      TEXT   NOT NULL,
    subject       TEXT   NOT NULL,
    email_at_link TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ,
    CONSTRAINT user_identity_provider_check CHECK (provider IN ('google'))
);
-- One provider account maps to at most one Foldex user (anti-takeover)…
CREATE UNIQUE INDEX user_identity_provider_subject_uniq ON user_identity (provider, subject);
-- …and one Foldex user links at most one account per provider.
CREATE UNIQUE INDEX user_identity_user_provider_uniq ON user_identity (user_id, provider);

-- ─────────────────────────────────────────────────────────────────────
-- 2. Sessions (refresh families + reuse detection)
-- ─────────────────────────────────────────────────────────────────────

-- One row per active login (device/browser). family_id groups every refresh
-- token descended from one password login; a replayed refresh token revokes the
-- whole family, not just one token.
--
-- Tokens are stored as sha256 in BYTEA, never raw: a DB dump must not be a
-- session-hijack kit. sha256 rather than bcrypt is correct here — the tokens are
-- 256-bit random (nothing to brute-force) and resolution is on the hot path of
-- every request.
CREATE TABLE session (
    id                 BIGSERIAL PRIMARY KEY,
    user_id            BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    family_id          UUID   NOT NULL,
    access_token_hash  BYTEA NOT NULL,
    access_expires_at  TIMESTAMPTZ NOT NULL,
    refresh_token_hash BYTEA NOT NULL,
    refresh_expires_at TIMESTAMPTZ NOT NULL,
    -- Signed double-submit: the X-Foldex-CSRF header is compared against THIS,
    -- not merely against the cookie, so cookie injection from a sibling
    -- subdomain cannot forge a matching pair.
    csrf_token_hash    BYTEA NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at         TIMESTAMPTZ,
    last_seen_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at         TIMESTAMPTZ,
    revoked_reason     TEXT,
    ip                 INET,
    user_agent         TEXT,
    CONSTRAINT session_revoked_reason_check CHECK (
        revoked_reason IS NULL OR revoked_reason IN
            ('logout', 'logout_all', 'reuse_detected', 'password_changed',
             'admin_revoked', 'user_disabled', 'expired')
    )
);
CREATE UNIQUE INDEX session_access_hash_uniq  ON session (access_token_hash);
CREATE UNIQUE INDEX session_refresh_hash_uniq ON session (refresh_token_hash);
CREATE INDEX session_user_idx        ON session (user_id, revoked_at);
CREATE INDEX session_family_idx      ON session (family_id);
-- Sweeper: DELETE FROM session WHERE refresh_expires_at < now() - interval '7 days'
CREATE INDEX session_refresh_exp_idx ON session (refresh_expires_at);

-- Consumed refresh tokens. A hit here on /refresh means either a legit racing
-- tab inside the grace window, or a stolen token being replayed.
CREATE TABLE session_used_token (
    token_hash BYTEA PRIMARY KEY,
    family_id  UUID   NOT NULL,
    session_id BIGINT NOT NULL REFERENCES session(id) ON DELETE CASCADE,
    used_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX session_used_token_family_idx  ON session_used_token (family_id);
CREATE INDEX session_used_token_used_at_idx ON session_used_token (used_at);

-- ─────────────────────────────────────────────────────────────────────
-- 3. Invites, password reset, 2FA challenges, e-mail OTP
-- ─────────────────────────────────────────────────────────────────────

CREATE TABLE invite (
    id               BIGSERIAL PRIMARY KEY,
    email            TEXT NOT NULL,
    email_normalized TEXT NOT NULL,
    role             TEXT NOT NULL DEFAULT 'user',
    token_hash       BYTEA NOT NULL,
    invited_by       BIGINT REFERENCES app_user(id) ON DELETE SET NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ NOT NULL,
    accepted_at      TIMESTAMPTZ,
    accepted_user_id BIGINT REFERENCES app_user(id) ON DELETE SET NULL,
    revoked_at       TIMESTAMPTZ,
    CONSTRAINT invite_role_check      CHECK (role IN ('admin', 'user')),
    CONSTRAINT invite_email_norm_chk  CHECK (email_normalized = lower(btrim(email)))
);
CREATE UNIQUE INDEX invite_token_hash_uniq ON invite (token_hash);
-- At most one live invite per e-mail (re-inviting revokes/replaces).
CREATE UNIQUE INDEX invite_open_email_uniq ON invite (email_normalized)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE INDEX invite_expires_idx ON invite (expires_at);

CREATE TABLE password_reset (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    token_hash   BYTEA NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    consumed_at  TIMESTAMPTZ,
    requested_ip INET
);
CREATE UNIQUE INDEX password_reset_token_hash_uniq ON password_reset (token_hash);
CREATE INDEX password_reset_user_idx    ON password_reset (user_id, consumed_at);
CREATE INDEX password_reset_expires_idx ON password_reset (expires_at);

-- The PRE-AUTH state between "password OK" and "2FA OK". Nothing here grants
-- data access; it only authorizes /api/auth/2fa/* (and, for 'convert_google',
-- the OAuth conversion endpoint).
--
-- `attempts` lives in the DB, not in the in-memory limiter, deliberately:
-- ADR-28 accepts that a restart clears the folder-unlock state, but a restart
-- must NOT reset a second-factor attempt budget.
CREATE TABLE auth_challenge (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    token_hash  BYTEA NOT NULL,
    -- 'totp'           → user has confirmed TOTP, must present a code
    -- 'enroll_2fa'     → admin without TOTP; only /2fa/totp/{start,confirm}
    -- 'convert_google' → ADR-31 password→Google conversion, awaiting the password
    purpose     TEXT NOT NULL,
    attempts    SMALLINT NOT NULL DEFAULT 0,
    sends       SMALLINT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    ip          INET,
    user_agent  TEXT,
    CONSTRAINT auth_challenge_purpose_check
        CHECK (purpose IN ('totp', 'enroll_2fa', 'convert_google'))
);
CREATE UNIQUE INDEX auth_challenge_token_hash_uniq ON auth_challenge (token_hash);
CREATE INDEX auth_challenge_expires_idx ON auth_challenge (expires_at);

-- 6-digit e-mail OTP. Initially stored as SHA-256; migration 000023 invalidates
-- those rows and runtime now stores a keyed, user/challenge-bound HMAC digest.
CREATE TABLE email_otp (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    challenge_id BIGINT REFERENCES auth_challenge(id) ON DELETE CASCADE,
    purpose      TEXT NOT NULL,
    code_hash    BYTEA NOT NULL,
    attempts     SMALLINT NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    consumed_at  TIMESTAMPTZ,
    CONSTRAINT email_otp_purpose_check CHECK (purpose IN ('login_2fa', 'verify_email'))
);
CREATE INDEX email_otp_lookup_idx  ON email_otp (user_id, purpose, consumed_at, expires_at);
CREATE INDEX email_otp_expires_idx ON email_otp (expires_at);

-- ─────────────────────────────────────────────────────────────────────
-- 4. TOTP + recovery codes
-- ─────────────────────────────────────────────────────────────────────

-- The shared secret is stored ENCRYPTED (AES-256-GCM, key from
-- AUTH_ENCRYPTION_KEY / /data/auth_encryption.key, same env→file→autogen shape
-- as FOLDER_UNLOCK_KEY). A plaintext base32 seed in a pg_dump is a permanent
-- 2FA bypass; a hash is impossible because TOTP verification needs the seed.
CREATE TABLE totp_secret (
    user_id           BIGINT PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
    secret_ciphertext BYTEA NOT NULL,
    secret_nonce      BYTEA NOT NULL,
    algorithm         TEXT NOT NULL DEFAULT 'SHA1',
    digits            SMALLINT NOT NULL DEFAULT 6,
    period_seconds    SMALLINT NOT NULL DEFAULT 30,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    confirmed_at      TIMESTAMPTZ,
    -- Replay guard: the last successfully consumed time-step counter. A code is
    -- rejected if its counter <= last_used_counter, so a shoulder-surfed code
    -- cannot be replayed inside its own 30s window.
    last_used_counter BIGINT,
    CONSTRAINT totp_digits_check CHECK (digits IN (6, 8)),
    CONSTRAINT totp_period_check CHECK (period_seconds BETWEEN 15 AND 120),
    CONSTRAINT totp_alg_check    CHECK (algorithm IN ('SHA1', 'SHA256', 'SHA512'))
);

-- Single-use recovery codes. Initially 50-bit values under plain SHA-256;
-- migration 000023 invalidates those rows. Runtime now issues 80-bit values and
-- stores a purpose-separated, user-bound HMAC for indexed verification.
CREATE TABLE recovery_code (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    code_hash  BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    used_at    TIMESTAMPTZ
);
CREATE UNIQUE INDEX recovery_code_hash_uniq ON recovery_code (code_hash);
CREATE INDEX recovery_code_user_idx ON recovery_code (user_id, used_at);

-- ─────────────────────────────────────────────────────────────────────
-- 5. API tokens (MV3 extension, CLI) + OAuth transient state
-- ─────────────────────────────────────────────────────────────────────

-- Long-lived bearer credential, presented as `Authorization: Bearer fx_<id>_<secret>`.
-- The id prefix makes the lookup a PK hit instead of a table scan, and makes
-- leaked tokens greppable in logs/CI.
CREATE TABLE api_token (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   BYTEA NOT NULL,
    -- Reserved for extensibility; only 'content' is issued today.
    scope        TEXT NOT NULL DEFAULT 'content',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    CONSTRAINT api_token_scope_check CHECK (scope IN ('content'))
);
CREATE UNIQUE INDEX api_token_hash_uniq ON api_token (token_hash);
CREATE INDEX api_token_user_idx ON api_token (user_id, revoked_at);

-- PKCE verifier + CSRF state for the OAuth redirect. Server-side (not a cookie)
-- so the verifier never leaves the server; the fx_oauth cookie carries only the
-- state, and BOTH must match on callback.
CREATE TABLE oauth_state (
    id            BIGSERIAL PRIMARY KEY,
    state_hash    BYTEA NOT NULL,
    code_verifier TEXT  NOT NULL,
    provider      TEXT  NOT NULL,
    -- 'login' | 'link' (requires a live session) | 'accept_invite'
    purpose       TEXT  NOT NULL,
    user_id       BIGINT REFERENCES app_user(id) ON DELETE CASCADE,
    invite_id     BIGINT REFERENCES invite(id)   ON DELETE CASCADE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL,
    consumed_at   TIMESTAMPTZ,
    CONSTRAINT oauth_state_provider_check CHECK (provider IN ('google')),
    CONSTRAINT oauth_state_purpose_check  CHECK (purpose IN ('login', 'link', 'accept_invite'))
);
CREATE UNIQUE INDEX oauth_state_hash_uniq ON oauth_state (state_hash);
CREATE INDEX oauth_state_expires_idx ON oauth_state (expires_at);

-- ─────────────────────────────────────────────────────────────────────
-- 6. Bootstrap admin placeholder
-- ─────────────────────────────────────────────────────────────────────
--
-- Always inserted, on fresh and existing DBs alike:
--   fresh    → the setup screen claims this row (sets real e-mail + password,
--              flips status to 'active').
--   existing → same, and it also inherits every pre-migration row below.
-- status='pending' + password_hash IS NULL is what /api/auth/bootstrap keys on;
-- the endpoint refuses once ANY row has status='active'.

INSERT INTO app_user (email, email_normalized, name, role, status, password_hash)
VALUES ('admin@foldex.local', 'admin@foldex.local', 'Administrator', 'admin', 'pending', NULL);

-- ─────────────────────────────────────────────────────────────────────
-- 7. Ownership columns + backfill
-- ─────────────────────────────────────────────────────────────────────

ALTER TABLE tag               ADD COLUMN user_id BIGINT REFERENCES app_user(id) ON DELETE CASCADE;
ALTER TABLE folder            ADD COLUMN user_id BIGINT REFERENCES app_user(id) ON DELETE CASCADE;
ALTER TABLE link              ADD COLUMN user_id BIGINT REFERENCES app_user(id) ON DELETE CASCADE;
ALTER TABLE note              ADD COLUMN user_id BIGINT REFERENCES app_user(id) ON DELETE CASCADE;
ALTER TABLE push_subscription ADD COLUMN user_id BIGINT REFERENCES app_user(id) ON DELETE CASCADE;

UPDATE tag               SET user_id = (SELECT id FROM app_user WHERE email_normalized = 'admin@foldex.local') WHERE user_id IS NULL;
UPDATE folder            SET user_id = (SELECT id FROM app_user WHERE email_normalized = 'admin@foldex.local') WHERE user_id IS NULL;
UPDATE link              SET user_id = (SELECT id FROM app_user WHERE email_normalized = 'admin@foldex.local') WHERE user_id IS NULL;
UPDATE note              SET user_id = (SELECT id FROM app_user WHERE email_normalized = 'admin@foldex.local') WHERE user_id IS NULL;
UPDATE push_subscription SET user_id = (SELECT id FROM app_user WHERE email_normalized = 'admin@foldex.local') WHERE user_id IS NULL;

ALTER TABLE tag               ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE folder            ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE link              ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE note              ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE push_subscription ALTER COLUMN user_id SET NOT NULL;

-- Adopt the ADR-29 master password onto the bootstrap admin, then retire the
-- global keys. app_setting survives as a table for genuine instance-level
-- config; it is left EMPTY by this migration.
UPDATE app_user u SET
    master_password_hash = (SELECT value FROM app_setting WHERE key = 'master_password_hash'),
    master_password_hint = (SELECT value FROM app_setting WHERE key = 'master_password_hint')
WHERE u.email_normalized = 'admin@foldex.local';
DELETE FROM app_setting WHERE key IN ('master_password_hash', 'master_password_hint');

-- ─────────────────────────────────────────────────────────────────────
-- 8. Unique-constraint swaps
-- ─────────────────────────────────────────────────────────────────────
--
-- link.url and tag.name become per-user. link.slug / note.slug DO NOT — the
-- public /go/{slug} and /n/{slug} routes resolve with no session and therefore
-- no tenant, so the slug namespace must stay global. Slug collision across
-- users is resolved by the existing -2/-3 suffix loop.
--
-- push_subscription.endpoint also stays globally unique on purpose: a Push
-- endpoint is a physical browser channel, not user data. Two users sharing one
-- browser profile produce the same endpoint; the upsert re-points user_id to
-- whoever subscribed last, which is correct (the old owner is no longer logged
-- in there).

ALTER TABLE link DROP CONSTRAINT link_url_unique;
ALTER TABLE link ADD  CONSTRAINT link_user_url_unique UNIQUE (user_id, url);

ALTER TABLE tag DROP CONSTRAINT tag_name_key;
ALTER TABLE tag ADD  CONSTRAINT tag_user_name_unique UNIQUE (user_id, name);

-- ─────────────────────────────────────────────────────────────────────
-- 9. Cross-tenant FK integrity (defense in depth)
-- ─────────────────────────────────────────────────────────────────────
--
-- Composite FKs make it structurally impossible for a link/note/subfolder to
-- reference another tenant's folder, even if a repository loses its scope
-- predicate. The `ON DELETE SET NULL (col)` column list (PG15+) is REQUIRED:
-- without it, deleting a folder would try to null user_id too, which is NOT NULL.

ALTER TABLE folder ADD CONSTRAINT folder_user_id_unique UNIQUE (user_id, id);

ALTER TABLE folder DROP CONSTRAINT folder_parent_id_fkey;
ALTER TABLE folder ADD  CONSTRAINT folder_parent_same_user_fkey
    FOREIGN KEY (user_id, parent_id) REFERENCES folder (user_id, id)
    ON DELETE SET NULL (parent_id);

ALTER TABLE link DROP CONSTRAINT link_folder_id_fkey;
ALTER TABLE link ADD  CONSTRAINT link_folder_same_user_fkey
    FOREIGN KEY (user_id, folder_id) REFERENCES folder (user_id, id)
    ON DELETE SET NULL (folder_id);

ALTER TABLE note DROP CONSTRAINT note_folder_id_fkey;
ALTER TABLE note ADD  CONSTRAINT note_folder_same_user_fkey
    FOREIGN KEY (user_id, folder_id) REFERENCES folder (user_id, id)
    ON DELETE SET NULL (folder_id);

-- link_tag deliberately gets NO composite FK: it lost its FK to link(id) in
-- migration 000014 when it was polymorphized, and tag_id's FK cannot carry a
-- user_id it does not have. Cross-tenant tag attachment is prevented in the
-- repository layer (tags.SetEntityTags validates tag ownership) and locked by
-- TestCrossUser_CannotAttachAnotherUsersTag.

-- ─────────────────────────────────────────────────────────────────────
-- 10. Index swaps
-- ─────────────────────────────────────────────────────────────────────
--
-- Every index whose leading column assumed "one user" gets user_id prepended.
-- btree_gin lets the trigram indexes lead with user_id too, so a search inside
-- a 5-user install does not scan the other four tenants' trigrams.

CREATE EXTENSION IF NOT EXISTS btree_gin;

-- link ---------------------------------------------------------------
DROP INDEX link_created;
CREATE INDEX link_user_created_idx ON link (user_id, created_at DESC);

DROP INDEX link_pinned_created;
CREATE INDEX link_user_pinned_created_idx ON link (user_id, pinned DESC, created_at DESC);

DROP INDEX link_title_lower_idx;
CREATE INDEX link_user_title_lower_idx ON link (user_id, lower(title));

DROP INDEX link_change_recent_idx;
CREATE INDEX link_user_change_recent_idx ON link (user_id, last_change_detected_at DESC)
    WHERE last_change_detected_at IS NOT NULL;

DROP INDEX link_title_trgm;
DROP INDEX link_url_trgm;
CREATE INDEX link_user_title_trgm ON link USING gin (user_id, title gin_trgm_ops);
CREATE INDEX link_user_url_trgm   ON link USING gin (user_id, url   gin_trgm_ops);

-- link_folder and link_folder_preview_idx are UNCHANGED: folder_id already
-- implies the owner through folder_user_id_unique, so prepending user_id would
-- only widen the key.
--
-- link_check_due_idx is UNCHANGED and MUST STAY UNSCOPED: the changecheck
-- worker scans across ALL tenants. Prepending user_id would leave
-- FindDueForCheck unindexed.

-- note ---------------------------------------------------------------
DROP INDEX note_created_idx;
CREATE INDEX note_user_created_idx ON note (user_id, created_at DESC);

DROP INDEX note_pinned_created_idx;
CREATE INDEX note_user_pinned_created_idx ON note (user_id, pinned DESC, created_at DESC);

DROP INDEX note_title_trgm;
DROP INDEX note_body_trgm;
CREATE INDEX note_user_title_trgm ON note USING gin (user_id, title     gin_trgm_ops);
CREATE INDEX note_user_body_trgm  ON note USING gin (user_id, body_text gin_trgm_ops);

CREATE INDEX note_user_title_lower_idx ON note (user_id, lower(title));

-- note_folder_preview_idx and note_slug_idx UNCHANGED (folder-implied / global).

-- folder -------------------------------------------------------------
-- folders.Repository.List does WHERE user_id=$1 AND parent_id IS NULL (root)
-- or WHERE user_id=$1 AND parent_id=$2, both ORDER BY name.
CREATE INDEX folder_user_parent_name_idx ON folder (user_id, parent_id, name);
CREATE INDEX folder_user_root_name_idx   ON folder (user_id, name) WHERE parent_id IS NULL;
-- folder_parent stays: still serves the DeleteCascade CTE and the FK check.

-- tag ----------------------------------------------------------------
-- tags.Repository.List is WHERE user_id=$1 ORDER BY name — served by the new
-- tag_user_name_unique composite; no extra index needed.

-- push_subscription --------------------------------------------------
CREATE INDEX push_subscription_user_idx ON push_subscription (user_id);

-- click_log ----------------------------------------------------------
-- DELIBERATELY gets no user_id. It has no FK to link/note (dropped in 000014)
-- so ownership would become a second source of truth that can drift. Stats
-- queries reach the owner through a semi-join on link/note, served by the
-- existing click_log_entity_ts (entity_kind, entity_id, clicked_at DESC).
-- If stats.Daily regresses under real data, migration 000018 denormalizes it.
