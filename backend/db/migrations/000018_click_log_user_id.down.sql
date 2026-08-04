-- 000018_click_log_user_id.down.sql
--
-- Fully reversible: user_id here is a denormalized accelerator, never the source
-- of truth. entity_kind/entity_id still identify what was clicked, and the
-- owner is recoverable at any time by joining link/note — which is exactly what
-- the queries did before this migration and will do again after it.
--
-- The only loss is the orphan rows that the up migration deleted (click_log
-- entries whose link or note no longer exists). Those described nothing and had
-- no recoverable owner; they are not restorable, and were not meaningful.

DROP INDEX IF EXISTS click_log_user_clicked_idx;
DROP INDEX IF EXISTS click_log_user_entity_idx;

ALTER TABLE click_log DROP COLUMN user_id;
