-- 000007_folders.up.sql
-- Adds iPhone-style folder organization for links.
--
-- Folder is 1:N with link (a link belongs to at most one folder). Tag M:N
-- continues to exist in parallel — folders = containment, tags = labels. See
-- docs/ARCHITECTURE.md ADR-19.
--
-- ON DELETE SET NULL preserves links when a folder is deleted (link becomes
-- ungrouped). Index is partial so it stays tiny when most links are ungrouped.
-- Both `name` and `color` get a 200-char cap (color may hold a gradient CSS
-- string, never a data: URL — same anti-pattern guard we already eat on tag).

CREATE TABLE folder (
  id         BIGSERIAL PRIMARY KEY,
  name       VARCHAR(200) NOT NULL,
  color      VARCHAR(200) NOT NULL DEFAULT '#6366F1',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE link
  ADD COLUMN folder_id BIGINT REFERENCES folder(id) ON DELETE SET NULL;

CREATE INDEX link_folder ON link (folder_id) WHERE folder_id IS NOT NULL;
