import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { useConfirm } from '../ConfirmDialog'
import { useCopy } from '../../hooks/useCopy'
import { useCurrentUser } from '../../auth/AuthProvider'
import { useRevealTarget } from '../../hooks/useRevealTarget'
import { relativeTime } from '../../lib/time'
import { Icon, I } from '../icons'
import {
  backupScheduleQueryKey,
  backupStatusQueryKey,
  fetchBackupSchedule,
  fetchBackupStatus,
  requestBackupRun,
  type BackupAgentJobReport,
  type BackupAgentState,
  type BackupJob,
  type BackupJobStatus,
  type BackupRun,
  type BackupRunStatus,
  type BackupScheduleResponse,
} from '../../api/admin'
import { ScheduleCard } from './BackupScheduleEditor'
import {
  drillTableCount,
  drillTables,
  formatBytes,
  runDuration,
  statusTone,
} from './backupFormat'
import { apiErrorCode } from '../../lib/apiError'

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

/** The four jobs in the order the layout presents them. */
const JOBS: readonly BackupJob[] = ['dump', 'drill', 'mirror', 'user_zip'] as const

/** One glyph per job, from the shared registry — no one-off SVGs here. */
const JOB_ICON: Record<BackupJob, typeof I.folder> = {
  dump: I.layers,
  drill: I.refresh,
  mirror: I.swap,
  user_zip: I.users,
}

type HistoryFilter = 'all' | 'succeeded' | 'failed'

/* Frozen empties for the pending render: `?? []` allocates a new array every
   time, which would change the memo inputs on every render. */
const NO_JOBS: BackupJobStatus[] = []
const NO_RUNS: BackupRun[] = []

/**
 * The instance-wide backup surface (ADR-43/ADR-44, SDD-OPS-BACKUP §10.2).
 *
 * Read-only over backup_run and backup_schedule except for two verbs: "run
 * now" enqueues a requested row for the agent to claim, and the owner's
 * agenda editors write the schedule rows. The S3 credentials and the
 * execution never touch the web process, so a trigger's only feedback is the
 * new row in the history at the bottom.
 *
 * Everything on screen is a value the server answered. The deliberate
 * absences matter as much: there is no "download the last dump" (the web
 * process holds no S3 credential, by design) and no retention/destination
 * panel (the agent's env is not exposed through the API) — a disaster-recovery
 * screen that shows a plausible number nobody computed is worse than one that
 * shows nothing, which is the whole lesson of the mailer incident.
 */
