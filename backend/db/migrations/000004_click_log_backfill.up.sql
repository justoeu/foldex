-- Click_log was added in 000003 but click_count on link has been bumped since
-- the project started. Backfill the gap so analytics show pre-000003 clicks.
--
-- For each link, insert (click_count - rows already in click_log) synthetic
-- rows stamped at `last_clicked_at` (or `created_at` if the link was never
-- clicked — defensive, this branch shouldn't fire because click_count=0
-- implies no rows to insert). We use `clicked_at = last_clicked_at` because
-- it's the only timestamp we have; the daily chart will show old clicks
-- bunched on that day, which is honest about the precision available.
--
-- Idempotent: running it again on a healthy state inserts zero rows because
-- click_count == click_log count for every link.

WITH gap AS (
  SELECT
    l.id,
    COALESCE(l.last_clicked_at, l.created_at) AS ts,
    GREATEST(
      l.click_count - (SELECT count(*) FROM click_log cl WHERE cl.link_id = l.id),
      0
    ) AS missing
  FROM link l
)
INSERT INTO click_log (link_id, clicked_at)
SELECT g.id, g.ts
FROM gap g
CROSS JOIN generate_series(1, g.missing::int)
WHERE g.missing > 0;
