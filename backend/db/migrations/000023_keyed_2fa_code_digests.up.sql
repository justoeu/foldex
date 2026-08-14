-- 000023_keyed_2fa_code_digests
--
-- Six-digit login OTPs and recovery codes now use purpose-separated keyed
-- HMAC-SHA256 digests derived from AUTH_ENCRYPTION_KEY. The MAC input binds its
-- version, purpose, user, optional challenge, and normalized code. Recovery
-- codes also grow from 50 to 80 random bits (XXXX-XXXX-XXXX-XXXX).
--
-- Existing SHA-256 rows cannot be converted because the plaintext was
-- deliberately never stored. They are also indistinguishable by shape from the
-- new 32-byte MACs, so accepting both formats would retain offline enumeration.
-- Delete every affected old digest atomically: pending six-digit login codes
-- can be re-sent and users with a legacy recovery sheet must regenerate it from
-- Settings. High-entropy verify_email link hashes are not part of this change.

DELETE FROM email_otp WHERE purpose = 'login_2fa';
DELETE FROM recovery_code;
