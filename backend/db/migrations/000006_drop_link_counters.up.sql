-- link.click_count and link.last_clicked_at were denormalized caches of data
-- that already lives in click_log (introduced in 000003 + backfilled in 000004).
-- Drop them so click_log becomes the single source of truth — no more risk of
-- the two views drifting after a crash, a bad migration, or a manual insert.
--
-- Queries that need the counter / last_clicked_at now derive them via a
-- LATERAL join, e.g.
--   LEFT JOIN LATERAL (
--     SELECT count(*) AS cnt, max(clicked_at) AS last_at
--     FROM click_log WHERE link_id = l.id
--   ) cl ON TRUE
--
-- The index `link_pinned_created` defined in 000005 stays. We drop
-- click_count's implicit support in the link_created index because the column
-- it referenced is gone (the index itself does not reference click_count).

ALTER TABLE link
  DROP COLUMN IF EXISTS click_count,
  DROP COLUMN IF EXISTS last_clicked_at;
