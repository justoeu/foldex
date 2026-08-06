-- 000020_email_otp_code_hash_index.up.sql
--
-- The e-mail confirmation link resolves by token hash ALONE, with no user_id —
-- the caller has no session, which is the whole point of a link. That lookup
-- had no index leading with code_hash: email_otp_lookup_idx (mig 000017) leads
-- with user_id, which this query does not have.
--
-- So every click on a confirmation link sequential-scanned email_otp, on an
-- UNAUTHENTICATED route whose table anyone can grow by requesting codes. The
-- cost scales with the junk an attacker has inserted, which is the wrong thing
-- for it to scale with.
--
-- Partial on the unconsumed rows: a spent token is never looked up again, and
-- the sweeper deletes them anyway, so they have no business in the index.
CREATE INDEX email_otp_code_hash_idx ON email_otp (code_hash)
    WHERE consumed_at IS NULL;
