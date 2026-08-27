-- backup_schedule.config: one scheduling vocabulary for all four jobs
-- (ADR-44, docs/SDD-OPS-BACKUP.md §5.9). RequiredSchemaVersion bumps to 43 in
-- the SAME change, on BOTH gates (internal/db and backupagent): the backend
-- writes the unified document and the agent reads it.
--
-- The agenda shipped with FOUR vocabularies, one per job — dump {"times":[…]},
-- drill {"time","weekday"} (a single day), mirror {"interval_min"}, user_zip
-- {"enabled","time"} — so no job could pick a SET of weekdays, and the admin
-- form had to grow a different editor per job. The unified document is
--
--   {"mode":"times","times":["03:30","15:30"],"weekdays":["mon","wed","fri"]}
--   {"mode":"interval","interval_min":360}
--   {"mode":"times","enabled":false}          -- user_zip only
--
-- with "mode" EXPLICIT: a row carrying both times and interval_min would
-- otherwise be half-honoured in silence. What differs between jobs is now the
-- FLOORS, not the vocabulary — compiled in backupagent.ValidateJobConfig
-- (INV-173), where the dump keeps a higher weekday floor than the rest because
-- it is the instance's disaster floor, not a product convenience.
--
-- The statements below rewrite the stored rows exactly as the Go-side
-- JobConfig.normalized does. Each is guarded on the row NOT already carrying
-- "mode", so the migration is idempotent and an already-unified row is left
-- alone. A legacy row that survives this migration unrewritten is still read
-- correctly: normalized runs on every load.

-- {"interval_min": N} — the mirror's shape.
UPDATE backup_schedule
SET config = jsonb_build_object('mode', 'interval', 'interval_min', (config ->> 'interval_min')::int)
WHERE NOT (config ? 'mode') AND config ? 'interval_min';

-- {"times": [...]} — the dump's shape. Days were implicit: every day.
UPDATE backup_schedule
SET config = jsonb_build_object(
        'mode', 'times',
        'times', config -> 'times',
        'weekdays', '["sun","mon","tue","wed","thu","fri","sat"]'::jsonb)
WHERE NOT (config ? 'mode') AND config ? 'times';

-- {"time": "HH:MM", "weekday": "sun"} — the drill's shape.
UPDATE backup_schedule
SET config = jsonb_build_object(
        'mode', 'times',
        'times', jsonb_build_array(config ->> 'time'),
        'weekdays', jsonb_build_array(lower(config ->> 'weekday')))
WHERE NOT (config ? 'mode') AND config ? 'time' AND config ? 'weekday';

-- {"enabled": true, "time": "HH:MM"} — user_zip's enabled shape.
UPDATE backup_schedule
SET config = jsonb_build_object(
        'mode', 'times',
        'enabled', config -> 'enabled',
        'times', jsonb_build_array(config ->> 'time'),
        'weekdays', '["sun","mon","tue","wed","thu","fri","sat"]'::jsonb)
WHERE NOT (config ? 'mode') AND config ? 'time' AND config ? 'enabled';

-- {"time": "HH:MM"} with neither weekday nor enabled — a hand-written relic.
UPDATE backup_schedule
SET config = jsonb_build_object(
        'mode', 'times',
        'times', jsonb_build_array(config ->> 'time'),
        'weekdays', '["sun","mon","tue","wed","thu","fri","sat"]'::jsonb)
WHERE NOT (config ? 'mode') AND config ? 'time';

-- {"enabled": false} — user_zip switched off, no agenda to carry.
UPDATE backup_schedule
SET config = jsonb_build_object('mode', 'times', 'enabled', config -> 'enabled')
WHERE NOT (config ? 'mode') AND config ? 'enabled';
