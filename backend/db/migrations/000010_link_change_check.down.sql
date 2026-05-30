-- 000010_link_change_check.down.sql
DROP INDEX IF EXISTS link_change_recent_idx;
DROP INDEX IF EXISTS link_check_due_idx;
ALTER TABLE link DROP CONSTRAINT IF EXISTS link_check_interval_valid;
ALTER TABLE link
    DROP COLUMN IF EXISTS last_check_error,
    DROP COLUMN IF EXISTS change_seen_at,
    DROP COLUMN IF EXISTS last_change_detected_at,
    DROP COLUMN IF EXISTS last_fingerprint,
    DROP COLUMN IF EXISTS last_checked_at,
    DROP COLUMN IF EXISTS check_interval;
