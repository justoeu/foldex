-- 000022_note_media_ownership.up.sql
--
-- Durable ownership and references for inline note media. Public
-- /api/files/notes/<uuid> URLs remain readable without a session, but the URL
-- is never write/delete authority. New uploads receive an owner-scoped lease;
-- note_media_ref can only join a note and media belonging to the same user.
-- Existing notes/<uuid> objects are intentionally NOT backfilled from
-- body_html: user-authored HTML is a reference, not proof of ownership. Those
-- legacy keys remain readable and fail closed for every destructive path.

ALTER TABLE note
    ADD CONSTRAINT note_user_id_unique UNIQUE (user_id, id);

CREATE TABLE note_media (
    object_key       TEXT PRIMARY KEY,
    user_id          BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    lease_expires_at TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT note_media_user_key_unique UNIQUE (user_id, object_key),
    CONSTRAINT note_media_key_format CHECK (
        object_key ~ '^notes/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.(jpg|jpeg|png|gif|webp)$'
    )
);

CREATE TABLE note_media_ref (
    user_id    BIGINT NOT NULL,
    note_id    BIGINT NOT NULL,
    object_key TEXT NOT NULL,
    PRIMARY KEY (note_id, object_key),
    CONSTRAINT note_media_ref_note_same_user_fkey
        FOREIGN KEY (user_id, note_id) REFERENCES note(user_id, id) ON DELETE CASCADE,
    CONSTRAINT note_media_ref_media_same_user_fkey
        FOREIGN KEY (user_id, object_key) REFERENCES note_media(user_id, object_key) ON DELETE CASCADE
);

CREATE INDEX note_media_expired_idx
    ON note_media (lease_expires_at, object_key)
    WHERE lease_expires_at IS NOT NULL;
CREATE INDEX note_media_ref_user_object_idx
    ON note_media_ref (user_id, object_key);

-- Direct SQL note deletion remains safe even if it bypasses repository
-- cleanup: the FK cascade removes refs and this statement-level trigger turns
-- newly unreferenced media back into an expired lease for the bounded sweeper.
CREATE FUNCTION release_deleted_note_media_refs() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE note_media m
    SET lease_expires_at = now()
    FROM (SELECT DISTINCT user_id, object_key FROM deleted_refs) d
    WHERE m.user_id = d.user_id
      AND m.object_key = d.object_key
      AND NOT EXISTS (
          SELECT 1 FROM note_media_ref r
          WHERE r.user_id = m.user_id AND r.object_key = m.object_key
      );
    RETURN NULL;
END;
$$;

CREATE TRIGGER note_media_ref_release_after_delete
AFTER DELETE ON note_media_ref
REFERENCING OLD TABLE AS deleted_refs
FOR EACH STATEMENT EXECUTE FUNCTION release_deleted_note_media_refs();
