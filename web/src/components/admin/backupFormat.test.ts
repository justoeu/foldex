import { describe, it, expect } from 'vitest'
import type { TFunction } from 'i18next'
import type { BackupRun, BackupRunStatus } from '../../api/admin'
import {
  backupErrorBlurb,
  formatBytes,
  formatDurationMs,
  formatMinutes,
  drillTables,
  drillTableCount,
  isRunInFlight,
  runDuration,
  runElapsedMs,
  runMetaRows,
  statusTone,
} from './backupFormat'

/** Interpolates the i18n key so a test can assert on the unit that was chosen. */
const tt = ((key: string, vars?: Record<string, unknown>) =>
  `${key}:${JSON.stringify(vars ?? {})}`) as unknown as TFunction

function run(over: Partial<BackupRun> & { status: BackupRunStatus }): BackupRun {
  return {
    id: 1,
    job: 'dump',
    scheduled_for: '2026-09-09T12:00:00Z',
    started_at: '2026-09-09T12:00:00Z',
    finished_at: null,
    artifact_key: null,
    artifact_bytes: null,
    artifact_sha256: null,
    objects_scanned: null,
    objects_copied: null,
    bytes_copied: null,
    drill_of_run_id: null,
    last_error: null,
    meta: {},
    ...over,
  }
}

describe('backupErrorBlurb', () => {
  const t = ((key: string) => (
    key === 'admin.backup_error_upload_failed' ? 'The artifact could not be uploaded.' : key
  )) as TFunction

  it('returns the sentence for a known token and nothing for an unknown one', () => {
    expect(backupErrorBlurb(t, 'upload_failed')).toBe('The artifact could not be uploaded.')
    expect(backupErrorBlurb(t, 'not_a_reason')).toBeNull()
    expect(backupErrorBlurb(t, 'upload.failed')).toBeNull()
    expect(backupErrorBlurb(t, null)).toBeNull()
  })
})

describe('formatBytes', () => {
  it('keeps raw bytes below a kilobyte', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1023)).toBe('1023 B')
  })

  // One decimal under ten, none above: "1.3 MB" is useful, "1.3 KB" beside
  // "947 KB" is not — the precision follows the magnitude.
  it('scales through the units and drops the decimal past ten', () => {
    expect(formatBytes(1024)).toBe('1.0 KB')
    expect(formatBytes(134_000)).toBe('131 KB')
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(formatBytes(42 * 1024 * 1024)).toBe('42 MB')
    expect(formatBytes(3 * 1024 ** 4)).toBe('3.0 TB')
  })

  it('stops at terabytes instead of inventing a larger unit', () => {
    expect(formatBytes(9999 * 1024 ** 4)).toBe('9999 TB')
  })
})

describe('formatMinutes', () => {
  it('reads as minutes below an hour and as hours above it', () => {
    expect(formatMinutes(30)).toBe('30m')
    expect(formatMinutes(60)).toBe('1h')
    expect(formatMinutes(720)).toBe('12h')
  })
})

/*
 * The drill's counts come from a jsonb column, so the shape is whatever the
 * agent wrote. Anything that is not a number is dropped rather than rendered:
 * a chip reading "link: undefined" would claim a comparison that never ran.
 */
describe('drillTables', () => {
  it('keeps only numeric entries', () => {
    expect(drillTables({ tables: { link: 42, note: 'seven', tag: 11 } })).toEqual([
      ['link', 42],
      ['tag', 11],
    ])
  })

  it('returns nothing for missing, null or array-shaped meta', () => {
    expect(drillTables({})).toEqual([])
    expect(drillTables({ tables: null })).toEqual([])
    expect(drillTables({ tables: ['link', 42] })).toEqual([])
    expect(drillTableCount({ tables: { link: 1, note: 2 } })).toBe(2)
    expect(drillTableCount({})).toBe(0)
  })
})

