-- backup_run: state and history of the operational backup agent (ADR-43,
-- docs/SDD-OPS-BACKUP.md §3). Written by cmd/backup-agent; the backend only
-- READS it (admin status surface, PR5) — which is why RequiredSchemaVersion
-- does NOT bump here: the rule is "bump when the backend's Go code reads or
-- writes something new", and in this PR it does neither. The agent runs its
-- own boot gate against schema_migrations instead.
--
-- Modeled on mail_outbox (000034) with deliberate differences: a failed run is
-- a ROW, not a state that evolves — the retry policy is "the next scheduled
-- slot", so there is no attempts/next_attempt_at pair. History is the product:
-- the admin UI and Grafana read the series, not just the latest state.
CREATE TABLE backup_run (
    id              BIGSERIAL PRIMARY KEY,
    job             TEXT        NOT NULL CHECK (job IN ('dump','mirror','user_zip','drill')),
    -- 'requested' is the manual-trigger channel: the backend INSERTs it, the
    -- agent claims it with a conditional UPDATE (CAS on status). The web
    -- process never runs a backup and never holds the S3 credentials.
    status          TEXT        NOT NULL DEFAULT 'running'
                      CHECK (status IN ('requested','running','succeeded','failed')),
    claim_token     UUID,                     -- agent-instance identity; NULL only while requested
    scheduled_for   TIMESTAMPTZ NOT NULL,     -- the slot this run satisfies (catch-up records the missed slot)
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at     TIMESTAMPTZ,
    artifact_key    TEXT,                     -- S3 object key (dump/user_zip); NULL for mirror/drill
    artifact_bytes  BIGINT,
    artifact_sha256 TEXT,                     -- sha256 of the CIPHERTEXT: verifiable against the bucket without decrypting
    objects_scanned BIGINT,                   -- mirror counters
    objects_copied  BIGINT,
    bytes_copied    BIGINT,
    drill_of_run_id BIGINT REFERENCES backup_run(id),  -- which dump run this drill validated
    -- Normalized reason token (pg_dump_failed, upload_failed, stale_claim, …),
    -- never raw tool output: pg_dump stderr can carry a DSN, and this column is
    -- rendered by the admin UI and mailed in alerts.
    last_error      TEXT,
    meta            JSONB       NOT NULL DEFAULT '{}'::jsonb  -- tool versions, per-phase durations, table counts
);

CREATE INDEX backup_run_job_started_idx ON backup_run (job, started_at DESC);

-- Mutual exclusion at the persistence layer: two agents (a deploy mistake)
-- cannot both record the same job — the second INSERT/claim fails and skips
-- the slot. The execution-time mutex is a pg advisory lock
-- (backup.InstanceBackupAdvisoryLockKey); each mechanism covers the other's
-- failure mode, and the janitor expires stale 'running' rows so a dead agent
-- cannot hold this index forever.
CREATE UNIQUE INDEX backup_run_one_running_idx ON backup_run (job) WHERE status = 'running';

-- The requested-claim poll runs every ~30s per job forever, and the common
-- case is zero requested rows; without a matching partial index that poll
-- heap-filters the job's whole ever-growing history on every tick. This
-- index holds only the (normally empty) requested set — an O(1) probe.
CREATE INDEX backup_run_requested_idx ON backup_run (job, id) WHERE status = 'requested';
