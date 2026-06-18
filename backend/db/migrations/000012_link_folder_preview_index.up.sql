-- Composite index for the folder-card previews LATERAL in
-- folders/repository.go:List. The previews subquery is:
--   SELECT id, title, og_image_url, favicon_url FROM link
--   WHERE folder_id = $1 ORDER BY pinned DESC, created_at DESC LIMIT 4
--
-- The pre-existing link_folder (folder_id) partial index serves the WHERE
-- but not the ORDER BY, forcing an explicit sort per folder row on every
-- List call. With the composite + INCLUDE columns the planner can do an
-- index-only scan in the right order and stop after 4 entries — meaningful
-- when the home grid renders many folders × many links each.
--
-- INCLUDE columns are stored in the leaf but not used for ordering, so they
-- can't be DESC; that's fine — they're projection-only.
--
-- No data-shape change → CurrentSchemaVersion unchanged (8). A restore from
-- an older backup into a server with this index still works; the index is
-- rebuilt from the freshly-inserted rows during the post-commit phase.
CREATE INDEX link_folder_preview_idx ON link
  (folder_id, pinned DESC, created_at DESC)
  INCLUDE (id, title, og_image_url, favicon_url)
  WHERE folder_id IS NOT NULL;
