-- 000025_auth_challenge_credential_epoch (down)

ALTER TABLE auth_challenge
    DROP CONSTRAINT auth_challenge_token_version_present,
    DROP COLUMN token_version;

ALTER TABLE totp_secret
    DROP CONSTRAINT totp_secret_pending_epoch_present,
    DROP COLUMN enrollment_session_id,
    DROP COLUMN enrollment_token_version;
