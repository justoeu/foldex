import { memo, useState, type RefObject } from 'react'
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
 * The last-resort draft, used only when neither a stored row nor the agent's
 * env baseline states an agenda — which happens before any agent ever reported
 * (the owner pre-configuring an instance whose backup profile is still off).
 * The `.env` is the first option; this is the fourth.
 */
function fallbackFor(job: BackupJob): BackupScheduleConfig {
  switch (job) {
    case 'drill':
      return { mode: 'times', times: ['01:00'], weekdays: ['sun'] }
    case 'mirror':
      return { mode: 'interval', interval_min: 360 }
    case 'user_zip':
      return { mode: 'times', times: ['02:30'], weekdays: [...WEEKDAYS], enabled: true }
    default:
      return { mode: 'times', times: ['03:30'], weekdays: [...WEEKDAYS] }
  }
}

/**
 * The draft the editor opens on: the stored row, else the ENV baseline the
 * agent publishes, else the fallback above. That order IS the layering
 * (INV-173) — the environment decides the agenda until a row overrides it, so
 * an owner who saves without touching anything writes their own environment's
 * agenda back rather than this screen's opinion of a good one.
 */
export function seedDraft(
  job: BackupJob,
  stored: BackupScheduleConfig | null,
  baseline: BackupScheduleConfig | null | undefined,
): BackupScheduleConfig {
  if (stored) return stored
  if (baseline?.mode) return baseline
  // A report whose baseline carries no mode is an env agenda that is OFF, and
  // only user_zip may be: opening its switch on would propose turning a job on
  // as the effect of merely looking at it.
  if (baseline && job === 'user_zip') return { ...fallbackFor(job), enabled: false }
  return fallbackFor(job)
}

/**
 * What actually reaches the server: exactly the fields the chosen mode uses.
 * The draft below is deliberately FAT — it keeps the times while you are on
 * the interval tab, so switching back does not discard them — but a stored row
 * carrying an agenda no process reads is the shape this whole change exists to
 * kill, so the payload is canonicalised here rather than in the draft.
 */
