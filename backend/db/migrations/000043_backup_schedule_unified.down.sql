-- Reverts backup_schedule.config to the four per-job vocabularies. This is
-- LOSSY and cannot be otherwise: the old shapes cannot express what the new
-- one can. The drill collapses to its FIRST weekday and FIRST time; user_zip
-- collapses to its FIRST time; the dump DROPS its weekday set entirely,
-- because the legacy dump shape had no weekdays and meant "every day" — the
-- one lossy direction here that RAISES frequency rather than lowering it.
-- An agenda the old vocabulary cannot express at all — an interval on
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
WHERE job = 'mirror' AND config ->> 'mode' = 'interval'
  -- Same guard as the up, with the same nuance: ->> unquotes, so "360" would
  -- cast fine — what aborts the whole revert is a NON-NUMERIC value. Skipped
  -- here means the row falls to the DELETE below, which is the honest answer
  -- for a document the legacy vocabulary cannot state.
  AND jsonb_typeof(config -> 'interval_min') = 'number';

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
