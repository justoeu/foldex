import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { useConfirm } from '../ConfirmDialog'
import { useCopy } from '../../hooks/useCopy'
import { useCurrentUser } from '../../auth/AuthProvider'
import { relativeTime } from '../../lib/time'
import {
  backupScheduleQueryKey,
  backupStatusQueryKey,
  fetchBackupSchedule,
  fetchBackupStatus,
  requestBackupRun,
  resetBackupSchedule,
  saveBackupSchedule,
  type BackupAgentJobReport,
  type BackupJob,
  type BackupJobStatus,
  type BackupRun,
  type BackupScheduleConfig,
  type BackupScheduleResponse,
  type BackupScheduleRow,
} from '../../api/admin'
import { reducesProtection } from './backupSchedule'
import { apiErrorCode, apiErrorMessage } from '../../lib/apiError'

/**
 * The dump freshness contract, mirrored from the shipped alert rule
 * (prometheus-alerts.yml: daily cadence + grace = 26 h). This constant is the
 * ONLY piece of server policy the client re-states — everything else on this
 * band renders exactly what the server answered (INV-138 by analogy).
 */
const DUMP_STALE_MS = 26 * 60 * 60 * 1000

/**
 * A 'requested' row the agent has not claimed within 5 minutes. The claim poll
 * runs every ~30 s, so anything past this is an agent that is not running —
 * the mailer incident's shape, and the one state that must not look pending.
 */
const REQUESTED_STALE_MS = 5 * 60 * 1000

/**
 * The heartbeat is written on the agent's requested-poll cadence
 * (BACKUP_REQUESTED_POLL_SEC, default 30 s), so a stamp older than four
 * default ticks means the agenda on screen describes a process that is not
 * running. An operator who raises the poll past this threshold trades a
 * false stale banner for their slower cadence — accepted; the query below
 * refetches on the same rhythm so the comparison is against fresh data, not
 * a cached seen_at going stale in the tab.
 */
const AGENT_STALE_MS = 2 * 60 * 1000
const SCHEDULE_REFETCH_MS = 60 * 1000

/**
 * The instance-wide backup status band (ADR-43, SDD-OPS-BACKUP §10.2).
 *
 * Read-only over backup_run except for one verb: "run now" enqueues a
 * requested row for the agent to claim. The S3 credentials, the schedule and
 * the execution never touch the web process, so the only feedback a trigger
 * has is the new row in the history below.
 */
