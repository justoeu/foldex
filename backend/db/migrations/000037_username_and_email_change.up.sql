-- 000037_username_and_email_change
--
-- Two things the account could not do: sign in with anything but an e-mail, and
-- change the e-mail at all.
--
-- USERNAME is OPTIONAL. Nullable rather than backfilled from the address,
-- because a generated `valmir.justo` derived from `valmir.justo@…` publishes
-- half the mailbox under a name its owner never chose. An account without one
-- signs in exactly as before.
--
-- The uniqueness that matters is on the NORMALIZED value, and the shape check
-- forbids `@` — that is load-bearing, not cosmetic. Login resolves ONE
-- identifier against both columns, so a username shaped like an address could
-- otherwise shadow somebody else's e-mail and quietly collect their password
-- attempts.
--
-- EMAIL CHANGE is a two-step: this table holds the request, and the address
-- only moves when a link sent to the NEW mailbox is followed. The alternative —
-- writing the address straight in — makes a typo the account's login AND its
-- recovery channel, with the warning going to the mistyped address. That is the
-- same property `AdminCreateUser` already protects by creating accounts
-- unverified (ADR-40).

-- ── username ─────────────────────────────────────────────────────────

ALTER TABLE app_user ADD COLUMN username            TEXT;
ALTER TABLE app_user ADD COLUMN username_normalized TEXT;

-- Partial, so the many accounts with no username do not collide on NULL.
CREATE UNIQUE INDEX app_user_username_norm_uniq
    ON app_user (username_normalized)
    WHERE username_normalized IS NOT NULL;

-- 3..32 characters, starting and ending alphanumeric, no `@` anywhere. The
-- database enforces it as well as the handler because this column decides who a
-- login attempt resolves to: a handler is one code path, and the next one added
-- would have to remember.
ALTER TABLE app_user ADD CONSTRAINT app_user_username_shape CHECK (
    username_normalized IS NULL
    OR username_normalized ~ '^[a-z0-9][a-z0-9._-]{1,30}[a-z0-9]$'
);

-- The pair moves together or the normalized column stops describing the stored
-- one, and the unique index starts guarding a value nobody logs in with.
ALTER TABLE app_user ADD CONSTRAINT app_user_username_pair CHECK (
    (username IS NULL) = (username_normalized IS NULL)
);

-- ── pending e-mail change ────────────────────────────────────────────

CREATE TABLE email_change (
    id                   BIGSERIAL PRIMARY KEY,
    user_id              BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,

    -- The address being moved TO. Stored here and nowhere else until the link
    -- is followed, so `app_user.email` keeps working the whole time.
    new_email            TEXT NOT NULL,
    new_email_normalized TEXT NOT NULL,

    -- sha256 of a 256-bit token, never the token. The row is the only record of
    -- the request, and a pg_dump of it must not be a mailbox-takeover kit.
    token_hash           BYTEA NOT NULL,

    -- The credential epoch the request was made in. A password change, a reset
    -- or a logout-all bumps `app_user.token_version`, and this row then fails
    -- closed — the same binding migration 000025 gave challenges and 000028
    -- gave resets. Without it, a pending change survives the very action taken
    -- to recover an account someone else got into.
    token_version        INTEGER NOT NULL,

    -- The session that proved the password, so a request cannot be finished
    -- under a session that was revoked in the meantime.
    session_id           BIGINT REFERENCES session(id) ON DELETE SET NULL,

    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at           TIMESTAMPTZ NOT NULL,
    consumed_at          TIMESTAMPTZ
);

-- Resolution is by hash ALONE, with no user_id, exactly like the e-mail
-- verification link: the token IS the identifier, and the endpoint is reachable
-- without a session because the link is followed from a mail client. Unique so
-- a collision is a constraint violation rather than an ambiguous row, and
-- indexed because an unauthenticated caller can grow this table.
CREATE UNIQUE INDEX email_change_token_hash_uniq ON email_change (token_hash);

-- One live request per account: asking again supersedes the previous one, so an
-- address typed wrong twice does not leave two live links to two mailboxes.
CREATE UNIQUE INDEX email_change_one_pending
    ON email_change (user_id)
    WHERE consumed_at IS NULL;

-- Non-partial, unlike the one above. `ON DELETE CASCADE` has to find EVERY row
-- for an account, consumed ones included, and those never leave the table — so
-- without this, deleting one account scans a table that grows with the whole
-- instance's history rather than with that account's.
CREATE INDEX email_change_user_idx ON email_change (user_id);

-- ── the reason a session died ────────────────────────────────────────

-- Consuming the confirmation changes the account's LOGIN IDENTIFIER, so every
-- session issued against the old one is revoked. It needs its own reason:
-- reusing 'password_changed' would put a sentence in the audit trail and in the
-- session list that is simply not true, and the trail outliving the account is
-- the whole point of ADR-34.
ALTER TABLE session DROP CONSTRAINT session_revoked_reason_check;
ALTER TABLE session ADD CONSTRAINT session_revoked_reason_check CHECK (
    revoked_reason IS NULL OR revoked_reason IN
        ('logout', 'logout_all', 'reuse_detected', 'password_changed',
         'admin_revoked', 'user_disabled', 'expired', 'email_changed')
);
