-- 000019_two_factor_indexes.down.sql
--
-- Reverses 000019. Indexes only: no row is read, written or deleted, so this
-- is fully reversible and costs nothing but the query plans it restores to
-- sequential scans.

ALTER TABLE auth_challenge DROP COLUMN IF EXISTS mailbox_already_proven;

DROP INDEX IF EXISTS auth_challenge_user_purpose_idx;
DROP INDEX IF EXISTS email_otp_challenge_idx;