export function BackupSection() {
  const { t } = useTranslation()
  const confirm = useConfirm()
  const queryClient = useQueryClient()
  const [pages, setPages] = useState<number[]>([])

  const before = pages.length > 0 ? pages[pages.length - 1] : 0
  const query = useQuery({
    queryKey: backupStatusQueryKey(before),
    queryFn: () => fetchBackupStatus(before > 0 ? { before } : {}),
  })

  const trigger = useMutation({
    mutationFn: requestBackupRun,
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['admin', 'backup'] }),
  })

  async function runNow(job: BackupJob) {
    trigger.reset()
    const ok = await confirm({
      title: t('admin.backup_run_confirm_title', { job: t(`admin.backup_job_${job}`) }),
      message: t('admin.backup_run_confirm_message'),
      confirmLabel: t('admin.backup_run_confirm_action'),
    })
    if (ok) trigger.mutate(job)
  }

  if (query.isPending) {
    return <div className="fx-card"><div className="fx-card-body"><div className="fx-empty">{t('common.loading')}</div></div></div>
  }
  if (query.isError || !query.data) {
    return <div className="fx-card"><div className="fx-card-body"><div className="fx-empty">{t('admin.backup_unavailable')}</div></div></div>
  }

  const jobs = query.data.jobs
  const runs = query.data.runs
  const neverRan =
    runs.length === 0 &&
    pages.length === 0 &&
    jobs.every((j) => j.last_success === null && j.consecutive_failures === 0)
  const staleRequested = runs.filter(
    (r) => r.status === 'requested' && Date.now() - Date.parse(r.scheduled_for) > REQUESTED_STALE_MS,
  )
  const drill = jobs.find((j) => j.job === 'drill')?.last_success ?? null
  const triggerError = trigger.isError
    ? (apiErrorCode(trigger.error) ?? t('admin.backup_trigger_failed'))
    : null

  return (
    <div className="fx-hub-stack">
      {neverRan && (
        <div className="fx-banner fx-banner-warn">
          <div>
            <div className="fx-banner-title">{t('admin.backup_inactive_title')}</div>
            <div className="fx-banner-desc">
              {t('admin.backup_inactive_desc')} <code>COMPOSE_PROFILES=backup</code>
            </div>
          </div>
        </div>
      )}
      {staleRequested.length > 0 && (
        <div className="fx-banner fx-banner-warn">
          <div>
            <div className="fx-banner-title">{t('admin.backup_requested_stale_title')}</div>
            <div className="fx-banner-desc">
              {t('admin.backup_requested_stale_desc', {
                jobs: staleRequested.map((r) => t(`admin.backup_job_${r.job}`)).join(', '),
              })}
            </div>
          </div>
        </div>
      )}
      {triggerError && (
        <div className="fx-banner fx-banner-warn">
          <div>
            <div className="fx-banner-title">{t('admin.backup_trigger_failed')}</div>
            {/* The code, verbatim: `backup_run_pending` is what the runbook
                and the agent's logs call it, and a translation would unlink
                the two. */}
            <div className="fx-banner-desc"><code>{triggerError}</code></div>
          </div>
        </div>
      )}

      <div className="fx-card">
        <div className="fx-card-body">
          <div className="fx-utable-wrap">
            <table className="fx-utable fx-bkp-table">
              <thead>
                <tr>
                  <th>{t('admin.backup_col_job')}</th>
                  <th>{t('admin.backup_col_last_success')}</th>
                  <th>{t('admin.backup_col_artifact')}</th>
                  <th>{t('admin.backup_col_size')}</th>
                  <th>{t('admin.backup_col_sha')}</th>
                  <th>{t('admin.backup_col_duration')}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {jobs.map((j) => (
                  <JobRow key={j.job} status={j} onRun={() => void runNow(j.job)} />
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <ScheduleSection />

      {drill && (
        <div className="fx-panel">
          <div className="fx-panel-head">
            <div>
              <div className="fx-panel-title">{t('admin.backup_drill_title')}</div>
              <div className="fx-panel-desc">
                {drill.drill_of_run_id !== null
                  ? t('admin.backup_drill_desc', {
                      run: drill.drill_of_run_id,
                      when: relativeTime(drill.started_at, t),
                    })
                  : relativeTime(drill.started_at, t)}
              </div>
            </div>
          </div>
          <DrillCounts meta={drill.meta} />
        </div>
      )}

      <div className="fx-card">
        <div className="fx-card-body" style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div className="fx-panel-title">{t('admin.backup_history_title')}</div>
          {runs.length === 0 && <div className="fx-empty">{t('admin.backup_history_empty')}</div>}
          {runs.length > 0 && (
            <div className="fx-utable-wrap">
              <table className="fx-utable fx-bkp-table">
                <thead>
                  <tr>
                    <th>{t('admin.backup_col_when')}</th>
                    <th>{t('admin.backup_col_job')}</th>
                    <th>{t('admin.backup_col_status')}</th>
                    <th>{t('admin.backup_col_artifact')}</th>
                    <th>{t('admin.backup_col_error')}</th>
                  </tr>
                </thead>
                <tbody>
                  {runs.map((r) => (
                    <HistoryRow key={r.id} run={r} />
                  ))}
                </tbody>
              </table>
            </div>
          )}
          <div style={{ display: 'flex', gap: 8 }}>
            {pages.length > 0 && (
              <button className="fx-pillbtn" onClick={() => setPages((p) => p.slice(0, -1))}>
                {t('admin.audit_prev')}
              </button>
            )}
            {runs.length > 0 && (
              <button
                className="fx-pillbtn"
                onClick={() => {
                  const last = runs[runs.length - 1]
                  if (last) setPages((p) => [...p, last.id])
                }}
              >
                {t('admin.audit_next')}
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

/**
 * The configurable agenda (ADR-44) as one sub-band: what the agent is
 * ACTUALLY following (its heartbeat is the truth), which layer each agenda
 * comes from (env baseline vs a stored row), and — for the owner only — the
 * editors that write the rows. The permission split mirrors the server:
 * reading rides `instance.backup`, writing is `instance.backup_schedule`,
 * owner-only and locked, so a non-owner sees no controls at all.
 */
function ScheduleSection() {
  const { t } = useTranslation()
  const isOwner = useCurrentUser()?.role === 'owner'
  const query = useQuery({
    queryKey: backupScheduleQueryKey,
    queryFn: fetchBackupSchedule,
    refetchInterval: SCHEDULE_REFETCH_MS,
  })

  if (query.isPending) {
    return <div className="fx-card"><div className="fx-card-body"><div className="fx-empty">{t('common.loading')}</div></div></div>
  }
  if (query.isError || !query.data) {
    return <div className="fx-card"><div className="fx-card-body"><div className="fx-empty">{t('admin.backup_schedule_unavailable')}</div></div></div>
  }

  const data = query.data
  const agent = data.agent
  const agentStale = agent !== null && Date.now() - Date.parse(agent.seen_at) > AGENT_STALE_MS

  return (
    <div className="fx-card">
      <div className="fx-card-body" style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <div>
          <div className="fx-panel-title">{t('admin.backup_schedule_title')}</div>
          <div className="fx-panel-desc">
            {agent !== null
              ? t('admin.backup_agent_seen', {
                  when: relativeTime(agent.seen_at, t),
                  version: agent.version,
                })
              : t('admin.backup_schedule_desc')}
          </div>
        </div>

        {agent === null && (
          <div className="fx-banner fx-banner-warn">
            <div>
              <div className="fx-banner-title">{t('admin.backup_agent_never_title')}</div>
              <div className="fx-banner-desc">
                {t('admin.backup_agent_never_desc')} <code>COMPOSE_PROFILES=backup</code>
              </div>
            </div>
          </div>
        )}
        {agentStale && (
          <div className="fx-banner fx-banner-warn">
            <div>
              <div className="fx-banner-title">{t('admin.backup_agent_stale_title')}</div>
              <div className="fx-banner-desc">{t('admin.backup_agent_stale_desc')}</div>
            </div>
          </div>
        )}

        {data.jobs.map((job) => (
          <ScheduleJobRow
            // A saved or reset row remounts the editor so its draft reseeds
            // from what the server now holds, instead of a stale local copy.
            key={`${job}:${data.rows[job]?.updated_at ?? 'baseline'}`}
            job={job}
            row={data.rows[job] ?? null}
            report={agent?.jobs[job] ?? null}
            agentSeen={agent !== null}
            bounds={data.bounds}
            isOwner={isOwner}
          />
        ))}
      </div>
    </div>
  )
}

function ScheduleJobRow({
  job,
  row,
  report,
  agentSeen,
  bounds,
  isOwner,
}: {
  job: BackupJob
  row: BackupScheduleRow | null
  report: BackupAgentJobReport | null
  agentSeen: boolean
  bounds: BackupScheduleResponse['bounds']
  isOwner: boolean
}) {
  const { t } = useTranslation()
  const confirm = useConfirm()
  const queryClient = useQueryClient()
  const stored = row?.config ?? null

  const [times, setTimes] = useState<string[]>(stored?.times ?? ['03:30'])
  const [time, setTime] = useState(stored?.time ?? (job === 'drill' ? '01:00' : '02:30'))
  const [weekday, setWeekday] = useState(stored?.weekday ?? 'sun')
  const [intervalMin, setIntervalMin] = useState(stored?.interval_min ?? 360)
  const [enabled, setEnabled] = useState(stored?.enabled ?? true)

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: backupScheduleQueryKey })
  const save = useMutation({
    mutationFn: (cfg: BackupScheduleConfig) => saveBackupSchedule(job, cfg),
    onSuccess: invalidate,
  })
  const reset = useMutation({
    mutationFn: () => resetBackupSchedule(job),
    onSuccess: invalidate,
  })

  // The server's own message, verbatim: the bounds are configurable
  // (compiled, but the message names the real numbers) and a client
  // restatement would drift — same reasoning as the password floor.
  const error = save.isError
    ? (apiErrorMessage(save.error) ?? t('common.error'))
    : reset.isError
      ? (apiErrorMessage(reset.error) ?? t('common.error'))
      : null

  // A job the agent reports it CANNOT run gets no editors: a schedule for it
  // would be a promise the process already said it cannot keep. A job MISSING
  // from a live agent's report is the same promise (older agent build) — only
  // when no agent ever reported at all does the owner get to pre-configure.
  const editable = isOwner && (agentSeen ? report?.capable === true : true)

  function draftConfig(): BackupScheduleConfig {
    switch (job) {
      case 'dump':
        return { times }
      case 'drill':
        return { time, weekday }
      case 'mirror':
        return { interval_min: intervalMin }
      default:
        return enabled ? { enabled: true, time } : { enabled: false }
    }
  }

  async function submit() {
    save.reset()
    reset.reset()
    const cfg = draftConfig()
    if (reducesProtection(job, stored, cfg, report)) {
      const ok = await confirm({
        title: t('admin.backup_schedule_reduce_confirm_title', {
          job: t(`admin.backup_job_${job}`),
        }),
        message: t('admin.backup_schedule_reduce_confirm_message'),
        confirmLabel: t('admin.backup_schedule_reduce_confirm_action'),
      })
      if (!ok) return
    }
    save.mutate(cfg)
  }

  async function resetToEnv() {
    save.reset()
    reset.reset()
    const ok = await confirm({
      title: t('admin.backup_schedule_reset_confirm_title', {
        job: t(`admin.backup_job_${job}`),
      }),
      message: t('admin.backup_schedule_reset_confirm_message'),
      confirmLabel: t('admin.backup_schedule_reset_confirm_action'),
    })
    if (ok) reset.mutate()
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <span className="fx-utable-name">{t(`admin.backup_job_${job}`)}</span>
        {report !== null ? (
          <>
            {/* The agent's render, verbatim — it is the same string its logs
                print, and the one agenda that is actually running. */}
            <code>{report.schedule}</code>
            <span className={'fx-chip' + (report.source === 'db' ? ' fx-chip-ok' : '')}>
              {t(`admin.backup_schedule_source_${report.source}`)}
            </span>
            {!report.capable && (
              <span className="fx-chip fx-chip-warn">
                {t(`admin.backup_reason_${report.reason}`, { defaultValue: report.reason })}
              </span>
            )}
          </>
        ) : (
          <span className="fx-utable-meta">{t('admin.backup_schedule_no_report')}</span>
        )}
      </div>

      {editable && (
        <div style={{ display: 'flex', alignItems: 'flex-end', gap: 10, flexWrap: 'wrap' }}>
          {job === 'dump' && (
            <div className="fx-field">
              <span className="fx-field-label">{t('admin.backup_schedule_times_label')}</span>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                {times.map((v, i) => (
                  <span key={i} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                    <input
                      className="fx-input"
                      type="time"
                      value={v}
                      aria-label={t('admin.backup_schedule_time_n_label', { n: i + 1 })}
                      onChange={(e) =>
                        setTimes(times.map((old, j) => (j === i ? e.target.value : old)))
                      }
                    />
                    {times.length > bounds.dump_times_min && (
                      <button
                        className="fx-pillbtn"
                        onClick={() => setTimes(times.filter((_, j) => j !== i))}
                      >
                        {t('admin.backup_schedule_remove_time')}
                      </button>
                    )}
                  </span>
                ))}
                {times.length < bounds.dump_times_max && (
                  <button className="fx-pillbtn" onClick={() => setTimes([...times, '12:00'])}>
                    {t('admin.backup_schedule_add_time')}
                  </button>
                )}
              </div>
            </div>
          )}

          {job === 'drill' && (
            <>
              <label className="fx-field">
                <span className="fx-field-label">{t('admin.backup_schedule_weekday_label')}</span>
                <select
                  className="fx-input"
                  value={weekday}
                  onChange={(e) => setWeekday(e.target.value)}
                >
                  {WEEKDAYS.map((d) => (
                    <option key={d} value={d}>
                      {t(`admin.backup_weekday_${d}`)}
                    </option>
                  ))}
                </select>
              </label>
              <label className="fx-field">
                <span className="fx-field-label">{t('admin.backup_schedule_time_label')}</span>
                <input
                  className="fx-input"
                  type="time"
                  value={time}
                  onChange={(e) => setTime(e.target.value)}
                />
              </label>
            </>
          )}

          {job === 'mirror' && (
            <label className="fx-field">
              <span className="fx-field-label">{t('admin.backup_schedule_interval_label')}</span>
              <input
                className="fx-input"
                type="number"
                min={bounds.mirror_interval_min}
                max={bounds.mirror_interval_max}
                value={intervalMin}
                onChange={(e) => setIntervalMin(Number(e.target.value))}
              />
              <span className="fx-field-hint">
                {t('admin.backup_schedule_interval_hint', {
                  min: bounds.mirror_interval_min,
                  max: bounds.mirror_interval_max,
                })}
              </span>
            </label>
          )}

          {job === 'user_zip' && (
            <>
              <label style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <input
                  type="checkbox"
                  checked={enabled}
                  onChange={(e) => setEnabled(e.target.checked)}
                />
                <span>{t('admin.backup_schedule_enabled_label')}</span>
              </label>
              {enabled && (
                <label className="fx-field">
                  <span className="fx-field-label">{t('admin.backup_schedule_time_label')}</span>
                  <input
                    className="fx-input"
                    type="time"
                    value={time}
                    onChange={(e) => setTime(e.target.value)}
                  />
                </label>
              )}
            </>
          )}

          <button
            className="fx-btn fx-btn-primary"
            disabled={save.isPending || reset.isPending}
            onClick={() => void submit()}
          >
            {save.isPending ? t('common.saving') : t('admin.backup_schedule_save')}
          </button>
          {row !== null && (
            <button
              className="fx-pillbtn"
              disabled={save.isPending || reset.isPending}
              onClick={() => void resetToEnv()}
            >
              {t('admin.backup_schedule_reset')}
            </button>
          )}
        </div>
      )}

      {error && (
        <div className="fx-inline-error" role="alert">
          {error}
        </div>
      )}
    </div>
  )
}

/** The server's closed weekday vocabulary, in its own order (sun-first). */
const WEEKDAYS = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'] as const

function JobRow({ status, onRun }: { status: BackupJobStatus; onRun: () => void }) {
  const { t } = useTranslation()
  const copier = useCopy()
  const last = status.last_success
  const stale =
    status.job === 'dump' &&
    last !== null &&
    Date.now() - Date.parse(last.started_at) > DUMP_STALE_MS

  return (
    <tr>
      <td>
        <span className="fx-utable-name">{t(`admin.backup_job_${status.job}`)}</span>
        {status.consecutive_failures > 0 && (
          <span className="fx-chip fx-chip-danger" style={{ marginLeft: 8 }}>
            {t('admin.backup_failures_chip', { count: status.consecutive_failures })}
          </span>
        )}
      </td>
      <td>
        {last === null ? (
          <span className="fx-utable-meta">{t('admin.backup_never_ran')}</span>
        ) : (
          <span className={'fx-utable-meta' + (stale ? ' fx-bkp-stale' : '')}>
            {relativeTime(last.started_at, t)}
            {stale && ` · ${t('admin.backup_stale_hint')}`}
          </span>
        )}
      </td>
      <td>
        {last?.artifact_key ? (
          <span className="fx-bkp-key" title={last.artifact_key}>{last.artifact_key}</span>
        ) : (
          <span className="fx-utable-meta">—</span>
        )}
      </td>
      <td className="fx-utable-meta">
        {last?.artifact_bytes != null ? formatBytes(last.artifact_bytes) : '—'}
      </td>
      <td>
        {last?.artifact_sha256 ? (
          <span className="fx-bkp-sha">
            <code title={last.artifact_sha256}>{last.artifact_sha256.slice(0, 12)}…</code>
            <button
              className="fx-pillbtn"
              onClick={() => void copier.copy(last.artifact_sha256!)}
            >
              {copier.copied(last.artifact_sha256) ? t('admin.backup_copied') : t('admin.backup_copy')}
            </button>
          </span>
        ) : (
          <span className="fx-utable-meta">—</span>
        )}
      </td>
      <td className="fx-utable-meta">{last ? runDuration(last, t) : '—'}</td>
      <td>
        <div className="fx-utable-actions">
          <button className="fx-pillbtn" onClick={onRun}>
            {t('admin.backup_run_now')}
          </button>
        </div>
      </td>
    </tr>
  )
}

function HistoryRow({ run }: { run: BackupRun }) {
  const { t } = useTranslation()
  const tone =
    run.status === 'succeeded' ? ' fx-chip-ok'
    : run.status === 'failed' ? ' fx-chip-danger'
    : ' fx-chip-warn'
  return (
    <tr>
      <td className="fx-utable-meta">{new Date(run.started_at).toLocaleString()}</td>
      <td className="fx-utable-meta">{t(`admin.backup_job_${run.job}`)}</td>
      <td>
        <span className={'fx-chip' + tone}>{t(`admin.backup_status_${run.status}`)}</span>
      </td>
      <td>
        {run.artifact_key ? (
          <span className="fx-bkp-key" title={run.artifact_key}>{run.artifact_key}</span>
        ) : (
          <span className="fx-utable-meta">—</span>
        )}
      </td>
      <td>
        {/* Verbatim, as code (never translated): the token is what the
            operator greps the agent's logs and the runbook for. */}
        {run.last_error ? <code>{run.last_error}</code> : <span className="fx-utable-meta">—</span>}
      </td>
    </tr>
  )
}

/**
 * The restored table counts the drill proved, straight from its meta. Absent
 * or malformed meta renders nothing — the panel's headline already says the
 * drill succeeded, and inventing zeros would claim a comparison that never ran.
 */
function DrillCounts({ meta }: { meta: Record<string, unknown> }) {
  const { t } = useTranslation()
  const tables = meta['tables']
  if (tables === null || typeof tables !== 'object' || Array.isArray(tables)) return null
  const entries = Object.entries(tables as Record<string, unknown>).filter(
    (e): e is [string, number] => typeof e[1] === 'number',
  )
  if (entries.length === 0) return null
  return (
    <div className="fx-bkp-counts">
      {entries.map(([table, count]) => (
        <span className="fx-chip" key={table}>
          {table}: {count.toLocaleString()}
        </span>
      ))}
      <span className="fx-utable-meta">{t('admin.backup_drill_counts_hint')}</span>
    </div>
  )
}

function runDuration(run: BackupRun, t: TFunction): string {
  if (!run.finished_at) return '—'
  const ms = Date.parse(run.finished_at) - Date.parse(run.started_at)
  if (!Number.isFinite(ms) || ms < 0) return '—'
  if (ms < 1000) return t('admin.backup_duration_ms', { value: ms })
  return t('admin.backup_duration_s', { value: (ms / 1000).toFixed(1) })
}

function formatBytes(b: number): string {
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
