-- Ranking projection of click_log (INV-056).
--
-- click_log remains the source of truth. entity_click_stats is one row per
-- (owner, kind, entity) so click/recent sorts can JOIN instead of GROUP BY
-- the caller's entire history before LIMIT. Do not add click_count to
-- link/note — that is the denormalisation 000006 removed.
--
-- Backfill is the deploy-time catch-up. After that, clicklog.Record UPSERTs
-- on every public click, and bulk writers (import, restore) refresh the
-- same rows. Down drops the projection; click_log is untouched.
CREATE TABLE entity_click_stats (
    user_id         BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    entity_kind     TEXT NOT NULL,
    entity_id       BIGINT NOT NULL,
    click_count     BIGINT NOT NULL,
    last_clicked_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, entity_kind, entity_id),
    CONSTRAINT entity_click_stats_kind_chk CHECK (entity_kind IN ('link', 'note')),
    CONSTRAINT entity_click_stats_count_chk CHECK (click_count > 0)
);

INSERT INTO entity_click_stats (user_id, entity_kind, entity_id, click_count, last_clicked_at)
SELECT user_id, entity_kind, entity_id, count(*)::bigint, max(clicked_at)
FROM click_log
GROUP BY user_id, entity_kind, entity_id;
