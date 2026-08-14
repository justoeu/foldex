-- 000027_backup_restore_ledger.up.sql
--
-- Makes default skip-mode backup restore durable across successful repeats and
-- the DB-commit/object-upload failure window. The parent row checkpoints the
-- database phase per owner + exact archive digest + mode; child rows retain the
-- old-to-new entity and note-media mappings needed to resume object writes.
-- No target content FK is possible for the polymorphic entity mapping, so a
-- destructive wipe explicitly removes the owner's ledgers in the same tx.

CREATE TABLE backup_restore (
    user_id            BIGINT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    archive_digest     BYTEA NOT NULL,
    mode               TEXT NOT NULL,
    inserted           JSONB NOT NULL,
    skipped            JSONB NOT NULL,
    warnings           JSONB NOT NULL,
    file_report        JSONB,
    db_completed_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    files_completed_at TIMESTAMPTZ,
    PRIMARY KEY (user_id, archive_digest, mode),
    CONSTRAINT backup_restore_digest_len_check CHECK (octet_length(archive_digest) = 32),
    CONSTRAINT backup_restore_mode_check CHECK (mode IN ('wipe', 'skip', 'duplicate'))
);

CREATE TABLE backup_restore_entity (
    user_id        BIGINT NOT NULL,
    archive_digest BYTEA NOT NULL,
    mode           TEXT NOT NULL,
    entity_kind    TEXT NOT NULL,
    source_id      BIGINT NOT NULL,
    target_id      BIGINT NOT NULL,
    PRIMARY KEY (user_id, archive_digest, mode, entity_kind, source_id),
    FOREIGN KEY (user_id, archive_digest, mode)
        REFERENCES backup_restore (user_id, archive_digest, mode) ON DELETE CASCADE,
    CONSTRAINT backup_restore_entity_kind_check
        CHECK (entity_kind IN ('tag', 'folder', 'link', 'note'))
);

CREATE TABLE backup_restore_file (
    user_id        BIGINT NOT NULL,
    archive_digest BYTEA NOT NULL,
    mode           TEXT NOT NULL,
    source_key     TEXT NOT NULL,
    target_key     TEXT NOT NULL,
    PRIMARY KEY (user_id, archive_digest, mode, source_key),
    FOREIGN KEY (user_id, archive_digest, mode)
        REFERENCES backup_restore (user_id, archive_digest, mode) ON DELETE CASCADE
);
