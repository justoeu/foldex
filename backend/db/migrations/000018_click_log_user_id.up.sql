-- 000018_click_log_user_id.up.sql
--
-- Denormalizes the owner onto click_log. Migration 000017 deliberately left it
-- off (SDD-AUTH-RBAC §12) so ownership would have exactly one source of truth;
-- every click aggregate reached the owner through
-- `entity_id IN (SELECT id FROM link WHERE user_id = $1)` instead.
--
-- Measurement changed the calculus. click_log lost its FK to link in migration
-- 000014, so the planner has no cross-table statistics and keeps choosing a full
-- Seq Scan over the whole table rather than a nested loop through
-- click_log_entity_ts. On a synthetic 6-tenant / 920k-click database the
-- GET /api/entries subquery — the hottest path in the product, and one the SPA
-- polls every 3s while any preview is pending — went from 25ms to 130ms for a
-- tenant that had not changed at all, purely because ANOTHER tenant accumulated
-- history. That is a cost-isolation failure between tenants, and the semi-join
-- alone does not fix it: it narrows the logical scope without changing the plan.
--
-- The cost of this migration is the thing 000017 was avoiding: a SECOND source
-- of truth for ownership, which can drift from link.user_id / note.user_id.
-- Two things keep it honest:
--   1. Every writer sets user_id from the row it is logging a click for, in the
--      same statement — there is no path that writes click_log without already
--      knowing the owner.
--   2. TestClickLogOwnerMatchesEntityOwner (internal/security) fails if any row
--      disagrees with its entity's owner.
--
-- entity_kind/entity_id stay authoritative for WHICH entity was clicked;
-- user_id is purely a query accelerator for "whose".

ALTER TABLE click_log ADD COLUMN user_id BIGINT REFERENCES app_user(id) ON DELETE CASCADE;

-- Backfill from the polymorphic target. On an installation upgraded through
-- 000017 every link and note already belongs to the bootstrap admin, so this
-- resolves to that single user; on a multi-tenant database it resolves per row.
UPDATE click_log c
SET user_id = l.user_id
FROM link l
WHERE c.entity_kind = 'link' AND c.entity_id = l.id;

UPDATE click_log c
SET user_id = n.user_id
FROM note n
WHERE c.entity_kind = 'note' AND c.entity_id = n.id;

-- Orphans: click_log has had no FK since 000014, so rows can outlive their
-- entity (the app-level cascade is best-effort). They carry no owner and no
-- longer describe anything, so they are dropped rather than parked under an
-- arbitrary user — keeping them would mean inventing ownership.
DELETE FROM click_log WHERE user_id IS NULL;

ALTER TABLE click_log ALTER COLUMN user_id SET NOT NULL;

-- Replaces the semi-join with a direct index hit. entity_kind leads because
-- every query filters it; user_id second because that is the tenant predicate;
-- entity_id last so per-entity aggregation still uses the index.
CREATE INDEX click_log_user_entity_idx ON click_log (entity_kind, user_id, entity_id);

-- Serves owner-scoped range scans over clicked_at. Note that internal/stats
-- deliberately keeps its semi-join through link instead of reading this column
-- (see the comment in stats/repository.go: the semi-join drops click_log rows
-- that outlived their link, which a direct user_id filter would keep counting).
-- The index is here for /api/entries and for future owner-scoped time queries.
CREATE INDEX click_log_user_clicked_idx ON click_log (user_id, clicked_at DESC);

-- click_log_entity_ts (entity_kind, entity_id, clicked_at DESC) stays: the
-- public /go/ and /n/ routes resolve without a session and therefore without a
-- user_id to lead with.
