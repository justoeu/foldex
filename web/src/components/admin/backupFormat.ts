import type { TFunction } from 'i18next'
import type { BackupRun, BackupRunStatus } from '../../api/admin'

/**
 * Pure presentation helpers for the instance-backup screen. They live outside
 * the component so they can be read — and tested — without a render.
 */

export function statusTone(status: BackupRunStatus): string {
  if (status === 'succeeded') return ' fx-chip-ok'
  if (status === 'failed') return ' fx-chip-danger'
  return ' fx-chip-warn'
}

export function drillTables(meta: Record<string, unknown>): [string, number][] {
  const tables = meta['tables']
  if (tables === null || typeof tables !== 'object' || Array.isArray(tables)) return []
  return Object.entries(tables as Record<string, unknown>).filter(
    (e): e is [string, number] => typeof e[1] === 'number',
  )
}

export function drillTableCount(meta: Record<string, unknown>): number {
  return drillTables(meta).length
}

export function runDuration(run: BackupRun, t: TFunction): string {
  if (!run.finished_at) return '—'
  const ms = Date.parse(run.finished_at) - Date.parse(run.started_at)
  if (!Number.isFinite(ms) || ms < 0) return '—'
  if (ms < 1000) return t('admin.backup_duration_ms', { value: ms })
  return t('admin.backup_duration_s', { value: (ms / 1000).toFixed(1) })
}

/** Minutes as a compact mono label for the preset chips: 30m, 1h, 6h. */
export function formatMinutes(min: number): string {
  return min < 60 ? `${min}m` : `${min / 60}h`
}

export function formatBytes(b: number): string {
  if (b < 1024) return `${b} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let n = b / 1024
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(n >= 10 ? 0 : 1)} ${units[i]}`
}
