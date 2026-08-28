DROP INDEX IF EXISTS audit_log_actor_idx;
ALTER TABLE audit_log
    DROP CONSTRAINT IF EXISTS audit_log_subject_len_chk,
    DROP CONSTRAINT IF EXISTS audit_log_entity_kind_len_chk,
    DROP CONSTRAINT IF EXISTS audit_log_user_agent_len_chk;
ALTER TABLE audit_log
    DROP COLUMN IF EXISTS subject,
    DROP COLUMN IF EXISTS entity_id,
    DROP COLUMN IF EXISTS entity_kind,
    DROP COLUMN IF EXISTS user_agent,
    DROP COLUMN IF EXISTS ip_trusted,
    DROP COLUMN IF EXISTS ip;
