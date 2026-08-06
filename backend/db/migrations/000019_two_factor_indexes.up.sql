-- 000019_two_factor_indexes.up.sql
--
-- One column and two indexes the second-factor queries need. Migration 000017 created every
-- 2FA TABLE (auth_challenge, email_otp, totp_secret, recovery_code,
-- password_reset) but not these two access paths, because the queries that use
-- them did not exist yet — they arrived with PR3.
--
-- Both back statements on the LOGIN path, and both are reachable before any
-- session exists, so the tables they scan grow at whatever rate an attacker
-- chooses. A sequential scan here is not merely slow: it is slow in proportion
-- to how much junk an unauthenticated caller has inserted.

-- ReserveChallengeSend reads `max(created_at) WHERE challenge_id = $1` to
-- enforce the 60-second resend cooldown, and its fallback diagnostic query
-- filters the same way. email_otp_lookup_idx leads with user_id, so neither
-- statement can use it. created_at DESC is included so the max() is an index
-- scan's first row rather than an aggregate over the matching set.
CREATE INDEX email_otp_challenge_idx ON email_otp (challenge_id, created_at DESC);

-- CreateChallenge supersedes any live challenge for the same (user, purpose)
-- before inserting a new one — that is what stops a user who retries the
-- password form from accumulating challenges, each carrying its own fresh
-- 5-attempt budget. It runs on EVERY successful password entry for a 2FA
-- account. email_otp got the analogous composite index in 000017;
-- auth_challenge did not, because at the time nothing queried it this way.
CREATE INDEX auth_challenge_user_purpose_idx ON auth_challenge (user_id, purpose, consumed_at);


-- Records that the FIRST factor was proven by reading the account's mailbox
-- (a password-reset link) rather than by knowing the password.
--
-- This closes a full account takeover. Resetting a password correctly diverts a
-- 2FA account into a challenge, because the reset proves only one factor. But
-- the e-mail OTP fallback then mails a six-digit code to the SAME address the
-- reset link arrived at, so whoever can read the mailbox completes both steps
-- on one channel and the second factor buys nothing:
--
--   /password/forgot -> link in mailbox -> /password/reset -> challenge
--                    -> /2fa/email -> code in the SAME mailbox -> /2fa/verify -> session
--
-- With this set, the challenge refuses the e-mail factor; only an authenticator
-- code or a recovery code finishes it, and the mailbox contains neither.
ALTER TABLE auth_challenge
    ADD COLUMN mailbox_already_proven BOOLEAN NOT NULL DEFAULT FALSE;
