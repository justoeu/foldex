-- Permanent IP blocklist — ADR-46.
--
-- The audit screen surfaces a burst of failed logins from one address;
-- attemptlimit already parks that address for fifteen minutes, in memory, per
-- process. This table is the durable answer for the case where fifteen minutes
-- is not enough and a human decided so.
--
-- INET, not TEXT: Postgres validates the shape, and equality against an
-- attacker-supplied string would otherwise be a comparison between two
-- spellings of the same address ("::ffff:1.2.3.4" and "1.2.3.4"). Single
-- addresses only — the enforcement path resolves an exact match. A prefix
-- column would let one typo park a /8, and the operator's own network is
-- inside the plausible typo range.
--
-- created_by is ON DELETE SET NULL for audit_log's reason: deleting the account
-- that installed a block must not erase the record that it was installed. The
-- e-mail is denormalized alongside for the same reason — after the row is
-- gone, the id identifies nobody.
CREATE TABLE ip_block (
    id               BIGSERIAL PRIMARY KEY,
    ip               INET NOT NULL UNIQUE,
    reason           TEXT,
    created_by       BIGINT REFERENCES app_user(id) ON DELETE SET NULL,
    created_by_email TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ip_block_reason_len_chk CHECK (reason IS NULL OR length(reason) <= 256),
    CONSTRAINT ip_block_email_len_chk  CHECK (created_by_email IS NULL OR length(created_by_email) <= 320)
);

-- The screen lists newest-first.
CREATE INDEX ip_block_created_idx ON ip_block (created_at DESC, id DESC);