describe('statusTone', () => {
  it('gives every non-terminal status the warning tone', () => {
    expect(statusTone('succeeded')).toBe(' fx-chip-ok')
    expect(statusTone('failed')).toBe(' fx-chip-danger')
    expect(statusTone('running')).toBe(' fx-chip-warn')
    expect(statusTone('requested')).toBe(' fx-chip-warn')
  })
})


describe('formatDurationMs', () => {
  it('keeps sub-second spans in milliseconds', () => {
    // 489 ms rounded to "0.5 s" loses the only digit that separates a mirror
    // scan that ran from one that found nothing.
    expect(formatDurationMs(489, tt)).toBe('admin.backup_duration_ms:{"value":489}')
    expect(formatDurationMs(999.6, tt)).toBe('admin.backup_duration_ms:{"value":1000}')
  })

  it('reads as seconds up to a minute', () => {
    expect(formatDurationMs(1000, tt)).toBe('admin.backup_duration_s:{"value":"1.0"}')
    expect(formatDurationMs(8_900, tt)).toBe('admin.backup_duration_s:{"value":"8.9"}')
    expect(formatDurationMs(59_999, tt)).toBe('admin.backup_duration_s:{"value":"60.0"}')
  })

  it('switches to minutes past sixty seconds, zero-padding the remainder', () => {
    // The whole reason the arm exists: a five-minute drill as "312.4 s" is a
    // number the reader has to divide before it means anything.
    expect(formatDurationMs(60_000, tt)).toBe('admin.backup_duration_m:{"m":1,"s":"00"}')
    expect(formatDurationMs(312_400, tt)).toBe('admin.backup_duration_m:{"m":5,"s":"12"}')
    expect(formatDurationMs(3_609_000, tt)).toBe('admin.backup_duration_m:{"m":60,"s":"09"}')
  })

  it('refuses a negative or non-finite span instead of rendering one', () => {
    expect(formatDurationMs(-1, tt)).toBe('—')
    expect(formatDurationMs(Number.NaN, tt)).toBe('—')
  })
})

describe('isRunInFlight', () => {
  it('counts the two states nobody has finished', () => {
    expect(isRunInFlight('requested')).toBe(true)
    expect(isRunInFlight('running')).toBe(true)
    expect(isRunInFlight('succeeded')).toBe(false)
    expect(isRunInFlight('failed')).toBe(false)
  })
})

describe('runDuration', () => {
  it('measures a finished run between its own two stamps', () => {
    const r = run({
      status: 'succeeded',
      started_at: '2026-09-09T12:00:00Z',
      finished_at: '2026-09-09T12:00:08.900Z',
    })
    expect(runDuration(r, tt)).toBe('admin.backup_duration_s:{"value":"8.9"}')
  })

  it('has nothing to measure while the run is unfinished', () => {
    expect(runDuration(run({ status: 'running' }), tt)).toBe('—')
  })

  it('refuses a finish that precedes the start', () => {
    const r = run({
      status: 'succeeded',
      started_at: '2026-09-09T12:00:08Z',
      finished_at: '2026-09-09T12:00:00Z',
    })
    expect(runDuration(r, tt)).toBe('—')
  })
})

describe('runElapsedMs', () => {
  const now = Date.parse('2026-09-09T12:05:00Z')

  it('counts a running row from the instant the agent claimed it', () => {
    // started_at is overwritten with now() by the claiming UPDATE, so it is
    // real work — not when the row was filed.
    const r = run({ status: 'running', started_at: '2026-09-09T12:04:00Z' })
    expect(runElapsedMs(r, now)).toBe(60_000)
  })

  it('counts a requested row from the slot it was filed against', () => {
    // started_at on an unclaimed row is only the insert; counting from it
    // would show "processing" for a job no process has touched.
    const r = run({
      status: 'requested',
      scheduled_for: '2026-09-09T12:00:00Z',
      started_at: '2026-09-09T12:03:00Z',
    })
    expect(runElapsedMs(r, now)).toBe(300_000)
  })

  it('has no elapsed for a finished run, whatever its stamps say', () => {
    expect(runElapsedMs(run({ status: 'succeeded' }), now)).toBeNull()
    expect(runElapsedMs(run({ status: 'failed' }), now)).toBeNull()
  })

  it('has no elapsed for an in-flight status that already carries a finish', () => {
    // The agent's terminal UPDATE is a CAS; a row seen mid-transition must not
    // start counting again from a start that is already over.
    const r = run({ status: 'running', finished_at: '2026-09-09T12:04:30Z' })
    expect(runElapsedMs(r, now)).toBeNull()
  })

  it('floors a browser clock that trails the server instead of counting backwards', () => {
    const r = run({ status: 'running', started_at: '2026-09-09T12:06:00Z' })
    expect(runElapsedMs(r, now)).toBe(0)
  })

  it('refuses an unparseable stamp', () => {
    expect(runElapsedMs(run({ status: 'running', started_at: 'never' }), now)).toBeNull()
  })
})


