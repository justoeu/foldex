-- 000016_master_password_and_hint.up.sql
--
-- Two additions supporting folder-password RECOVERY (ADR-29), which relaxes
-- ADR-28's "no admin bypass" for the recovery case only:
--
-- 1. app_setting: a generic key/value store for mutable, UI-settable app
--    config. The first (and currently only) key is `master_password_hash` —
--    a bcrypt hash (see internal/folders/password.go) of the operator's
--    master password. The plaintext is never stored or logged. NULL/absent
--    row = no master password configured (recovery disabled). This table is
--    deliberately generic so future singleton settings need no new migration.
--
-- 2. folder.password_hint: an optional, NON-SECRET reminder phrase shown on
--    the unlock prompt to jog the owner's memory. It MUST NOT equal the
--    password (enforced in internal/folders). NULL = no hint. Unlike
--    password_hash it is returned verbatim in folder responses — single-user
--    / local threat model, and surfacing it is the whole point of a hint.

CREATE TABLE app_setting (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE folder ADD COLUMN password_hint TEXT;
