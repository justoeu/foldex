-- A login challenge may have only one current e-mail OTP. Runtime replaces the
-- code under the challenge owner's row lock; this index is the database
-- backstop against a future writer or a missed serialization path.
--
-- Keep the newest legacy row and consume older duplicates before creating the
-- index so an upgrade remains bootable if an earlier race already produced an
-- impossible state.
LOCK TABLE email_otp IN SHARE ROW EXCLUSIVE MODE;

WITH ranked AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY challenge_id
               ORDER BY created_at DESC, id DESC
           ) AS position
    FROM email_otp
    WHERE purpose = 'login_2fa'
      AND challenge_id IS NOT NULL
      AND consumed_at IS NULL
)
UPDATE email_otp AS otp
SET consumed_at = now()
FROM ranked
WHERE otp.id = ranked.id
  AND ranked.position > 1;

CREATE UNIQUE INDEX email_otp_one_live_login_challenge_idx
    ON email_otp (challenge_id)
    WHERE purpose = 'login_2fa'
      AND challenge_id IS NOT NULL
      AND consumed_at IS NULL;
