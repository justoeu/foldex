-- Pinned flag lets the user "stick" links at the top of the home grid.
-- Default false; updated in place via PATCH /api/links/:id with `pinned`.
ALTER TABLE link
  ADD COLUMN pinned BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX link_pinned_created ON link (pinned DESC, created_at DESC);
