import type { BackupAgentJobReport, BackupJob, BackupScheduleConfig } from '../../api/admin'

/**
 * Whether applying `newCfg` over `oldCfg` runs the job LESS than it runs
 * today — the one direction that asks for a confirmation (INV-122): backups
 * are the instance's disaster floor, and thinning them deserves a deliberate
 * second click. Raising frequency never confirms.
 *
 * "Today" is the stored row when one exists; with no row the baseline is what
 * the agent reported it is actually following (its rendered `schedule`
 * string). When neither is known there is nothing to compare against, so no
 * reduction can honestly be claimed.
 *
 * `newCfg === null` is the DELETE — always confirmed, because "back to the
 * env baseline" replaces the visible agenda with one this screen cannot show
 * until the agent's next heartbeat.
 */
export function reducesProtection(
  job: BackupJob,
  oldCfg: BackupScheduleConfig | null,
  newCfg: BackupScheduleConfig | null,
  agent: BackupAgentJobReport | null | undefined,
): boolean {
  if (newCfg === null) return true
  switch (job) {
    case 'dump': {
      const current = oldCfg?.times?.length ?? countAnchors(agent?.schedule)
      return current !== null && (newCfg.times?.length ?? 0) < current
    }
    case 'mirror': {
      const current = oldCfg?.interval_min ?? parseEveryMinutes(agent?.schedule)
      return current !== null && (newCfg.interval_min ?? 0) > current
    }
    case 'user_zip': {
      const currentlyOn =
        oldCfg !== null ? oldCfg.enabled === true : agent ? agent.schedule !== 'disabled' : false
      return currentlyOn && newCfg.enabled === false
    }
    // The drill is weekly by design — a row only moves WHICH slot, never how
    // often, so no edit of it can reduce protection.
    default:
      return false
  }
}

/**
 * How many wall-time anchors the agent's rendered schedule carries
 * ("03:30, 15:30" → 2). null when there is no report to read.
 */
function countAnchors(schedule: string | undefined): number | null {
  if (!schedule) return null
  const m = schedule.match(/\d{1,2}:\d{2}/g)
  return m ? m.length : 0
}

/** The mirror cadence out of the agent's "every 360m" render. */
function parseEveryMinutes(schedule: string | undefined): number | null {
  const m = schedule?.match(/every (\d+)m/)
  return m ? Number(m[1]) : null
}
