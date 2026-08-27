-- backup_schedule.config: one scheduling vocabulary for all four jobs
-- (ADR-45, docs/SDD-OPS-BACKUP.md §5.9). RequiredSchemaVersion bumps to 43 in
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
-- The statements below rewrite the stored rows as the Go-side
-- JobConfig.normalized does, shape for shape, and the two are tested against
-- each other row by row (TestMigration_AgreesWithNormalizedOnEveryLegacyShape).
-- Three of the details below exist only because the two sides once disagreed,
-- and each disagreement changed an agenda nobody asked to change:
--
--   * "enabled" is stripped from every job but user_zip, because only user_zip
--     may carry it. Left on a dump row, the rewrite would produce a document
--     that HAS a mode — so normalized returns it untouched — and that
--     ValidateJobConfig then refuses forever, pinning the job to the env
--     baseline behind nothing louder than a Warn.
--   * a DISABLED user_zip loses its agenda. The previous validator accepted
--     {"enabled":false,"time":"02:30"}; the unified one refuses "switched off"
--     and an agenda together, so carrying the times across would invalidate
--     the row, fall user_zip back to the env baseline and START IT RUNNING
--     AGAIN — reversing the one thing the operator explicitly asked for.
--   * every other branch carries "enabled" THROUGH. A branch that rebuilds the
--     object without it turns a user_zip the owner switched off back on, which
--     is the same defect as the one above arriving by the opposite road.
--
-- Each statement is guarded on the row NOT already carrying "mode", so the
-- migration is idempotent and an already-unified row is left alone. A legacy
-- row that survives this migration unrewritten is still read correctly:
-- normalized runs on every load.
--
-- One shape is deliberately NOT rewritten: a document carrying none of the
-- legacy keys ({}, or hand-written junk). It says nothing the unified
-- vocabulary could express, so there is nothing to translate; normalized turns
-- it into a bare {"mode":"times"} that the floors refuse, which is the same
-- outcome the untouched row gets — the job runs on the env baseline either
-- way, and the original document survives for whoever has to explain it.

-- "enabled" is user_zip's alone. Strip it before anything reads it as part of
-- a shape.
UPDATE backup_schedule
SET config = config - 'enabled'
WHERE NOT (config ? 'mode') AND job <> 'user_zip' AND config ? 'enabled';

-- user_zip switched off: no agenda travels with it, whatever the row carried.
UPDATE backup_schedule
SET config = jsonb_build_object('mode', 'times', 'enabled', false)
WHERE NOT (config ? 'mode') AND job = 'user_zip' AND config -> 'enabled' = 'false'::jsonb;

-- {"interval_min": N} — the mirror's shape.
UPDATE backup_schedule
SET config = jsonb_strip_nulls(jsonb_build_object(
        'mode', 'interval',
        'enabled', config -> 'enabled',
        'interval_min', (config ->> 'interval_min')::int))
WHERE NOT (config ? 'mode') AND config ? 'interval_min';

-- {"times": [...]} — the dump's shape. Days were implicit: every day.
UPDATE backup_schedule
SET config = jsonb_strip_nulls(jsonb_build_object(
        'mode', 'times',
        'enabled', config -> 'enabled',
        'times', config -> 'times',
        'weekdays', '["sun","mon","tue","wed","thu","fri","sat"]'::jsonb))
WHERE NOT (config ? 'mode') AND config ? 'times';

-- {"time": "HH:MM", "weekday": "sun"} — the drill's shape.
UPDATE backup_schedule
SET config = jsonb_strip_nulls(jsonb_build_object(
        'mode', 'times',
        'enabled', config -> 'enabled',
        'times', jsonb_build_array(config ->> 'time'),
        'weekdays', jsonb_build_array(lower(config ->> 'weekday'))))
WHERE NOT (config ? 'mode') AND config ? 'time' AND config ? 'weekday';

-- {"time": "HH:MM"} with no weekday — user_zip's enabled shape, and the
-- hand-written relic that carries neither weekday nor enabled.
UPDATE backup_schedule
SET config = jsonb_strip_nulls(jsonb_build_object(
        'mode', 'times',
        'enabled', config -> 'enabled',
        'times', jsonb_build_array(config ->> 'time'),
        'weekdays', '["sun","mon","tue","wed","thu","fri","sat"]'::jsonb))
WHERE NOT (config ? 'mode') AND config ? 'time';

-- {"enabled": true} with no agenda at all — user_zip only by now, and refused
-- by the floors exactly as normalized's output is: enabled means an agenda.
UPDATE backup_schedule
SET config = jsonb_build_object('mode', 'times', 'enabled', config -> 'enabled')
WHERE NOT (config ? 'mode') AND config ? 'enabled';
