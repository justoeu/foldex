-- ADR-48 — download de artefatos de backup pela tela de administração.
--
-- Uma linha por download ADMITIDO, não por download concluído. O teto do
-- INV-187 (3 por administrador, por artefato) conta reservas: cobrar só na
-- conclusão deixaria um atacante baixar 99% e abortar em laço, e quem recebeu
-- 99% dos bytes recebeu os dados. `completed` e `bytes_sent` existem para a
-- auditoria responder "chegou inteiro?", nunca para decidir o orçamento.
--
-- Não há coluna de senha, e não pode haver: a passphrase que cifra o artefato
-- existe apenas durante a requisição e nunca é persistida em lugar nenhum.
CREATE TABLE backup_download (
    id         BIGSERIAL   PRIMARY KEY,
    -- ON DELETE CASCADE: o histórico do run é o dono da série. Quando a linha
    -- do backup_run some (retenção), o orçamento dela deixa de existir junto —
    -- não há artefato para gastá-lo.
    run_id     BIGINT      NOT NULL REFERENCES backup_run(id) ON DELETE CASCADE,
    user_id    BIGINT      NOT NULL REFERENCES app_user(id)   ON DELETE CASCADE,
    -- A chave exata servida, e não só o run: um run de user_zip aponta para N
    -- objetos, então "qual artefato foi baixado" não é derivável do run_id.
    object_key TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    bytes_sent BIGINT,
    completed  BOOLEAN     NOT NULL DEFAULT false
);

-- A consulta do orçamento é sempre (run, usuário, chave) — o índice é a
-- consulta, e ela roda antes de CADA download.
CREATE INDEX backup_download_budget_idx ON backup_download (run_id, user_id, object_key);

-- A permissão nova precisa existir na metade EDITÁVEL da matriz, senão um
-- administrador nunca a recebe: `role_permission` é a fonte viva (INV-167) e
-- 000039 a semeou a partir da matriz compilada de então.
--
-- Não dá para editar 000039: ela já foi aplicada. Uma migração aplicada é
-- congelada — `schema_migrations` guarda só um NÚMERO, então o que for
-- acrescentado a ela depois nunca alcança um banco que já a rodou, e nada
-- reporta a divergência (CLAUDE.md §7).
--
-- ON CONFLICT DO NOTHING: um owner que já tenha decidido o contrário nesta
-- instância não pode ser sobrescrito por uma migração.
INSERT INTO role_permission (role, permission) VALUES
    ('admin', 'instance.backup_download')
ON CONFLICT DO NOTHING;
