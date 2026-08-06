-- 000020_email_otp_code_hash_index.down.sql
--
-- Reverses 000020. Index only: no row is read, written or deleted. Dropping it
-- restores the sequential scan on the confirmation-link lookup.

DROP INDEX IF EXISTS email_otp_code_hash_idx;
