-- An index on audit_log.ip — ADR-47.
--
-- Migration 000044 deliberately did NOT create one, and said so in a comment
-- that ended: "on the most-written table in the instance an index nothing
-- reads is write amplification and disk for nothing." That was correct on the
-- day it was written. The reason it gave was not "an ip index is a bad idea",
-- it was "no query needs one" — every query touching the column at the time
-- filtered on created_at and hash-aggregated by ip, and the search reached it
-- only through host(ip) LIKE, which is a function call and sargable on nothing.
--
-- The anomaly panel changes the premise. Its three rules each scan a time
-- window and GROUP BY ip, and two of them are asked for the SHORT windows
-- (15m, 1h) where the created_at index leaves the grouping to a full hash over
-- whatever the window holds. (ip, created_at DESC) lets those grouped scans be
-- served in ip order, and the partial predicate keeps the index off the bulk of
-- the table: the trail is dominated by rows written before 000044 and by any
-- row with no observable address, and none of them are ever grouped by origin.
--
-- The write cost 000044 was protecting against is what the WHERE clause bounds.
-- A row with a NULL ip — every event written by a background path, and every
-- row predating 000044 — never enters this index.
CREATE INDEX audit_log_ip_time_idx
    ON audit_log (ip, created_at DESC)
    WHERE ip IS NOT NULL;
