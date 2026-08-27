import type { BackupScheduleConfig } from '../../api/admin'

const MINUTES_PER_WEEK = 7 * 24 * 60

/**
 * How many times a config fires in a week — the one number that makes the two
 * modes comparable, and therefore the only honest answer to "does this run the
 * job less than it runs today?".
 *
 * `NaN` is a real answer, not a failure: a config with no mode (the `{}` an
 * agent reports for a job whose env agenda is off), or a mode whose own field
 * is missing, states no cadence at all. Every comparison below propagates it
 * as "nothing can be claimed" rather than guessing zero.
 */
export function firingsPerWeek(cfg: BackupScheduleConfig): number {
  if (cfg.enabled === false) return 0
  if (cfg.mode === 'interval') return MINUTES_PER_WEEK / (cfg.interval_min as number)
  if (cfg.mode === 'times') return (cfg.times?.length as number) * (cfg.weekdays?.length as number)
  return NaN
}

/**
 * Whether applying `next` runs the job LESS than it runs today — the one
 * direction that asks for a confirmation (INV-122): backups are the instance's
 * disaster floor, and thinning them deserves a deliberate second click.
 * Raising the frequency never confirms.
 *
 * "Today" is the stored row when one exists; with no row it is the ENV
 * baseline the agent publishes (INV-173: absent row = env baseline). When
 * neither states a cadence, no reduction can honestly be claimed.
 *
 * `next === null` is the DELETE — always confirmed, because "back to the env
 * baseline" replaces the visible agenda with one this screen cannot show until
 * the agent's next heartbeat.
 */
export function reducesProtection(
  stored: BackupScheduleConfig | null,
  next: BackupScheduleConfig | null,
  baseline: BackupScheduleConfig | null | undefined,
): boolean {
  if (next === null) return true
  const current = stored ?? baseline
  if (!current) return false
  const before = firingsPerWeek(current)
  const after = firingsPerWeek(next)
  if (Number.isNaN(before) || Number.isNaN(after)) return false
  return after < before
}
