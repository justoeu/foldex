-- Reverses 000036_email_second_factor.
--
-- Dropping the table removes every enrolled e-mail factor, so an account whose
-- ONLY factor was e-mail comes back down to one. That is the honest outcome of
-- reverting the feature that created it — the alternative would be leaving rows
-- behind that nothing reads.
--
-- The purpose constraint is restored to its 000017 form, which means any
-- outstanding 'enroll_email_2fa' or 'step_up_2fa' code has to go first: they
-- would violate the narrower CHECK, and both are worthless without the table
-- anyway — the step-up path only ever accepts a code from an enrolled factor.
DELETE FROM email_otp WHERE purpose IN ('enroll_email_2fa', 'step_up_2fa');

ALTER TABLE email_otp DROP CONSTRAINT email_otp_purpose_check;
ALTER TABLE email_otp ADD CONSTRAINT email_otp_purpose_check
    CHECK (purpose IN ('login_2fa', 'verify_email'));

DROP TABLE IF EXISTS email_factor;
