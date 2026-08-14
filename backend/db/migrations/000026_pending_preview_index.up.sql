-- The preview recovery sweep runs every 45 seconds across tenants and needs
-- only pending link ids. A partial index avoids scanning completed links.
CREATE INDEX link_preview_pending_idx ON link (id)
  WHERE preview_status = 'pending';
