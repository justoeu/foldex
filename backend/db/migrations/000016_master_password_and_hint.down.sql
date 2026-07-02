-- 000016_master_password_and_hint.down.sql
--
-- Reverses 000016. Dropping app_setting discards the master password hash
-- (recovery disabled). Dropping folder.password_hint discards every folder's
-- reminder phrase. No other data is touched; folder passwords themselves
-- (folder.password_hash, migration 000015) survive.

ALTER TABLE folder DROP COLUMN password_hint;

DROP TABLE app_setting;