export function BackupSection() {
  const { t } = useTranslation()
  const confirm = useConfirm()
  const queryClient = useQueryClient()
  const isOwner = useCurrentUser()?.role === 'owner'
  const [pages, setPages] = useState<number[]>([])
  const [selected, setSelected] = useState<BackupJob>('dump')
  const [filter, setFilter] = useState<HistoryFilter>('all')
  const { ref: agendaRef, reveal: revealAgenda } = useRevealTarget<HTMLDivElement>()

  const before = pages.length > 0 ? pages[pages.length - 1] : 0
  const query = useQuery({
    queryKey: backupStatusQueryKey(before),
    queryFn: () => fetchBackupStatus(before > 0 ? { before } : {}),
  })
  const schedule = useQuery({
    queryKey: backupScheduleQueryKey,
    queryFn: fetchBackupSchedule,
    refetchInterval: SCHEDULE_REFETCH_MS,
  })

  const trigger = useMutation({
    mutationFn: requestBackupRun,
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['admin', 'backup'] }),
  })

  /* The two stable members, not the mutation itself: `useMutation` answers a
     NEW object on every render, so depending on it would make the callback
     below a fresh prop on every poll tick and undo all four card memos. */
  const { mutate: mutateRun, reset: resetRun } = trigger

  const runNow = useCallback(async function runNow(job: BackupJob) {
    resetRun()
    const ok = await confirm({
      title: t('admin.backup_run_confirm_title', { job: t(`admin.backup_job_${job}`) }),
      message: t('admin.backup_run_confirm_message'),
      confirmLabel: t('admin.backup_run_confirm_action'),
    })
    if (ok) mutateRun(job)
  }, [confirm, t, mutateRun, resetRun])

  /* Stable identities: an inline arrow here would hand every card a new prop
     on each render and quietly undo the memo on all four. */
  const handleRun = useCallback((job: BackupJob) => void runNow(job), [runNow])

  /* The pending flag is read through a ref so the select callback below keeps
     ONE identity: taking `schedule.isPending` as a dependency would hand the
     four job cards a new prop the moment the load finished and undo their
     memos. */
  const revealedWhileLoading = useRef(false)
  const schedulePendingRef = useRef(schedule.isPending)

  /* The cards sit above the fold and the agenda below it, so picking a job up
     there used to change a form the reader could not see. The tabs INSIDE the
     agenda deliberately do NOT reveal: the click already happened in that
     card, and moving it under the cursor is gratuitous. */
  const handleSelectFromCard = useCallback(
    (job: BackupJob) => {
      setSelected(job)
      revealedWhileLoading.current = schedulePendingRef.current
      revealAgenda()
    },
    [revealAgenda],
  )

  /* A reveal fired during the first load centres the PLACEHOLDER, which is a
     few lines tall; the card then grows into the whole form and the position
     the scroll settled on is the middle of nothing. So the reveal repeats once
     the real card is there — and only the scroll repeats, because the caret
     already went there on the first one and the reader may have moved it since. */
  const schedulePending = schedule.isPending
  useEffect(() => {
    schedulePendingRef.current = schedulePending
    if (schedulePending || !revealedWhileLoading.current) return
    revealedWhileLoading.current = false
    revealAgenda({ keepFocus: true })
  }, [schedulePending, revealAgenda])

  const jobs = query.data?.jobs ?? NO_JOBS
  const runs = query.data?.runs ?? NO_RUNS
  const byJob = useMemo(() => new Map(jobs.map((j) => [j.job, j])), [jobs])
  const neverRan =
    runs.length === 0 &&
    pages.length === 0 &&
    jobs.every((j) => j.last_success === null && j.consecutive_failures === 0)
  const staleRequested = useMemo(
    () =>
      runs.filter(
        (r) =>
          r.status === 'requested' &&
          Date.now() - Date.parse(r.scheduled_for) > REQUESTED_STALE_MS,
      ),
    [runs],
  )
  const drill = byJob.get('drill')?.last_success ?? null
  const triggerError = trigger.isError
    ? (apiErrorCode(trigger.error) ?? t('admin.backup_trigger_failed'))
    : null
  const filtered = useMemo(
    () => runs.filter((r) => filter === 'all' || r.status === filter),
    [runs, filter],
  )

  // Every hook has run by here — the guards below may return early.
  if (query.isPending) {
    return <div className="fx-card"><div className="fx-card-body"><div className="fx-empty">{t('common.loading')}</div></div></div>
  }
  if (query.isError || !query.data) {
    return <div className="fx-card"><div className="fx-card-body"><div className="fx-empty">{t('admin.backup_unavailable')}</div></div></div>
  }

  const agent = schedule.data?.agent ?? null
  const agentStale = agent !== null && Date.now() - Date.parse(agent.seen_at) > AGENT_STALE_MS
  // Build skew, compared and not re-derived: both numbers come from the
  // server. An agent that predates the field reports nothing, and nothing is
  // not a match — it is precisely the old build this row exists to name.
  const agentSchemaSkewed =
    agent !== null && (agent.schema_version ?? 0) < (schedule.data?.agent_schema_version ?? 0)

  return (
    <div className="fx-bkp">
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

      <Kpis jobs={jobs} drill={drill} />

      <div className="fx-bkp-jobs">
        {JOBS.map((job) => (
          <JobCard
            key={job}
            job={job}
            status={byJob.get(job) ?? null}
            report={agent?.jobs[job] ?? null}
            selected={selected === job}
            onSelect={handleSelectFromCard}
            onRun={handleRun}
          />
        ))}
      </div>

      <div className="fx-bkp-split">
        {/* Narrowed on purpose: the agenda card must not depend on `agent`,
            whose `seen_at` moves with every heartbeat. The banner below is
            where that timestamp belongs. */}
        <ScheduleCard
          selected={selected}
          onSelect={setSelected}
          jobs={schedule.data?.jobs}
          rows={schedule.data?.rows}
          bounds={schedule.data?.bounds}
          report={agent?.jobs[selected] ?? null}
          agentSeen={agent !== null}
          isPending={schedule.isPending}
          isError={schedule.isError}
          isOwner={isOwner}
          cardRef={agendaRef}
        />
        <div className="fx-bkp-aside">
          <DrillCard drill={drill} />
          <AgentCard
            agent={agent}
            stale={agentStale}
            pending={schedule.isPending}
            skewed={agentSchemaSkewed}
          />
        </div>
      </div>

      <div className="fx-card">
        <div className="fx-card-body fx-bkp-history">
          <div className="fx-bkp-history-head">
            <div>
              <div className="fx-panel-title">{t('admin.backup_history_title')}</div>
              {/* "on this page", never "in the last 24 h": the count is what
                  this keyset page holds, and the query has no window. */}
              <div className="fx-panel-desc">
                {t('admin.backup_history_count', { count: runs.length })}
              </div>
            </div>
            <div className="fx-bkp-filters" role="group" aria-label={t('admin.backup_filter_label')}>
              {(['all', 'succeeded', 'failed'] as const).map((f) => (
                <button
                  key={f}
                  type="button"
                  className={'fx-bkp-filter' + (filter === f ? ' fx-bkp-filter-on' : '')}
                  aria-pressed={filter === f}
                  onClick={() => setFilter(f)}
                >
                  {t(`admin.backup_filter_${f}`)}
                </button>
              ))}
            </div>
          </div>

          {filtered.length === 0 && <div className="fx-empty">{t('admin.backup_history_empty')}</div>}
          {filtered.length > 0 && (
            <div className="fx-utable-wrap">
              <table className="fx-utable fx-bkp-table">
                <thead>
                  <tr>
                    <th>{t('admin.backup_col_when')}</th>
                    <th>{t('admin.backup_col_job')}</th>
                    <th>{t('admin.backup_col_status')}</th>
                    <th>{t('admin.backup_col_artifact')}</th>
                    <th>{t('admin.backup_col_error')}</th>
                    <th>{t('admin.backup_col_duration')}</th>
                  </tr>
                </thead>
                <tbody>
                  {filtered.map((r) => (
                    <HistoryRow key={r.id} run={r} />
                  ))}
                </tbody>
              </table>
            </div>
          )}

          <div className="fx-bkp-pager">
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
 * Four headline numbers, each one a value the API answered.
 *
 * Deliberately NOT "artifacts retained" or "failures in 7 days": retention
 * lives in the agent's env and the history is a keyset page, not a window —
 * both would be numbers the screen invented.
 */
const Kpis = memo(function Kpis({ jobs, drill }: { jobs: BackupJobStatus[]; drill: BackupRun | null }) {
  const { t } = useTranslation()
  const dump = jobs.find((j) => j.job === 'dump')?.last_success ?? null
  const dumpStale = dump !== null && Date.now() - Date.parse(dump.started_at) > DUMP_STALE_MS
  const failures = jobs.reduce((max, j) => Math.max(max, j.consecutive_failures), 0)
  const tables = drill ? drillTableCount(drill.meta) : 0

  return (
    <div className="fx-bkp-kpis">
      <Kpi
        tone={dump === null ? 'warn' : dumpStale ? 'warn' : 'ok'}
        label={t('admin.backup_kpi_last_dump')}
        value={dump === null ? '—' : relativeTime(dump.started_at, t)}
        hint={dump === null ? t('admin.backup_never_ran') : new Date(dump.started_at).toLocaleString()}
      />
      <Kpi
        tone={drill === null ? 'warn' : 'ok'}
        label={t('admin.backup_kpi_integrity')}
        value={drill === null ? '—' : t('admin.backup_integrity_ok')}
        hint={
          drill === null
            ? t('admin.backup_integrity_none')
            : t('admin.backup_integrity_hint', { count: tables })
        }
      />
      <Kpi
        tone="info"
        label={t('admin.backup_kpi_dump_size')}
        value={dump?.artifact_bytes != null ? formatBytes(dump.artifact_bytes) : '—'}
        hint={dump ? runDuration(dump, t) : t('admin.backup_never_ran')}
      />
      <Kpi
        tone={failures > 0 ? 'danger' : 'ok'}
        label={t('admin.backup_kpi_failures')}
        value={String(failures)}
        hint={t('admin.backup_kpi_failures_hint')}
      />
    </div>
  )
})

function Kpi({
  tone,
  label,
  value,
  hint,
}: {
  tone: 'ok' | 'warn' | 'danger' | 'info'
  label: string
  value: string
  hint: string
}) {
  return (
    <div className="fx-bkp-kpi">
      <div className="fx-bkp-kpi-head">
        <span className="fx-bkp-kpi-label">{label}</span>
        <span className={`fx-bkp-dot fx-bkp-dot-${tone}`} />
      </div>
      <div className="fx-bkp-kpi-value">{value}</div>
      <div className="fx-bkp-kpi-hint">{hint}</div>
    </div>
  )
}

/**
 * One job as a selectable card: what it is, the agenda the AGENT says it is
 * following, and its last proven outcome. Selecting it moves the agenda
 * editor below to the same job, so the card and the form never disagree
 * about which job is on screen.
 */
const JobCard = memo(function JobCard({
  job,
  status,
  report,
  selected,
  onSelect,
  onRun,
}: {
  job: BackupJob
  status: BackupJobStatus | null
  report: BackupAgentJobReport | null
  selected: boolean
  onSelect: (job: BackupJob) => void
  onRun: (job: BackupJob) => void
}) {
  const { t } = useTranslation()
  const copier = useCopy()
  const last = status?.last_success ?? null
  const failures = status?.consecutive_failures ?? 0
  const stale = job === 'dump' && last !== null && Date.now() - Date.parse(last.started_at) > DUMP_STALE_MS
  const name = t(`admin.backup_job_${job}`)

  return (
    <div className={'fx-bkp-job' + (selected ? ' fx-bkp-job-on' : '')}>
      <button
        type="button"
        className="fx-bkp-job-head"
        aria-pressed={selected}
        aria-label={t('admin.backup_select_job', { job: name })}
        onClick={() => onSelect(job)}
      >
        <span className={`fx-bkp-job-icon fx-bkp-job-icon-${job}`}>
          <Icon d={JOB_ICON[job]} size={17} />
        </span>
        <span className="fx-bkp-job-titles">
          <span className="fx-bkp-job-title">
            {name}
            {/* The agent's own render of the agenda, verbatim — the same
                string its logs print, and the one that is actually running. */}
            {report && <code className="fx-bkp-job-cron">{report.schedule}</code>}
          </span>
          <span className="fx-bkp-job-desc">{t(`admin.backup_job_desc_${job}`)}</span>
        </span>
        <span className="fx-bkp-job-state">
          {failures > 0 ? (
            <span className="fx-chip fx-chip-danger">
              {t('admin.backup_failures_chip', { count: failures })}
            </span>
          ) : last ? (
            <span className="fx-chip fx-chip-ok">{t('admin.backup_status_succeeded')}</span>
          ) : (
            <span className="fx-chip">{t('admin.backup_never_ran')}</span>
          )}
        </span>
      </button>

      <div className="fx-bkp-job-metrics">
        <div>
          <span className="fx-bkp-metric-label">{t('admin.backup_col_last_success')}</span>
          <span className={'fx-bkp-metric-value' + (stale ? ' fx-bkp-stale' : '')}>
            {last ? relativeTime(last.started_at, t) : '—'}
            {stale && ` · ${t('admin.backup_stale_hint')}`}
          </span>
        </div>
        <div>
          <span className="fx-bkp-metric-label">{t('admin.backup_col_duration')}</span>
          <span className="fx-bkp-metric-value">{last ? runDuration(last, t) : '—'}</span>
        </div>
        <div>
          <span className="fx-bkp-metric-label">{t('admin.backup_col_size')}</span>
          <span className="fx-bkp-metric-value">
            {last?.artifact_bytes != null ? formatBytes(last.artifact_bytes) : '—'}
          </span>
        </div>
      </div>

      {(last?.artifact_key || last?.artifact_sha256) && (
        <div className="fx-bkp-job-artifact">
          {last.artifact_key && (
            <span className="fx-bkp-key" title={last.artifact_key}>
              {last.artifact_key}
            </span>
          )}
          {last.artifact_sha256 && (
            <button
              type="button"
              className="fx-bkp-sha-btn"
              title={last.artifact_sha256}
              onClick={() => void copier.copy(last.artifact_sha256!)}
            >
              <code>{last.artifact_sha256.slice(0, 12)}…</code>
              <span>
                {copier.copied(last.artifact_sha256)
                  ? t('admin.backup_copied')
                  : t('admin.backup_copy')}
              </span>
            </button>
          )}
        </div>
      )}

      <button type="button" className="fx-bkp-job-run" onClick={() => onRun(job)}>
        {t('admin.backup_run_now')}
      </button>
    </div>
  )
})

/**
 * The last drill, as the proof it is: which dump it restored, how long the
 * restore took, and the row counts it compared. No drill on record renders
 * the honest absence — a green panel with no numbers behind it would claim a
 * restore nobody ran.
 */
const DrillCard = memo(function DrillCard({ drill }: { drill: BackupRun | null }) {
  const { t } = useTranslation()
  return (
    <div className="fx-bkp-drill">
      <div className="fx-bkp-drill-head">
        <span className="fx-bkp-kpi-label">{t('admin.backup_drill_title')}</span>
        <span className={`fx-bkp-dot fx-bkp-dot-${drill ? 'ok' : 'warn'}`} />
      </div>
      {drill === null ? (
        <>
          <div className="fx-bkp-drill-title">{t('admin.backup_integrity_none')}</div>
          <div className="fx-bkp-drill-sub">{t('admin.backup_drill_never_desc')}</div>
        </>
      ) : (
        <>
          <div className="fx-bkp-drill-title">
            {drill.drill_of_run_id !== null
              ? t('admin.backup_drill_headline', { run: drill.drill_of_run_id })
              : t('admin.backup_integrity_ok')}
          </div>
          <div className="fx-bkp-drill-sub">
            {t('admin.backup_drill_sub', {
              when: relativeTime(drill.started_at, t),
              duration: runDuration(drill, t),
            })}
          </div>
          <DrillCounts meta={drill.meta} />
        </>
      )}
    </div>
  )
})

/**
 * The heartbeat, as its own panel: which build is running, when it last
 * reported, and what it says about each job. This is the honesty layer — the
 * agenda above is only the editable row; THIS is the process that reads it.
 */
const AgentCard = memo(function AgentCard({
  agent,
  stale,
  pending,
  skewed,
}: {
  agent: BackupAgentState | null
  stale: boolean
  pending: boolean
  skewed: boolean
}) {
  const { t } = useTranslation()
  if (pending) return null

  return (
    <div className="fx-panel fx-bkp-agent">
      <div className="fx-panel-head">
        <div>
          <div className="fx-panel-title">{t('admin.backup_agent_title')}</div>
          <div className="fx-panel-desc">
            {agent === null
              ? t('admin.backup_agent_never_desc')
              : t('admin.backup_agent_seen', {
                  when: relativeTime(agent.seen_at, t),
                  version: agent.version,
                })}
          </div>
        </div>
        <span className={`fx-bkp-dot fx-bkp-dot-${agent === null || stale ? 'warn' : 'ok'}`} />
      </div>

      {agent === null && (
        <div className="fx-bkp-agent-empty">
          {t('admin.backup_agent_never_title')} <code>COMPOSE_PROFILES=backup</code>
        </div>
      )}
      {stale && (
        <div className="fx-bkp-agent-empty">
          <b>{t('admin.backup_agent_stale_title')}</b> {t('admin.backup_agent_stale_desc')}
        </div>
      )}
      {skewed && (
        <div className="fx-bkp-agent-empty">
          <b>{t('admin.backup_agent_skew_title')}</b> {t('admin.backup_agent_skew_desc')}
        </div>
      )}

      {agent !== null && (
        <ul className="fx-bkp-agent-jobs">
          {JOBS.map((job) => {
            const report = agent.jobs[job]
            return (
              <li key={job}>
                <span className="fx-bkp-agent-job">{t(`admin.backup_job_${job}`)}</span>
                {report ? (
                  report.capable ? (
                    <code>{report.schedule}</code>
                  ) : (
                    <span className="fx-chip fx-chip-warn">
                      {t(`admin.backup_reason_${report.reason}`, { defaultValue: report.reason })}
                    </span>
                  )
                ) : (
                  <span className="fx-utable-meta">{t('admin.backup_schedule_no_report')}</span>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
})

const HistoryRow = memo(function HistoryRow({ run }: { run: BackupRun }) {
  const { t } = useTranslation()
  return (
    <tr>
      <td className="fx-utable-meta">{new Date(run.started_at).toLocaleString()}</td>
      <td>
        <span className="fx-bkp-history-job">
          <span className={`fx-bkp-jobdot fx-bkp-jobdot-${run.job}`} />
          {t(`admin.backup_job_${run.job}`)}
        </span>
      </td>
      <td>
        <span className={'fx-chip' + statusTone(run.status)}>
          {t(`admin.backup_status_${run.status}`)}
        </span>
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
      <td className="fx-utable-meta">{runDuration(run, t)}</td>
    </tr>
  )
})

/**
 * The restored table counts the drill proved, straight from its meta. Absent
 * or malformed meta renders nothing — the panel's headline already says the
 * drill succeeded, and inventing zeros would claim a comparison that never ran.
 */
function DrillCounts({ meta }: { meta: Record<string, unknown> }) {
  const { t } = useTranslation()
  const entries = drillTables(meta)
  if (entries.length === 0) return null
  return (
    <>
      <div className="fx-bkp-counts">
        {entries.map(([table, count]) => (
          <span className="fx-bkp-count" key={table}>
            {table}
            <b>{count.toLocaleString()}</b>
          </span>
        ))}
      </div>
      <div className="fx-bkp-drill-sub">{t('admin.backup_drill_counts_hint')}</div>
    </>
  )
}
