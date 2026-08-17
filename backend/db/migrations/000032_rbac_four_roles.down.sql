-- Collapses the four-role set back to ('admin','user').
--
-- owner  -> admin   (the instance keeps an administrator)
-- editor -> user
-- viewer -> user    LOSSY: a viewer regains write access, because the old model
--                   has no way to express read-only. Rolling back with viewers
--                   present therefore widens their reach, and re-applying the
--                   up migration cannot tell them apart from editors again.

DROP INDEX IF EXISTS app_user_single_owner_uniq;

ALTER TABLE app_user DROP CONSTRAINT app_user_role_check;
ALTER TABLE invite   DROP CONSTRAINT invite_role_check;

UPDATE app_user SET role = 'admin' WHERE role = 'owner';
UPDATE app_user SET role = 'user'  WHERE role IN ('editor', 'viewer');
UPDATE invite   SET role = 'user'  WHERE role IN ('editor', 'viewer');

ALTER TABLE app_user ADD CONSTRAINT app_user_role_check CHECK (role IN ('admin', 'user'));
ALTER TABLE invite   ADD CONSTRAINT invite_role_check   CHECK (role IN ('admin', 'user'));

ALTER TABLE app_user ALTER COLUMN role SET DEFAULT 'user';
ALTER TABLE invite   ALTER COLUMN role SET DEFAULT 'user';
