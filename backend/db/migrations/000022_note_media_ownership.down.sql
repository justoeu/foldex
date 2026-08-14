-- 000022_note_media_ownership.down.sql
-- Remove note-media ownership metadata. Objects in RustFS are deliberately
-- untouched; rolling back schema must never turn a migration into bulk data
-- deletion.

DROP TRIGGER IF EXISTS note_media_ref_release_after_delete ON note_media_ref;
DROP FUNCTION IF EXISTS release_deleted_note_media_refs();
DROP TABLE IF EXISTS note_media_ref;
DROP TABLE IF EXISTS note_media;
ALTER TABLE note DROP CONSTRAINT IF EXISTS note_user_id_unique;
