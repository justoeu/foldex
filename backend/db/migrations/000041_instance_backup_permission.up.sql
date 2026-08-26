-- ADR-43 PR5: the operational backup status surface gains its permission.
--
-- instance.backup gates GET /api/admin/backup/runs and POST
-- /api/admin/backup/run. It is EDITABLE (unlike instance.transfer), so the
-- admin grant lives in role_permission like every other editable entry; the
-- owner never reads this table — its grant comes from the compiled matrix on
-- every resolution (migration 000039's rationale).
--
-- ON CONFLICT DO NOTHING keeps the seed idempotent against an instance whose
-- owner already granted it by hand through the matrix screen.
INSERT INTO role_permission (role, permission)
VALUES ('admin', 'instance.backup')
ON CONFLICT DO NOTHING;
