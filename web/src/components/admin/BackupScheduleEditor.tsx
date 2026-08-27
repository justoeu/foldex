import { memo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useConfirm } from '../ConfirmDialog'
import {
  backupScheduleQueryKey,
  resetBackupSchedule,
  saveBackupSchedule,
  type BackupAgentJobReport,
  type BackupJob,
  type BackupScheduleConfig,
  type BackupScheduleResponse,
  type BackupScheduleRow,
} from '../../api/admin'
import { reducesProtection } from './backupSchedule'
import { formatMinutes } from './backupFormat'
import { apiErrorMessage } from '../../lib/apiError'

/** The server's closed weekday vocabulary, in its own order (sun-first). */
const WEEKDAYS = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'] as const

/** Shortcuts for the mirror interval. Every one is inside the server's floor. */
const INTERVAL_PRESETS = [30, 60, 360, 720] as const

/**
 * The configurable agenda (ADR-44): one card, one job at a time, chosen by
 * the tabs that mirror the cards above. The permission split follows the
 * server — reading rides `instance.backup`, writing is
 * `instance.backup_schedule`, owner-only and locked — so a non-owner sees the
 * agenda and no controls at all.
 */
export const ScheduleCard = memo(function ScheduleCard({
  selected,
  onSelect,
  data,
  isPending,
  isError,
  isOwner,
}: {
  selected: BackupJob
  onSelect: (job: BackupJob) => void
  /* Narrowed to the three values this card reads, not the whole
     UseQueryResult: that object is a new reference on every poll tick, which
     would defeat the memo above on a card whose inputs did not change. */
  data: BackupScheduleResponse | undefined
  isPending: boolean
  isError: boolean
  isOwner: boolean
}) {
  const { t } = useTranslation()

  if (isPending) {
    return <div className="fx-card"><div className="fx-card-body"><div className="fx-empty">{t('common.loading')}</div></div></div>
  }
  if (isError || !data) {
    return <div className="fx-card"><div className="fx-card-body"><div className="fx-empty">{t('admin.backup_schedule_unavailable')}</div></div></div>
  }

  const row = data.rows[selected] ?? null
  const report = data.agent?.jobs[selected] ?? null

  return (
    <div className="fx-card fx-bkp-agenda">
      <div className="fx-card-body">
        <div className="fx-bkp-agenda-head">
          <div>
            <div className="fx-panel-title">
              {t('admin.backup_agenda_title', { job: t(`admin.backup_job_${selected}`) })}
            </div>
            <div className="fx-panel-desc">{t(`admin.backup_agenda_desc_${selected}`)}</div>
          </div>
          {report ? (
            <span className={'fx-chip' + (report.source === 'db' ? ' fx-chip-ok' : '')}>
              {t(`admin.backup_schedule_source_${report.source}`)}
            </span>
          ) : (
            <span className="fx-chip fx-chip-warn">{t('admin.backup_schedule_no_report')}</span>
          )}
        </div>

        <div className="fx-bkp-tabs" role="tablist">
          {data.jobs.map((job) => (
            <button
              key={job}
              type="button"
              role="tab"
              aria-selected={job === selected}
              className={'fx-bkp-tab' + (job === selected ? ' fx-bkp-tab-on' : '')}
              onClick={() => onSelect(job)}
            >
              {t(`admin.backup_job_${job}`)}
            </button>
          ))}
        </div>

        <ScheduleEditor
          // A saved or reset row remounts the editor so its draft reseeds
          // from what the server now holds, instead of a stale local copy.
          key={`${selected}:${row?.updated_at ?? 'baseline'}`}
          job={selected}
          row={row}
          report={report}
          agentSeen={data.agent !== null}
          bounds={data.bounds}
          isOwner={isOwner}
        />
      </div>
    </div>
  )
})

