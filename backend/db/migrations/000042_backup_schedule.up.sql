-- backup_schedule + backup_agent_state: the configurable half of the backup
-- agenda (ADR-44, docs/SDD-OPS-BACKUP.md §5.9). RequiredSchemaVersion bumps to
-- 42 in the SAME change: the backend now reads and writes both tables (the
-- admin schedule surface), and the agent's own gate moves to 42 too — it reads
-- backup_schedule and writes backup_agent_state.
--
-- The division of authority is the design (INV-173): the ENVIRONMENT decides
-- which jobs exist at all (credentials, the age identity), because a DB row
-- cannot conjure a secret into the agent's process; the DATABASE decides when
-- the existing jobs run, because "when" is an operating decision the owner
-- takes from the admin UI. Compiled floors in Go (backupagent.ValidateJobConfig)
-- keep any row — including one written by hand — from lowering protection
-- below the env baseline: a missing row simply means "use the env schedule".
CREATE TABLE backup_schedule (
    job         TEXT PRIMARY KEY CHECK (job IN ('dump','mirror','user_zip','drill')),
    -- Per-job document, validated in ONE place (backupagent.ValidateJobConfig)
    -- by both writers-side (backend PUT) and reader-side (agent load, which
    -- SKIPS an invalid row and logs, falling back to env — a hand-edited row
    -- must degrade to the baseline, never disable the job):
    --   dump:     {"times": ["03:30", ...]}          1..6 daily wall times
    --   drill:    {"time": "01:00", "weekday": "sun"} weekly, never off
    --   mirror:   {"interval_min": 360}               15..1440, never off
    --   user_zip: {"enabled": true, "time": "02:30"}  the one job a row may
    --                                                 disable: it is a product
    --                                                 convenience, not the
    --                                                 instance's protection
    config      JSONB       NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Who changed the agenda, for the band; the audit trail is the durable
    -- record (INV-047) — this column may null out with the account.
    updated_by  BIGINT REFERENCES app_user(id) ON DELETE SET NULL
);

-- The agent's heartbeat: one row, upserted on boot and on every requested-poll
-- tick. It exists for honesty in the UI (the mailer lesson): without it the
-- schedule screen would let an owner agenda a drill on an instance whose agent
-- has no age identity mounted — a schedule that would sit ignored forever
-- while looking configured. capabilities carries per-job booleans only
-- ({"dump":true,"drill":false,...} + the agent version); never credentials.
CREATE TABLE backup_agent_state (
    id           SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    seen_at      TIMESTAMPTZ NOT NULL,
    capabilities JSONB       NOT NULL
);
