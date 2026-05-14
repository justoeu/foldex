-- 000008_folder_nesting.up.sql
-- Allows folders to nest. parent_id IS NULL = root folder; non-null = inside
-- another folder. ON DELETE SET NULL: deleting a folder promotes its children
-- to root (matches the link-side behavior — nothing dies just because the
-- container died). The aggressive "cascade" path (DeleteCascade on the repo)
-- explicitly recurses via CTE before deleting, when the user opts in.

ALTER TABLE folder
  ADD COLUMN parent_id BIGINT REFERENCES folder(id) ON DELETE SET NULL;

CREATE INDEX folder_parent ON folder (parent_id) WHERE parent_id IS NOT NULL;
