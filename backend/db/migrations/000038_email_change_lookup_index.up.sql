-- 000038_email_change_lookup_index
--
-- The administration surface's e-mail availability probe asks whether an
-- address is free, and "free" has to include addresses somebody is already
-- MOVING to: `email_change_one_pending` guards one live request per USER, and
-- the unique index on app_user guards only the live column, so nothing stops
-- two accounts from having a pending move to the same address — the second one
-- simply loses at confirmation, after being told the address was available.
--
-- Without this index that second arm has no index on `new_email_normalized`.
-- The planner falls back to repurposing `email_change_one_pending` as a
-- predicate-only bitmap scan and then filters, so the cost is O(every live
-- pending change on the instance) — paid on the COMMON path, since an address
-- the administrator is typing is free almost every time. Measured at 5k live
-- pending rows: 90 buffers and 0.345 ms, against 4 buffers and 0.035 ms with
-- this index.
--
-- Partial on `consumed_at IS NULL` because consumed rows are exactly the ones
-- the query does not want, and they never leave the table — `email_change` has
-- no sweeper, by design (mig 000037), so the full-table version would grow with
-- instance history forever while the useful set stays small.
CREATE INDEX IF NOT EXISTS email_change_new_email_pending_idx
    ON email_change (new_email_normalized)
    WHERE consumed_at IS NULL;
