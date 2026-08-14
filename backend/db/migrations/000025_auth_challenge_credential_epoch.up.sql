-- 000025_auth_challenge_credential_epoch
--
-- A pre-auth challenge proves credentials from one exact app_user credential
-- epoch. Password changes/resets, logout-all and administrative revocation bump
-- token_version, so a challenge minted before any of them must become unusable.

ALTER TABLE auth_challenge
    ADD COLUMN token_version INTEGER;

-- Pre-000025 rows remain NULL and therefore fail every live-epoch join. A
-- NOT VALID check preserves those short-lived rows for a genuinely reversible
-- migration while requiring every new/updated row to carry a real epoch.
ALTER TABLE auth_challenge
    ADD CONSTRAINT auth_challenge_token_version_present
    CHECK (token_version IS NOT NULL) NOT VALID;

-- Pending TOTP enrollment is another proof-derived artifact. Confirmed factors
-- survive credential changes, but an unconfirmed seed may only be activated in
-- the credential epoch and, for Settings, exact session that authorized it.
ALTER TABLE totp_secret
    ADD COLUMN enrollment_token_version INTEGER,
    ADD COLUMN enrollment_session_id BIGINT REFERENCES session(id) ON DELETE CASCADE;

ALTER TABLE totp_secret
    ADD CONSTRAINT totp_secret_pending_epoch_present
    CHECK (confirmed_at IS NOT NULL OR enrollment_token_version IS NOT NULL) NOT VALID;
