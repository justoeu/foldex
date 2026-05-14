-- Reverse 000006: add the columns back and rehydrate from click_log so any
-- existing analytics keep working after a rollback.

ALTER TABLE link
  ADD COLUMN IF NOT EXISTS click_count BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS last_clicked_at TIMESTAMPTZ;

UPDATE link l
SET click_count    = COALESCE(cl.cnt, 0),
    last_clicked_at = cl.last_at
FROM (
  SELECT link_id, count(*) AS cnt, max(clicked_at) AS last_at
  FROM click_log
  GROUP BY link_id
) cl
WHERE cl.link_id = l.id;