describe('runMetaRows', () => {
  /*
   * The row from the incident: user_zip reported succeeded in 3.5 s with an
   * empty artifact_key — it ships one object per user, so its product lives
   * entirely in meta — and the panel, which read only the columns, told the
   * operator no counters had been recorded. Three had.
   */
  it('reads the counters a user_zip run actually recorded', () => {
    const rows = runMetaRows(
      { users: 1, shipped: 1, bytes_total: 489_750, duration_ms: 3387 },
      tt,
    )
    expect(rows).toEqual([
      { key: 'users', value: '1', token: false },
      { key: 'shipped', value: '1', token: false },
      { key: 'bytes_total', value: '478 KB', token: false },
    ])
  })

  // duration_ms IS the Duração column; repeating it as a counter would read as
  // a second, different measurement.
  it('never repeats a value the panel already renders', () => {
    const rows = runMetaRows(
      { duration_ms: 3387, source_run_id: 53, schema_version: 43, tables: { link: 3 } },
      tt,
    )
    expect(rows).toEqual([])
  })

  it('renders an array of users as its COUNT, never the ids', () => {
    const rows = runMetaRows({ failed_users: [7, 9], deferred_users: [3] }, tt)
    expect(rows).toEqual([
      { key: 'failed_users', value: '2', token: false },
      { key: 'deferred_users', value: '1', token: false },
    ])
    // The ids are nobody's business on an instance-health screen.
    expect(JSON.stringify(rows)).not.toContain('7')
  })

  it('keeps a normalized reason as a token so it stays <code>', () => {
    const rows = runMetaRows({ prune_error: 'prune_failed' }, tt)
    expect(rows).toEqual([{ key: 'prune_error', value: 'prune_failed', token: true }])
  })

  it('renders a boolean as words, not as "true"', () => {
    expect(runMetaRows({ encrypted: true }, tt)).toEqual([
      { key: 'encrypted', value: 'common.yes:{}', token: false },
    ])
    expect(runMetaRows({ encrypted: false }, tt)).toEqual([
      { key: 'encrypted', value: 'common.no:{}', token: false },
    ])
  })

  /*
   * meta is jsonb — whatever the agent wrote — and the key is interpolated
   * into an i18n key. An open set would let an unexpected key walk the
   * catalog, and a value of the wrong TYPE would render "[object Object]"
   * beside a label that promises a number.
   */
  it('ignores unknown keys and values of the wrong shape', () => {
    const rows = runMetaRows(
      {
        'admin.backup_col_when': 'poisoned',
        not_a_counter: 5,
        users: 'many',
        bytes_total: { nested: 1 },
        encrypted: 'yes',
        failed_users: 3,
        prune_error: 42,
        shipped: 2,
      },
      tt,
    )
    expect(rows).toEqual([{ key: 'shipped', value: '2', token: false }])
  })

  it('holds the display order regardless of the order meta was written in', () => {
    const rows = runMetaRows({ bytes_total: 1024, shipped: 2, users: 3 }, tt)
    expect(rows.map((r) => r.key)).toEqual(['users', 'shipped', 'bytes_total'])
  })
})
