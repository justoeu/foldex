-- 000014_notes.down.sql
--
-- Reverses 000014: drops the note table, de-polymorphizes click_log/link_tag.
-- Note rows ARE LOST (no migration-time backfill to a non-existent format).
-- link_tag/click_log rows with entity_kind='note' are also lost; existing
-- entity_kind='link' rows survive the column rename back to link_id.

DROP TABLE IF EXISTS note;

-- link_tag: revert polymorphization
ALTER TABLE link_tag DROP CONSTRAINT link_tag_entity_kind_check;
ALTER TABLE link_tag DROP CONSTRAINT link_tag_pkey;
ALTER TABLE link_tag DROP COLUMN entity_kind;
ALTER TABLE link_tag RENAME COLUMN entity_id TO link_id;
ALTER TABLE link_tag ADD CONSTRAINT link_tag_pkey PRIMARY KEY (link_id, tag_id);
ALTER TABLE link_tag ADD CONSTRAINT link_tag_link_id_fkey
    FOREIGN KEY (link_id) REFERENCES link(id) ON DELETE CASCADE;

-- click_log: revert polymorphization
ALTER TABLE click_log DROP CONSTRAINT click_log_entity_kind_check;
DROP INDEX click_log_entity_ts;
ALTER TABLE click_log DROP COLUMN entity_kind;
ALTER TABLE click_log RENAME COLUMN entity_id TO link_id;
ALTER TABLE click_log ADD CONSTRAINT click_log_link_id_fkey
    FOREIGN KEY (link_id) REFERENCES link(id) ON DELETE CASCADE;
CREATE INDEX click_log_link_id_ts ON click_log (link_id, clicked_at DESC);