export function payloadOf(job: BackupJob, cfg: BackupScheduleConfig): BackupScheduleConfig {
  // Only user_zip may be switched off, so only user_zip ever carries the flag.
  if (job !== 'user_zip') {
    return cfg.mode === 'interval'
      ? { mode: 'interval', interval_min: cfg.interval_min }
      : { mode: 'times', times: cfg.times, weekdays: cfg.weekdays }
  }
  if (cfg.enabled === false) return { mode: cfg.mode, enabled: false }
  return cfg.mode === 'interval'
    ? { mode: 'interval', interval_min: cfg.interval_min, enabled: true }
    : { mode: 'times', times: cfg.times, weekdays: cfg.weekdays, enabled: true }
}

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
  cardRef,
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
  /* A ref object, not a callback: its identity is stable, so it does not
     defeat the memo the way an inline closure would. */
  cardRef: RefObject<HTMLDivElement | null>
}) {
  const { t } = useTranslation()

  // The two placeholders wear the grid class and the focus target too: a job
  // card clicked during the first load must still reveal this slot, and the
  // column must keep its shape while it fills.
  if (isPending) {
    return (
      <div className="fx-card fx-bkp-agenda" ref={cardRef} tabIndex={-1}>
        <div className="fx-card-body"><div className="fx-empty">{t('common.loading')}</div></div>
      </div>
    )
  }
  if (isError || !data) {
    return (
      <div className="fx-card fx-bkp-agenda" ref={cardRef} tabIndex={-1}>
        <div className="fx-card-body"><div className="fx-empty">{t('admin.backup_schedule_unavailable')}</div></div>
      </div>
    )
  }

  const row = data.rows[selected] ?? null
  const report = data.agent?.jobs[selected] ?? null

  return (
    <div className="fx-card fx-bkp-agenda" ref={cardRef} tabIndex={-1}>
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

/**
 * The shell: it owns the draft, the two mutations and the confirmations, and
 * delegates the controls to `ScheduleFields`. There is ONE fields component
 * because there is now one vocabulary — the jobs differ only in the floors the
 * server enforces, which arrive as `bounds`.
 */
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
  const [draft, setDraft] = useState<BackupScheduleConfig>(() =>
    seedDraft(job, stored, report?.baseline),
  )

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

  async function submit() {
    save.reset()
    reset.reset()
    const payload = payloadOf(job, draft)
    if (reducesProtection(stored, payload, report?.baseline)) {
      const ok = await confirm({
        title: t('admin.backup_schedule_reduce_confirm_title', {
          job: t(`admin.backup_job_${job}`),
        }),
        message: t('admin.backup_schedule_reduce_confirm_message'),
        confirmLabel: t('admin.backup_schedule_reduce_confirm_action'),
      })
      if (!ok) return
    }
    save.mutate(payload)
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
      <ScheduleFields job={job} config={draft} onChange={setDraft} bounds={bounds} />

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

/**
 * One vocabulary for four jobs: an off switch (user_zip only), a mode, and
 * then either the weekday × wall-time grid or an interval.
 *
 * Nothing here re-derives the server's policy. The weekday floor is a HINT
 * carrying the server's own number, and unpicking the last day is allowed on
 * purpose: the refusal lives in one place, and the message the owner reads is
 * the server's (INV-138 by analogy). Pinning a day client-side would be a
 * second copy of that policy — and it could not express the dump's floor of
 * five anyway.
 */
function ScheduleFields({
  job,
  config,
  onChange,
  bounds,
}: {
  job: BackupJob
  config: BackupScheduleConfig
  onChange: (config: BackupScheduleConfig) => void
  bounds: BackupScheduleResponse['bounds']
}) {
  const { t } = useTranslation()
  const enabled = config.enabled !== false
  const mode = config.mode === 'interval' ? 'interval' : 'times'
  const weekdayFloor = job === 'dump' ? bounds.dump_weekdays_min : bounds.weekdays_min

  function switchMode(next: 'times' | 'interval') {
    // The other mode's values stay on the draft; `payloadOf` is what drops
    // them, so a tab round-trip never costs the owner their edits.
    onChange(
      next === 'interval'
        ? { ...config, mode: next, interval_min: config.interval_min ?? 360 }
        : {
            ...config,
            mode: next,
            times: config.times ?? ['03:30'],
            weekdays: config.weekdays ?? [...WEEKDAYS],
          },
    )
  }

  function toggleWeekday(day: string) {
    const picked = new Set(config.weekdays ?? [])
    if (picked.has(day)) picked.delete(day)
    else picked.add(day)
    // Rebuilt from the vocabulary, never from click order: the stored document
    // reads the same whichever way the owner assembled it.
    onChange({ ...config, weekdays: WEEKDAYS.filter((d) => picked.has(d)) })
  }

  const times = config.times ?? []

  return (
    <>
      {job === 'user_zip' && (
        /* The same switch the instance-policy screen uses — one toggle shape in
           the administration surface, focus ring included. */
        <label className="fx-toggle-row">
          <input
            type="checkbox"
            checked={enabled}
            aria-label={t('admin.backup_schedule_enabled_label')}
            onChange={(e) => onChange({ ...config, enabled: e.target.checked })}
          />
          <span className="fx-toggle-track"><span className="fx-toggle-knob" /></span>
          <span className="fx-toggle-label">
            {t('admin.backup_schedule_enabled_label')}
            <span className="fx-toggle-hint">{t('admin.backup_schedule_enabled_desc')}</span>
          </span>
        </label>
      )}

      {enabled && (
        <>
          <div className="fx-bkp-control">
            <span className="fx-bkp-control-label">{t('admin.backup_schedule_mode_label')}</span>
            <div
              className="fx-bkp-modes"
              role="tablist"
              aria-label={t('admin.backup_schedule_mode_label')}
            >
              {(['times', 'interval'] as const).map((m) => (
                <button
                  key={m}
                  type="button"
                  role="tab"
                  aria-selected={mode === m}
                  className={'fx-bkp-mode' + (mode === m ? ' fx-bkp-mode-on' : '')}
                  onClick={() => switchMode(m)}
                >
                  {t(`admin.backup_schedule_mode_${m}`)}
                </button>
              ))}
            </div>
          </div>

          {mode === 'times' ? (
            <>
              <div className="fx-bkp-control">
                <span className="fx-bkp-control-label">
                  {t('admin.backup_schedule_weekdays_label')}
                </span>
                <div
                  className="fx-bkp-days"
                  role="group"
                  aria-label={t('admin.backup_schedule_weekdays_label')}
                >
                  {WEEKDAYS.map((d) => {
                    const on = config.weekdays?.includes(d) === true
                    return (
                      <button
                        key={d}
                        type="button"
                        className={'fx-bkp-day' + (on ? ' fx-bkp-day-on' : '')}
                        aria-pressed={on}
                        aria-label={t(`admin.backup_weekday_${d}`)}
                        onClick={() => toggleWeekday(d)}
                      >
                        {t(`admin.backup_weekday_short_${d}`)}
                      </button>
                    )
                  })}
                </div>
                <div className="fx-bkp-daysets">
                  <button
                    type="button"
                    className="fx-bkp-dayset"
                    onClick={() => onChange({ ...config, weekdays: [...WEEKDAYS] })}
                  >
                    {t('admin.backup_schedule_weekdays_all')}
                  </button>
                  <button
                    type="button"
                    className="fx-bkp-dayset"
                    onClick={() =>
                      onChange({ ...config, weekdays: ['mon', 'tue', 'wed', 'thu', 'fri'] })
                    }
                  >
                    {t('admin.backup_schedule_weekdays_workweek')}
                  </button>
                </div>
                {/* A floor of one is what "pick some days" already means; only
                    a job that demands more than that has something to say. */}
                {weekdayFloor > 1 && (
                  <span className="fx-bkp-control-hint">
                    {t('admin.backup_schedule_weekdays_floor_hint', { min: weekdayFloor })}
                  </span>
                )}
              </div>

              <div className="fx-bkp-control">
                <span className="fx-bkp-control-label">
                  {t('admin.backup_schedule_times_label')}
                </span>
                <div className="fx-bkp-times">
                  {times.map((v, i) => (
                    <span className="fx-bkp-time" key={i}>
                      <input
                        className="fx-bkp-input"
                        type="time"
                        value={v}
                        aria-label={t('admin.backup_schedule_time_n_label', { n: i + 1 })}
                        onChange={(e) =>
                          onChange({
                            ...config,
                            times: times.map((old, j) => (j === i ? e.target.value : old)),
                          })
                        }
                      />
                      {times.length > bounds.times_min && (
                        <button
                          type="button"
                          className="fx-bkp-time-remove"
                          aria-label={t('admin.backup_schedule_remove_time_n', { n: i + 1 })}
                          onClick={() =>
                            onChange({ ...config, times: times.filter((_, j) => j !== i) })
                          }
                        >
                          {t('admin.backup_schedule_remove_time')}
                        </button>
                      )}
                    </span>
                  ))}
                  {times.length < bounds.times_max && (
                    <button
                      type="button"
                      className="fx-bkp-add"
                      onClick={() => onChange({ ...config, times: [...times, '12:00'] })}
                    >
                      <span aria-hidden="true">+</span> {t('admin.backup_schedule_add_time')}
                    </button>
                  )}
                </div>
              </div>
            </>
          ) : (
            <div className="fx-bkp-control">
              <span className="fx-bkp-control-label">
                {t('admin.backup_schedule_interval_label')}
              </span>
              <div className="fx-bkp-interval">
                <span className="fx-bkp-interval-field">
                  <input
                    className="fx-bkp-input"
                    type="number"
                    min={bounds.interval_min}
                    max={bounds.interval_max}
                    value={config.interval_min ?? 0}
                    aria-label={t('admin.backup_schedule_interval_label')}
                    /* Coerced here: the server's bounds check runs on a NUMBER,
                       and a string would be refused by the JSON schema, not by
                       the floor. */
                    onChange={(e) =>
                      onChange({ ...config, interval_min: Number(e.target.value) })
                    }
                  />
                  <span className="fx-bkp-unit">min</span>
                </span>
                <span className="fx-bkp-presets">
                  {INTERVAL_PRESETS.map((p) => (
                    <button
                      key={p}
                      type="button"
                      className={'fx-bkp-preset' + (config.interval_min === p ? ' fx-bkp-preset-on' : '')}
                      aria-pressed={config.interval_min === p}
                      onClick={() => onChange({ ...config, interval_min: p })}
                    >
                      {formatMinutes(p)}
                    </button>
                  ))}
                </span>
              </div>
              <span className="fx-bkp-control-hint">
                {t('admin.backup_schedule_interval_hint', {
                  min: bounds.interval_min,
                  max: bounds.interval_max,
                })}
              </span>
            </div>
          )}
        </>
      )}
    </>
  )
}