function ScheduleEditor({
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

  const invalidate = () => queryClient.invalidateQueries({ queryKey: backupScheduleQueryKey })
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

  if (!editable) {
    return (
      <div className="fx-bkp-agenda-body">
        <div className="fx-bkp-effective">
          {report ? (
            <>
              <code>{report.schedule}</code>
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
      </div>
    )
  }

  return (
    <div className="fx-bkp-agenda-body">
      {job === 'dump' && (
        <div className="fx-bkp-control">
          <span className="fx-bkp-control-label">{t('admin.backup_schedule_times_label')}</span>
          <div className="fx-bkp-times">
            {times.map((v, i) => (
              <span className="fx-bkp-time" key={i}>
                <input
                  className="fx-bkp-input"
                  type="time"
                  value={v}
                  aria-label={t('admin.backup_schedule_time_n_label', { n: i + 1 })}
                  onChange={(e) => setTimes(times.map((old, j) => (j === i ? e.target.value : old)))}
                />
                {times.length > bounds.dump_times_min && (
                  <button
                    type="button"
                    className="fx-bkp-time-remove"
                    aria-label={t('admin.backup_schedule_remove_time_n', { n: i + 1 })}
                    onClick={() => setTimes(times.filter((_, j) => j !== i))}
                  >
                    {t('admin.backup_schedule_remove_time')}
                  </button>
                )}
              </span>
            ))}
            {times.length < bounds.dump_times_max && (
              <button
                type="button"
                className="fx-bkp-add"
                onClick={() => setTimes([...times, '12:00'])}
              >
                <span aria-hidden="true">+</span> {t('admin.backup_schedule_add_time')}
              </button>
            )}
          </div>
        </div>
      )}

      {job === 'drill' && (
        <div className="fx-bkp-row">
          <div className="fx-bkp-control">
            <span className="fx-bkp-control-label">{t('admin.backup_schedule_weekday_label')}</span>
            <div className="fx-bkp-days" role="group" aria-label={t('admin.backup_schedule_weekday_label')}>
              {WEEKDAYS.map((d) => (
                <button
                  key={d}
                  type="button"
                  className={'fx-bkp-day' + (weekday === d ? ' fx-bkp-day-on' : '')}
                  aria-pressed={weekday === d}
                  aria-label={t(`admin.backup_weekday_${d}`)}
                  onClick={() => setWeekday(d)}
                >
                  {t(`admin.backup_weekday_short_${d}`)}
                </button>
              ))}
            </div>
          </div>
          <label className="fx-bkp-control">
            <span className="fx-bkp-control-label">{t('admin.backup_schedule_time_label')}</span>
            <input
              className="fx-bkp-input"
              type="time"
              value={time}
              onChange={(e) => setTime(e.target.value)}
            />
          </label>
        </div>
      )}

      {job === 'mirror' && (
        <div className="fx-bkp-control">
          <span className="fx-bkp-control-label">{t('admin.backup_schedule_interval_label')}</span>
          <div className="fx-bkp-interval">
            <span className="fx-bkp-interval-field">
              <input
                className="fx-bkp-input"
                type="number"
                min={bounds.mirror_interval_min}
                max={bounds.mirror_interval_max}
                value={intervalMin}
                aria-label={t('admin.backup_schedule_interval_label')}
                onChange={(e) => setIntervalMin(Number(e.target.value))}
              />
              <span className="fx-bkp-unit">min</span>
            </span>
            <span className="fx-bkp-presets">
              {INTERVAL_PRESETS.map((p) => (
                <button
                  key={p}
                  type="button"
                  className={'fx-bkp-preset' + (intervalMin === p ? ' fx-bkp-preset-on' : '')}
                  aria-pressed={intervalMin === p}
                  onClick={() => setIntervalMin(p)}
                >
                  {formatMinutes(p)}
                </button>
              ))}
            </span>
          </div>
          <span className="fx-bkp-control-hint">
            {t('admin.backup_schedule_interval_hint', {
              min: bounds.mirror_interval_min,
              max: bounds.mirror_interval_max,
            })}
          </span>
        </div>
      )}

      {job === 'user_zip' && (
        <>
          {/* The same switch the instance-policy screen uses — one toggle
              shape in the administration surface, focus ring included. */}
          <label className="fx-toggle-row">
            <input
              type="checkbox"
              checked={enabled}
              aria-label={t('admin.backup_schedule_enabled_label')}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            <span className="fx-toggle-track"><span className="fx-toggle-knob" /></span>
            <span className="fx-toggle-label">
              {t('admin.backup_schedule_enabled_label')}
              <span className="fx-toggle-hint">{t('admin.backup_schedule_enabled_desc')}</span>
            </span>
          </label>
          {enabled && (
            <label className="fx-bkp-control">
              <span className="fx-bkp-control-label">{t('admin.backup_schedule_time_label')}</span>
              <input
                className="fx-bkp-input"
                type="time"
                value={time}
                onChange={(e) => setTime(e.target.value)}
              />
            </label>
          )}
        </>
      )}

      <div className="fx-bkp-actions">
        <button
          type="button"
          className="fx-btn fx-btn-primary"
          disabled={save.isPending || reset.isPending}
          onClick={() => void submit()}
        >
          {save.isPending ? t('common.saving') : t('admin.backup_schedule_save')}
        </button>
        {row !== null && (
          <button
            type="button"
            className="fx-btn"
            disabled={save.isPending || reset.isPending}
            onClick={() => void resetToEnv()}
          >
            {t('admin.backup_schedule_reset')}
          </button>
        )}
      </div>

      {error && (
        <div className="fx-inline-error" role="alert">
          {error}
        </div>
      )}
    </div>
  )
}
