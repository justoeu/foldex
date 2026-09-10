-- Reverter recoloca o grant que a 000049 tinha semeado. `roleperm.Load` só o
-- honra se a permissão deixar de ser travada no código — a tabela sozinha não
-- reabre o furo.
INSERT INTO role_permission (role, permission) VALUES
    ('admin', 'instance.backup_download')
ON CONFLICT DO NOTHING;
