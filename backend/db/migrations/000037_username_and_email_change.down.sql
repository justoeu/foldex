ALTER TABLE session DROP CONSTRAINT IF EXISTS session_revoked_reason_check;
-- Rows already carrying the reason this migration added would fail the narrower
-- CHECK, so they are relabelled rather than left to block the rollback. The
-- session is dead either way; only the recorded cause is approximated.
UPDATE session SET revoked_reason = 'password_changed' WHERE revoked_reason = 'email_changed';
ALTER TABLE session ADD CONSTRAINT session_revoked_reason_check CHECK (
    revoked_reason IS NULL OR revoked_reason IN
        ('logout', 'logout_all', 'reuse_detected', 'password_changed',
         'admin_revoked', 'user_disabled', 'expired')
);

-- Reverses 000037. Pending e-mail changes are dropped with the table: they are
-- requests, not credentials, and one that outlived its own feature would resolve
-- against a column that no longer exists.
DROP TABLE IF EXISTS email_change;

ALTER TABLE app_user DROP CONSTRAINT IF EXISTS app_user_username_pair;
ALTER TABLE app_user DROP CONSTRAINT IF EXISTS app_user_username_shape;
DROP INDEX IF EXISTS app_user_username_norm_uniq;
ALTER TABLE app_user DROP COLUMN IF EXISTS username_normalized;
ALTER TABLE app_user DROP COLUMN IF EXISTS username;
