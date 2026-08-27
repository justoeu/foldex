-- Reverts backup_schedule.config to the four per-job vocabularies. This is
-- LOSSY and cannot be otherwise: the old shapes cannot express what the new
-- one can, so a weekday SET collapses to its FIRST weekday and a multi-time
-- agenda on a job whose legacy shape held one time collapses to its FIRST
-- time. An agenda the old vocabulary cannot express at all — an interval on
-- the dump, drill or user_zip, or wall times on the mirror — has no legacy
-- form to fall back to, so the ROW IS DELETED and the job returns to its env
-- baseline, which is exactly what the old code would do with a row it refuses
-- (it just would not say so).
--
-- Reverting this migration therefore presupposes reverting the CODE as well:
-- the current backend and agent cannot read the legacy shape as their primary
-- form, and the old ones cannot read the unified shape at all.

UPDATE backup_schedule
SET config = jsonb_build_object('interval_min', (config ->> 'interval_min')::int)
WHERE job = 'mirror' AND config ->> 'mode' = 'interval';

UPDATE backup_schedule
SET config = jsonb_build_object('times', config -> 'times')
WHERE job = 'dump' AND config ->> 'mode' = 'times';

UPDATE backup_schedule
SET config = jsonb_build_object(
        'time', config -> 'times' ->> 0,
        'weekday', config -> 'weekdays' ->> 0)
WHERE job = 'drill' AND config ->> 'mode' = 'times'
  AND config -> 'times' ->> 0 IS NOT NULL
  AND config -> 'weekdays' ->> 0 IS NOT NULL;

UPDATE backup_schedule
SET config = jsonb_strip_nulls(jsonb_build_object(
        'enabled', coalesce(config -> 'enabled', 'true'::jsonb),
        'time', to_jsonb(config -> 'times' ->> 0)))
WHERE job = 'user_zip' AND config ->> 'mode' = 'times';

-- Whatever the legacy vocabulary cannot say at all.
DELETE FROM backup_schedule WHERE config ? 'mode';
