-- Administrative audit trail — ADR-34.
--
-- Records who did what to whom on the identity surface: sign-ins and their
-- failures, role and status changes, invitations, forced password recoveries
-- and policy edits. Content is deliberately out of scope; a row per link click
-- already exists in click_log, and mixing the two would bury the security
-- events this table exists to surface.
--
-- actor_id and target_id are ON DELETE SET NULL rather than CASCADE: deleting
-- an account must not erase the record of what it did, and "a deleted account
-- promoted this user" is exactly the entry an investigation needs. The e-mail
-- is therefore denormalized alongside the id — after the row is gone, the id
-- alone identifies nobody.
--
-- There is no ip or user_agent column. X-Forwarded-For is only trustworthy
-- behind a configured proxy (see trustedProxyRealIP), so an ip column would be
-- authoritative-looking and attacker-controlled on a direct bind — the worst
-- combination for a table people reach for during an incident.
CREATE TABLE audit_log (
    id           BIGSERIAL PRIMARY KEY,
    action       TEXT NOT NULL,
    actor_id     BIGINT REFERENCES app_user(id) ON DELETE SET NULL,
    actor_email  TEXT,
    target_id    BIGINT REFERENCES app_user(id) ON DELETE SET NULL,
    target_email TEXT,
    -- Free-form, never credential-bearing. Writers pass only values that are
    -- already safe to render: a role name, a status, an invited address.
    detail       TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT audit_log_action_len_chk CHECK (length(action) BETWEEN 1 AND 64),
    CONSTRAINT audit_log_detail_len_chk CHECK (detail IS NULL OR length(detail) <= 512)
);

-- The screen reads newest-first, unfiltered or narrowed to one action. id is
-- part of the key so pagination is stable when several rows share a timestamp.
CREATE INDEX audit_log_created_idx ON audit_log (created_at DESC, id DESC);
CREATE INDEX audit_log_action_created_idx ON audit_log (action, created_at DESC, id DESC);
