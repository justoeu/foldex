-- ADR-42: the RBAC matrix becomes configurable.
--
-- One row per (role, permission) the instance GRANTS. The compiled matrix in
-- internal/pkg/authctx stays the seed and the floor: this table records the
-- editable delta, and resolution unions it with the locked entries, so an empty
-- or missing table degrades to "everyone keeps their locked permissions" rather
-- than "nobody can do anything".
--
-- The owner is deliberately NOT stored. Its grants come from the compiled
-- matrix on every resolution, which is what guarantees that no state of this
-- table can leave the instance without a role able to repair it.
CREATE TABLE role_permission (
    role       TEXT NOT NULL,
    permission TEXT NOT NULL,
    granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (role, permission),
    -- Mirrors app_user's constraint minus the owner, for the reason above.
    CONSTRAINT role_permission_role_check CHECK (role IN ('admin', 'editor', 'viewer'))
);

-- Seed the EDITABLE half of what the compiled matrix held on the day this
-- shipped, so an instance that never opens the screen behaves identically to
-- before.
--
-- The locked entries (content.read, roles.assign) are deliberately absent:
-- resolution reads those from the compiled matrix whatever this table says, so
-- a row here would be a second source of truth for the one part of the matrix
-- that must not have one — and a reader comparing the two would have no way to
-- know which wins.
INSERT INTO role_permission (role, permission) VALUES
    ('admin',  'content.write'),
    ('admin',  'backup.export'),
    ('admin',  'backup.restore'),
    ('admin',  'import.run'),
    ('admin',  'users.read'),
    ('admin',  'users.write'),
    ('admin',  'invites.read'),
    ('admin',  'invites.write'),
    ('admin',  'audit.read'),
    ('admin',  'policy.read'),
    ('editor', 'content.write'),
    ('editor', 'backup.export'),
    ('editor', 'backup.restore'),
    ('editor', 'import.run'),
    ('viewer', 'backup.export')
ON CONFLICT DO NOTHING;
