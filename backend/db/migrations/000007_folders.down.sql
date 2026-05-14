-- 000007_folders.down.sql
DROP INDEX IF EXISTS link_folder;
ALTER TABLE link DROP COLUMN IF EXISTS folder_id;
DROP TABLE IF EXISTS folder;
