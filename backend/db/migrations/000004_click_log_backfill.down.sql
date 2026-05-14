-- Down for click_log_backfill is intentionally a no-op: we can't tell which
-- rows were synthetic vs real after the fact. Reverting 000003 instead drops
-- the entire click_log table.
SELECT 1;
