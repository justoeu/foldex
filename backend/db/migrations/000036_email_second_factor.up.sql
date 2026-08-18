-- 000036_email_second_factor
--
-- E-mail becomes a second factor an account ENROLLS, instead of an escape hatch
-- that only exists inside a challenge which is already TOTP.
--
-- Before this, `emailFactorAvailable` required `purpose == 'totp'`, and a
-- challenge only ever became 'totp' for an account that already had a confirmed
-- authenticator. So an account without TOTP never received a challenge at all,
-- and `/2fa/email` was unreachable for exactly the users who had no other
-- factor. This table is what "this account has e-mail as a factor" means.
--
-- Deliberately mirrors totp_secret rather than inventing a shape: the epoch
-- binding below is the same one migration 000025 applied there, so the existing
-- confirmation patterns transfer without reinterpretation.
CREATE TABLE email_factor (
    user_id      BIGINT PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    confirmed_at TIMESTAMPTZ,

    -- A pending enrollment may only be activated in the credential epoch — and,
    -- from Settings, the exact session — that authorized it. Password changes,
    -- resets, logout-all and administrative revocation bump token_version, so a
    -- half-finished enrollment started before any of them fails closed instead
    -- of installing a factor on an account whose credentials just moved.
    enrollment_token_version INTEGER,
    enrollment_session_id    BIGINT REFERENCES session(id) ON DELETE CASCADE,

    -- Same shape as totp_secret_pending_epoch_present: a row is legitimate when
    -- it is confirmed, or when it is pending AND carries the epoch it was
    -- authorized in. A pending row with no epoch could be confirmed at any time
    -- by anyone who could reach the endpoint.
    CONSTRAINT email_factor_pending_epoch_present
        CHECK (confirmed_at IS NOT NULL OR enrollment_token_version IS NOT NULL)
);

-- `user.email_2fa_enabled` is derived with EXISTS against this predicate on
-- every read, never cached — same reasoning as totp_enabled: a stored boolean
-- would need updating in four places, and the direction of the first
-- disagreement decides whether login demands a code the user cannot produce.
CREATE INDEX email_factor_confirmed_idx ON email_factor (user_id)
    WHERE confirmed_at IS NOT NULL;

-- Enrollment and session step-up each send a code, and neither may be
-- interchangeable with a login code. Reusing 'login_2fa' would let a code mailed
-- to prove "you can read this mailbox, so we may enroll it" be redeemed at a
-- login prompt, and vice versa — the purpose is what keeps the redemptions
-- apart, exactly as it already does for 'verify_email'.
--
-- 'step_up_2fa' is separate from 'enroll_email_2fa' for the same reason at one
-- remove: an enrollment code proves a mailbox nobody has accepted yet, while a
-- step-up code is a proof presented BY an already-enrolled factor to authorize
-- removing a credential. A shared purpose would let the first be spent as the
-- second, so mailbox control during a not-yet-confirmed enrollment would reach
-- operations that demand a live factor.
ALTER TABLE email_otp DROP CONSTRAINT email_otp_purpose_check;
ALTER TABLE email_otp ADD CONSTRAINT email_otp_purpose_check
    CHECK (purpose IN ('login_2fa', 'verify_email', 'enroll_email_2fa', 'step_up_2fa'));
