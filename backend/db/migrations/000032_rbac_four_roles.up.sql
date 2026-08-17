-- Widens the RBAC role set from ('admin','user') to
-- ('owner','admin','editor','viewer') — ADR-33.
--
-- The two-role model conflated "runs the instance" with "may write content".
-- Four roles split those apart: owner and admin both administer, editor and
-- viewer both hold only their own library, and the difference between the two
-- pairs is a permission matrix in internal/pkg/authctx rather than a boolean.
--
-- Mapping is chosen so no existing account loses reach:
--   user  -> editor   (identical capability: full CRUD on its own rows)
--   admin -> admin, EXCEPT the single oldest one, which becomes owner
--
-- Owner is capped at one row by a partial unique index rather than by handler
-- discipline, for the same reason cross-tenant references are FK-blocked: a
-- second owner is not a state any code path should be able to reach, and a
-- transfer that briefly wrote two would corrupt "the account that cannot be
-- demoted". Transfer is therefore a single UPDATE touching both rows.
--
-- The oldest admin is preferred ACTIVE-first: an instance whose only admin row
-- is migration 000017's pending bootstrap placeholder must still end up with
-- that placeholder as the owner, because the setup screen claims exactly that
-- row and would otherwise insert a second one against the new index.

ALTER TABLE app_user DROP CONSTRAINT app_user_role_check;
ALTER TABLE invite   DROP CONSTRAINT invite_role_check;

-- Existing ordinary accounts keep exactly the capability they had.
UPDATE app_user SET role = 'editor' WHERE role = 'user';
UPDATE invite   SET role = 'editor' WHERE role = 'user';

-- Exactly one administrator is promoted to owner. ORDER BY puts an active row
-- ahead of a pending placeholder, then falls back to the oldest id, so the
-- choice is deterministic on any instance.
WITH first_admin AS (
    SELECT id
    FROM app_user
    WHERE role = 'admin'
    ORDER BY (status = 'active') DESC, created_at ASC, id ASC
    LIMIT 1
)
UPDATE app_user SET role = 'owner'
WHERE id IN (SELECT id FROM first_admin);

ALTER TABLE app_user ADD CONSTRAINT app_user_role_check
    CHECK (role IN ('owner', 'admin', 'editor', 'viewer'));

-- An invitation can never mint an owner: ownership moves only by transfer from
-- the live owner, so a leaked invite cannot hand someone the one role that
-- cannot be demoted.
ALTER TABLE invite ADD CONSTRAINT invite_role_check
    CHECK (role IN ('admin', 'editor', 'viewer'));

ALTER TABLE app_user ALTER COLUMN role SET DEFAULT 'editor';
ALTER TABLE invite   ALTER COLUMN role SET DEFAULT 'editor';

CREATE UNIQUE INDEX app_user_single_owner_uniq ON app_user (role) WHERE role = 'owner';
