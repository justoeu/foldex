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

/** A run nobody has finished yet: still waiting for the agent, or in flight. */
export function isRunInFlight(status: BackupRunStatus): boolean {
  return status === 'requested' || status === 'running'
}

/**
 * A span, in the unit that keeps it readable.
 *
 * The minute arm exists because a drill is minutes long and `(ms / 1000)`
 * alone renders it as `312.4 s` — a number the reader has to divide before it
 * means anything. Sub-second stays in milliseconds: a 489 ms mirror scan
 * rounded to `0.5 s` loses the only digit that distinguishes a scan that ran
 * from one that found nothing.
 */
export function formatDurationMs(ms: number, t: TFunction): string {
  if (!Number.isFinite(ms) || ms < 0) return '—'
  if (ms < 1000) return t('admin.backup_duration_ms', { value: Math.round(ms) })
  if (ms < 60_000) return t('admin.backup_duration_s', { value: (ms / 1000).toFixed(1) })
  const total = Math.floor(ms / 1000)
  return t('admin.backup_duration_m', {
    m: Math.floor(total / 60),
    s: String(total % 60).padStart(2, '0'),
  })
}

export function runDuration(run: BackupRun, t: TFunction): string {
  if (!run.finished_at) return '—'
  return formatDurationMs(Date.parse(run.finished_at) - Date.parse(run.started_at), t)
}

/**
 * How long an unfinished run has been unfinished, or null for a finished one.
 *
 * The two in-flight states count from different instants because they measure
 * different things. `running` counts from `started_at`, which the agent
 * overwrites with `now()` the moment it wins the claim (runstore.go) — that is
 * real work. `requested` counts from `scheduled_for`, the slot the row was
 * filed against: on an unclaimed row `started_at` is only when the web process
 * inserted it, and calling that "processing" would show a duration for a job
 * no process has touched — the exact lie the stale-requested banner exists to
 * catch.
 *
 * `now` is the BROWSER's clock and the timestamps are the SERVER's, so a
 * skewed laptop can put the start in the future; the floor at zero keeps that
 * from rendering a counter that runs backwards.
 */
export function runElapsedMs(run: BackupRun, now: number): number | null {
  if (!isRunInFlight(run.status) || run.finished_at) return null
  const from = Date.parse(run.status === 'requested' ? run.scheduled_for : run.started_at)
  if (!Number.isFinite(from)) return null
  return Math.max(0, now - from)
}

/** Minutes as a compact mono label for the preset chips: 30m, 1h, 6h. */
export function formatMinutes(min: number): string {
  return min < 60 ? `${min}m` : `${min / 60}h`
}

/**
 * Closed set of last_error tokens the agent writes. Unknown strings must not
 * be interpolated into an i18n key — a poisoned row with dots would walk the
 * catalog.
 */
export const KNOWN_BACKUP_ERRORS = new Set([
  'pg_dump_failed',
  'user_zip_failed',
  'encrypt_failed',
  'spool_failed',
  'upload_failed',
  'prune_failed',
  'stale_claim',
  'shutdown',
  'lock_busy',
  'drill_source_failed',
  'drill_no_dump',
  'drill_download_failed',
  'drill_digest_mismatch',
  'drill_decrypt_failed',
  'drill_restore_failed',
  'drill_counts_mismatch',
  'restore_in_flight',
  'mirror_scan_failed',
  'mirror_copy_failed',
])

/**
 * Human copy for a normalized last_error token. The token itself stays on
 * screen as <code> — this is the sentence next to it. Missing keys return
 * null so an unknown reason does not print its i18n key.
 */
export function backupErrorBlurb(t: TFunction, token: string | null): string | null {
  if (!token || !KNOWN_BACKUP_ERRORS.has(token)) return null
  const key = `admin.backup_error_${token}`
  const text = t(key)
  return text === key ? null : text
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


/**
 * The meta counters the details panel renders, in display order.
 *
 * A CLOSED set, like KNOWN_BACKUP_ERRORS and for the same reason: the key is
 * interpolated into an i18n key, and a `meta` document is jsonb — whatever the
 * agent wrote. An open set would let an unexpected key walk the catalog.
 *
 * Excluded on purpose: `duration_ms` (it IS the Duração column),
 * `source_run_id` / `source_artifact_key` / `schema_version` / `tables` (the
 * drill rows already have their own entries above).
 */
const META_ROWS: { key: string; kind: 'count' | 'bytes' | 'size' | 'bool' | 'token' }[] = [
  { key: 'users', kind: 'count' },
  { key: 'shipped', kind: 'count' },
  { key: 'bytes_total', kind: 'bytes' },
  { key: 'objects_skipped', kind: 'count' },
  { key: 'suspicious_keys_skipped', kind: 'count' },
  { key: 'pruned_objects', kind: 'count' },
  { key: 'encrypted', kind: 'bool' },
  // Arrays of user ids: the COUNT is the operational fact, and the ids
  // themselves are nobody's business on an instance-health screen.
  { key: 'failed_users', kind: 'size' },
  { key: 'deferred_users', kind: 'size' },
  { key: 'prune_error', kind: 'token' },
]

export type BackupMetaRow = { key: string; value: string; token: boolean }

/**
 * What a run actually recorded, beyond the columns.
 *
 * Every job but `dump` ships its product in `meta` rather than in
 * `artifact_key` — `user_zip` writes one object per user, `mirror` copies many
 * — so a panel that reads only the columns tells an operator "nenhum contador
 * foi gravado" about a run that recorded three.
 */
export function runMetaRows(meta: Record<string, unknown>, t: TFunction): BackupMetaRow[] {
  const rows: BackupMetaRow[] = []
  for (const { key, kind } of META_ROWS) {
    const raw = meta[key]
    if (raw === undefined || raw === null) continue
    if (kind === 'bool') {
      if (typeof raw !== 'boolean') continue
      rows.push({ key, value: t(raw ? 'common.yes' : 'common.no'), token: false })
      continue
    }
    if (kind === 'token') {
      if (typeof raw !== 'string') continue
      rows.push({ key, value: raw, token: true })
      continue
    }
    if (kind === 'size') {
      if (!Array.isArray(raw)) continue
      rows.push({ key, value: raw.length.toLocaleString(), token: false })
      continue
    }
    if (typeof raw !== 'number') continue
    rows.push({
      key,
      value: kind === 'bytes' ? formatBytes(raw) : raw.toLocaleString(),
      token: false,
    })
  }
  return rows
}
