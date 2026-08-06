-- Reverses 000021. Dropping the guard cannot fail on data: it only ever
-- refused writes, so every row already satisfies it.

DROP TRIGGER IF EXISTS user_identity_credential_check ON user_identity;
DROP TRIGGER IF EXISTS app_user_credential_check ON app_user;

DROP FUNCTION IF EXISTS user_identity_credential_guard();
DROP FUNCTION IF EXISTS app_user_credential_guard();
DROP FUNCTION IF EXISTS assert_active_user_has_credential(BIGINT);

ALTER TABLE auth_challenge
    DROP COLUMN IF EXISTS oauth_email,
    DROP COLUMN IF EXISTS oauth_subject,
    DROP COLUMN IF EXISTS oauth_provider;
