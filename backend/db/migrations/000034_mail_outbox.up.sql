-- Transactional outbox for auth e-mail — ADR-36.
--
-- Every credential-bearing message is written HERE, in the same transaction as
-- the credential it carries. That replaces the queue-admission protocol the
-- handlers used before (reserve a dispatcher slot, then persist the token, then
-- publish): admission bounded the in-memory queue but could not survive a
-- restart, so a deploy between the commit and the send lost the message while
-- the credential — and its 60-second cooldown — stayed. An INSERT that
-- participates in the caller's transaction cannot fail for capacity and cannot
-- be lost: either both rows exist or neither does.
--
-- The payload is AES-256-GCM (internal/pkg/secrets.Cipher, keyed by
-- AUTH_ENCRYPTION_KEY), not plaintext. A reset link IS a full account takeover,
-- and the whole reason password_reset stores only a sha256 is that a pg_dump
-- must not be a takeover kit; storing the rendered link beside it in clear text
-- would hand back exactly what that design refused. GCM rather than CTR for the
-- authentication tag: without it, write access to this table is a link
-- substitution attack, and the victim sees only a legitimate-looking recovery
-- e-mail pointing somewhere else.
--
-- What is stored is (template name, params) rather than a rendered body. The row
-- stays small, a template can be corrected without draining the queue, and the
-- locale column stays meaningful — the recipient's language decides the render,
-- which cannot be true once the body is frozen.
--
-- There is no user_id. The recipient is an ADDRESS, not an account: an invite
-- precedes the account it creates, and a forgot-password for an unknown address
-- has no user to reference. That absence also keeps the table outside the backup
-- surface by construction, with no new rule needed — the ZIP never carries auth
-- material.
CREATE TABLE mail_outbox (
    id                 BIGSERIAL PRIMARY KEY,
    template           TEXT NOT NULL,
    recipient          TEXT NOT NULL,
    payload_ciphertext BYTEA NOT NULL,
    payload_nonce      BYTEA NOT NULL,
    locale             TEXT NOT NULL DEFAULT 'en',
    status             TEXT NOT NULL DEFAULT 'pending'
                         CHECK (status IN ('pending', 'publishing', 'published', 'failed')),
    attempts           INT NOT NULL DEFAULT 0,
    -- claim_token is what makes publication idempotent under concurrency. A
    -- relay claims with FOR UPDATE SKIP LOCKED and may then stall for longer
    -- than the claim TTL, by which time another relay owns the row; the CAS on
    -- this exact token is what stops the straggler from overwriting the result.
    claim_token        UUID,
    claimed_at         TIMESTAMPTZ,
    -- Backoff is a column, not a sleep in the worker. A sleeping worker holds
    -- its slot and turns one slow recipient into a stalled queue for everyone.
    next_attempt_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A NORMALIZED reason (timeout / canceled / smtp_rejected / …), never the
    -- raw SMTP error: an MTA rejection routinely echoes the envelope back, and
    -- the envelope of these messages is a recipient address.
    last_error         TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at       TIMESTAMPTZ
);

-- The relay's claim query orders by next_attempt_at over pending rows only, so
-- the index is partial: a large published backlog must not widen the scan that
-- runs every poll interval.
CREATE INDEX mail_outbox_due_idx
    ON mail_outbox (next_attempt_at)
    WHERE status = 'pending';

-- Rows stranded in 'publishing' by a relay that died between claim and result.
-- The sweeper returns them to 'pending' once the claim TTL has passed.
CREATE INDEX mail_outbox_stuck_idx
    ON mail_outbox (claimed_at)
    WHERE status = 'publishing';

-- Retention sweep: 'published' is dropped after a week, 'failed' is kept for the
-- audit window because it is operational evidence.
CREATE INDEX mail_outbox_retention_idx
    ON mail_outbox (created_at)
    WHERE status IN ('published', 'failed');
