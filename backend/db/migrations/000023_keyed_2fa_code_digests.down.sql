-- Irreversible by design: migration 000023 deletes one-way digests whose raw
-- login OTP/recovery values were never stored, so down cannot reconstruct
-- valid legacy rows safely. Schema and indexes did not change.

SELECT 1;
