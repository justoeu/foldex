-- 000014_notes.up.sql
--
-- Notes entity (pastebin-style rich content) — share link_tag/click_log/folder.
--
-- The user's choice was "tabela `note` separada mas compartilhando
-- link_tag/folder/click_log". To share those two M:N/event tables without
-- duplicating their structure we polymorphize them with an `entity_kind`
-- discriminator ('link' | 'note'). FKs to link(id) are dropped because a
-- polymorphic column can't reference two tables; cascade moves to app-level
-- (notes.Repository.Delete deletes its own link_tag/click_log rows in the
-- same tx). See ADR-27.
--
-- Schema changes:
--   click_log: rename link_id→entity_id, add entity_kind, drop FK to link,
--              add CHECK + replace composite index with (entity_kind,entity_id,...)
--   link_tag:  rename link_id→entity_id, add entity_kind, drop FK to link,
--              replace PK (link_id,tag_id)→(entity_kind,entity_id,tag_id),
--              add CHECK. tag_id FK + link_tag_tag index unchanged.
--   note:      new table mirroring link's structure for title/slug/folder/pinned
--              + body_html/body_text (denormalized plain text for ILIKE search).
--              Same slug format CHECK as link (so /n/{slug} never shadows /n/{id}).
--
-- App-level cascade (replaces ON DELETE CASCADE that the FK carried):
--   notes.Repository.Delete(ctx, id) executes in one tx:
--     DELETE FROM link_tag  WHERE entity_kind='note' AND entity_id=$1;
--     DELETE FROM click_log WHERE entity_kind='note' AND entity_id=$1;
--     DELETE FROM note WHERE id=$1;
-- Backup wipe mode adds `note` to its TRUNCATE list (backup/db.go).

-- ─── click_log: polymorphize ───────────────────────────────────────────
ALTER TABLE click_log RENAME COLUMN link_id TO entity_id;
ALTER TABLE click_log ADD COLUMN entity_kind TEXT NOT NULL DEFAULT 'link';
ALTER TABLE click_log DROP CONSTRAINT click_log_link_id_fkey;
ALTER TABLE click_log ADD CONSTRAINT click_log_entity_kind_check
    CHECK (entity_kind IN ('link', 'note'));
DROP INDEX click_log_link_id_ts;
CREATE INDEX click_log_entity_ts
    ON click_log (entity_kind, entity_id, clicked_at DESC);

-- ─── link_tag: polymorphize ────────────────────────────────────────────
ALTER TABLE link_tag RENAME COLUMN link_id TO entity_id;
ALTER TABLE link_tag ADD COLUMN entity_kind TEXT NOT NULL DEFAULT 'link';
ALTER TABLE link_tag DROP CONSTRAINT link_tag_link_id_fkey;
ALTER TABLE link_tag DROP CONSTRAINT link_tag_pkey;
ALTER TABLE link_tag ADD CONSTRAINT link_tag_pkey PRIMARY KEY (entity_kind, entity_id, tag_id);
ALTER TABLE link_tag ADD CONSTRAINT link_tag_entity_kind_check
    CHECK (entity_kind IN ('link', 'note'));
-- link_tag_tag (tag_id index) is unaffected by the rename — keep as-is.

-- ─── note: new entity ──────────────────────────────────────────────────
CREATE TABLE note (
    id              BIGSERIAL PRIMARY KEY,
    title           TEXT NOT NULL,
    slug            TEXT NOT NULL,
    body_html       TEXT NOT NULL DEFAULT '',
    body_text       TEXT NOT NULL DEFAULT '',
    pinned          BOOLEAN NOT NULL DEFAULT FALSE,
    folder_id       BIGINT REFERENCES folder(id) ON DELETE SET NULL,
    cover_url       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT note_slug_unique UNIQUE (slug),
    CONSTRAINT note_slug_format
        CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$' AND slug !~ '^[0-9]+$')
);

CREATE INDEX note_created_idx        ON note (created_at DESC);
CREATE INDEX note_pinned_created_idx ON note (pinned DESC, created_at DESC);
CREATE INDEX note_folder_preview_idx ON note (folder_id, pinned DESC, created_at DESC)
    INCLUDE (id, title, cover_url) WHERE folder_id IS NOT NULL;
CREATE INDEX note_title_trgm ON note USING gin (title gin_trgm_ops);
CREATE INDEX note_body_trgm  ON note USING gin (body_text gin_trgm_ops);
CREATE INDEX note_slug_idx   ON note (slug);
