-- 000028_password_reset_credential_epoch (down)

ALTER TABLE password_reset
    DROP CONSTRAINT password_reset_token_version_present,
    DROP COLUMN token_version;
