-- 000028_password_reset_credential_epoch
--
-- A password-reset link proves mailbox control in one exact app_user credential
-- epoch. Password changes, logout-all, role/status changes and administrator
-- session revocation bump token_version, so links minted before those events
-- must not remain account-takeover credentials.

ALTER TABLE password_reset
    ADD COLUMN token_version INTEGER;

-- Pre-000028 rows remain NULL and fail every live-epoch check. NOT VALID keeps
-- the migration reversible without assigning an epoch to proof created before
-- the binding existed, while requiring every new or updated row to carry one.
ALTER TABLE password_reset
    ADD CONSTRAINT password_reset_token_version_present
    CHECK (token_version IS NOT NULL) NOT VALID;
