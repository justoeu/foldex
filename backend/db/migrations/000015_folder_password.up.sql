-- 000015_folder_password.up.sql
--
-- Folder password protection — a privacy gate on browsing INTO a folder,
-- not a hard multi-tenant security boundary (this is a single-user app
-- behind one shared secret). NULL = unprotected (the default for every
-- existing folder). Non-NULL = a bcrypt hash (see internal/folders/password.go);
-- the plaintext password is never stored or logged.
--
-- No CHECK constraint: bcrypt output is always non-empty text when set, and
-- the column's only other legal state is NULL.

ALTER TABLE folder ADD COLUMN password_hash TEXT;
