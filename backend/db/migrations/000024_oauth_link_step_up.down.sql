DROP INDEX IF EXISTS oauth_state_session_idx;

ALTER TABLE oauth_state
    DROP CONSTRAINT IF EXISTS oauth_state_link_proof_check,
    DROP COLUMN IF EXISTS proof_at,
    DROP COLUMN IF EXISTS token_version,
    DROP COLUMN IF EXISTS session_id;
