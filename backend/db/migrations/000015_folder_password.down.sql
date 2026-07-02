-- 000015_folder_password.down.sql
--
-- Reverses 000015: drops password_hash. Any folder protection in place at
-- rollback time is LOST (the column, and with it every hash, is gone) —
-- every folder becomes unprotected. No data outside this column is touched.

ALTER TABLE folder DROP COLUMN password_hash;
