import { memo, useState, type ReactNode, type RefObject } from 'react'
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

/** The two schedule modes, in render order — also the arrow-key order. */
const MODES = ['times', 'interval'] as const

/** Which way each arrow moves inside a radiogroup. */
const ARROW_STEP: Record<string, number | undefined> = {
  ArrowRight: 1,
  ArrowDown: 1,
  ArrowLeft: -1,
  ArrowUp: -1,
}

/** The server's closed weekday vocabulary, in its own order (sun-first). */
const WEEKDAYS = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'] as const

/** The "work week" shortcut, derived so it cannot drift from the list above. */
const WORKWEEK = WEEKDAYS.filter((d) => d !== 'sat' && d !== 'sun')

/** Shortcuts for the mirror interval. Every one is inside the server's floor. */
const INTERVAL_PRESETS = [30, 60, 360, 720] as const

/** The cadence a draft opens on when nothing upstream states an interval. */
const DEFAULT_INTERVAL_MIN = 360

/**
 * The wall time a row the owner just ADDED opens on — noon, so it reads as the
 * placeholder it is next to any agenda `fallbackFor` proposes.
 */
const ADDED_TIME = '12:00'

/** The floor this job's weekday set may not go under, in the server's numbers. */
function weekdayFloorFor(job: BackupJob, bounds: BackupScheduleResponse['bounds']): number {
  return job === 'dump' ? bounds.dump_weekdays_min : bounds.weekdays_min
}

/**
 * The last-resort draft, used only when neither a stored row nor the agent's
 * env baseline states an agenda — which happens before any agent ever reported
 * (the owner pre-configuring an instance whose backup profile is still off).
 * The `.env` is the first option; this is the fourth.
 *
 * It states BOTH modes on purpose: it is also what fills the half a stored row
 * or an env baseline leaves unsaid, and a draft missing that half is a form
 * with no days and no times in it.
 */
function fallbackFor(job: BackupJob): BackupScheduleConfig {
  const both = { times: ['03:30'], weekdays: [...WEEKDAYS], interval_min: DEFAULT_INTERVAL_MIN }
  switch (job) {
    case 'drill':
      return { ...both, mode: 'times', times: ['01:00'], weekdays: ['sun'] }
    case 'mirror':
      return { ...both, mode: 'interval' }
    case 'user_zip':
      return { ...both, mode: 'times', times: ['02:30'], enabled: true }
    default:
      return { ...both, mode: 'times' }
  }
}

/**
 * The env is exempt from the compiled floors by design — it IS the baseline —
 * so `BACKUP_DUMP_AT="03:30 sun"` is legal there and reaches this form as one
 * weekday against a floor of five. Opening the editor on a document the server
 * is known to refuse teaches the owner that the screen is broken, so the seed
 * corrects it in the only direction that is safe to take on their behalf:
 * widening RAISES protection, which is the direction that never asks for a
 * confirmation. The times stay exactly as the environment states them.
 */
function withinWeekdayFloor(
  job: BackupJob,
  cfg: BackupScheduleConfig,
  bounds: BackupScheduleResponse['bounds'],
): BackupScheduleConfig {
  if (cfg.mode !== 'times') return cfg
  if ((cfg.weekdays?.length ?? 0) >= weekdayFloorFor(job, bounds)) return cfg
  return { ...cfg, weekdays: [...WEEKDAYS] }
}

/**
 * `ScheduleStore.Load` returns invalid rows on purpose, so the env fallback
 * stays visible — which means a row written by hand in SQL reaches this form,
 * and an unbounded `times` array would render an unbounded list of inputs. The
 * surplus is dropped for the same reason the weekday set is widened above: the
 * form must not open on a document it knows the server refuses, and six inputs
 * beside a 400 that says "between 1 and 6" is a screen the owner cannot
 * reconcile. Trimming reduces the agenda, so the save asks first.
 */
function withinTimesCeiling(
  cfg: BackupScheduleConfig,
  bounds: BackupScheduleResponse['bounds'],
): BackupScheduleConfig {
  if ((cfg.times?.length ?? 0) <= bounds.times_max) return cfg
  return { ...cfg, times: cfg.times?.slice(0, bounds.times_max) }
}

/**
 * The draft the editor opens on: the stored row over the ENV baseline the
 * agent publishes, over the fallback above. That order IS the layering
 * (INV-173) — the environment decides the agenda until a row overrides it, so
 * an owner who saves without touching anything writes their own environment's
 * agenda back rather than this screen's opinion of a good one.
 *
 * The result is deliberately FAT. A stored row states only the mode it uses,
 * and seeding the draft from it verbatim left the other half empty: a disabled
 * ZIP toggled back on carried no day and no time at all, which is a guaranteed
 * 400. What the row does not state comes from the baseline, then from the
 * fallback — `payloadOf` is what trims the document on the way out, so the
 * extra half costs nothing on the wire.
 */
export function seedDraft(
  job: BackupJob,
  stored: BackupScheduleConfig | null,
  baseline: BackupScheduleConfig | null | undefined,
  bounds: BackupScheduleResponse['bounds'],
): BackupScheduleConfig {
  const fallback = fallbackFor(job)
  const env = baseline?.mode
    ? { ...fallback, ...withinWeekdayFloor(job, baseline, bounds) }
    : null
  // A report whose baseline carries no mode is an env agenda that is OFF, and
  // only user_zip may be: opening its switch on would propose turning a job on
  // as the effect of merely looking at it.
  const seed = stored
    ? { ...(env ?? fallback), ...stored }
    : (env ?? (baseline && job === 'user_zip' ? { ...fallback, enabled: false } : fallback))
  return withinTimesCeiling(seed, bounds)
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
  jobs,
  rows,
  bounds,
  report,
  agentSeen,
  isPending,
  isError,
  isOwner,
  cardRef,
}: {
  selected: BackupJob
  onSelect: (job: BackupJob) => void
  /* The pieces of the response this card reads, never the response itself:
     `agent.seen_at` advances on every ~30 s heartbeat, so the whole document
     is a new reference on every poll while these are not — and the memo above
     is what keeps a live form from re-rendering under the owner's cursor once
     a minute. The agent's timestamp is read where the banner needs it. */
  jobs: BackupJob[] | undefined
  rows: Record<string, BackupScheduleRow> | undefined
  bounds: BackupScheduleResponse['bounds'] | undefined
  report: BackupAgentJobReport | null
  agentSeen: boolean
  isPending: boolean
  isError: boolean
  isOwner: boolean
  /* A ref object, not a callback: its identity is stable, so it does not
     defeat the memo the way an inline closure would. */
  cardRef: RefObject<HTMLDivElement | null>
}) {
  const { t } = useTranslation()

  // Both placeholders go through the same shell as the loaded card: the slot
  // is a reveal target in every state (a job card clicked during the first
  // load must still land on it) and the column must keep its shape while it
  // fills. Stated once, so a fourth return cannot quietly lose either.
  if (isPending) {
    return (
      <AgendaShell cardRef={cardRef}>
        <div className="fx-empty">{t('common.loading')}</div>
      </AgendaShell>
    )
  }
  if (isError || !jobs || !rows || !bounds) {
    return (
      <AgendaShell cardRef={cardRef}>
        <div className="fx-empty">{t('admin.backup_schedule_unavailable')}</div>
      </AgendaShell>
    )
  }

  const row = rows[selected] ?? null

  return (
    <AgendaShell cardRef={cardRef}>
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
        {jobs.map((job) => (
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
        // A saved or reset row remounts the editor so its draft reseeds from
        // what the server now holds, instead of a stale local copy — and so
        // does the ARRIVAL of the env baseline, because the first successful
        // fetch can land before the agent's first heartbeat and seed the
        // draft from this screen's fallback. Once a baseline is present its
        // mode is stable, so the poll that only refreshes the heartbeat does
        // not throw a half-typed agenda away.
        key={`${selected}:${row?.updated_at ?? 'baseline'}:${report?.baseline?.mode ?? 'none'}`}
        job={selected}
        row={row}
        report={report}
        agentSeen={agentSeen}
        bounds={bounds}
        isOwner={isOwner}
      />
    </AgendaShell>
  )
})

/**
 * The agenda slot itself: the grid cell, and the element `useRevealTarget`
 * focuses and scrolls to. `tabIndex={-1}` is what makes a div focusable at
 * all, and `.fx-bkp-agenda` is both the equal-height rule and the focus ring.
 */
function AgendaShell({
  cardRef,
  children,
}: {
  cardRef: RefObject<HTMLDivElement | null>
  children: ReactNode
}) {
  return (
    <div className="fx-card fx-bkp-agenda" ref={cardRef} tabIndex={-1}>
      <div className="fx-card-body">{children}</div>
    </div>
  )
}

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
    seedDraft(job, stored, report?.baseline, bounds),
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
/**
 * The controls, one component per control group. They collapsed into a single
 * 240-line function when the four job vocabularies became one, which read as
 * progress and was not: the pickers share nothing but the draft they edit, and
 * a switch, a radiogroup, a weekday set, a times list and an interval are five
 * different widgets with five different keyboard contracts.
 *
 * `ScheduleFields` is now only the composition — which pickers this job and
 * this mode call for.
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
  const enabled = config.enabled !== false
  const mode = config.mode === 'interval' ? 'interval' : 'times'

  return (
    <>
      {job === 'user_zip' && <EnabledSwitch enabled={enabled} config={config} onChange={onChange} />}
      {enabled && (
        <>
          <ModePicker mode={mode} config={config} onChange={onChange} />
          {mode === 'times' ? (
            <>
              <WeekdayPicker
                config={config}
                onChange={onChange}
                floor={weekdayFloorFor(job, bounds)}
              />
              <TimesPicker config={config} onChange={onChange} bounds={bounds} />
            </>
          ) : (
            <IntervalPicker config={config} onChange={onChange} bounds={bounds} />
          )}
        </>
      )}
    </>
  )
}

/** What every picker receives: the draft, and the way to replace it. */
type PickerProps = {
  config: BackupScheduleConfig
  onChange: (config: BackupScheduleConfig) => void
}

/** The one job a row may switch off, so the only one with a switch. */
function EnabledSwitch({ enabled, config, onChange }: PickerProps & { enabled: boolean }) {
  const { t } = useTranslation()
  return (
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
  )
}

/**
 * Wall times or an interval. A radiogroup, not a tablist: two mutually
 * exclusive options that swap this form's own fields are a choice, and tabs
 * with no panel and no `aria-controls` announce navigation that goes nowhere.
 * The JOB picker above really is a tablist.
 */
function ModePicker({ mode, config, onChange }: PickerProps & { mode: 'times' | 'interval' }) {
  const { t } = useTranslation()

  // Only the mode moves: the draft already carries both halves (`seedDraft`
  // is what fills them, from the env baseline rather than from a default
  // invented here), and `payloadOf` is what drops the unused one, so a
  // round-trip through the other mode never costs the owner their edits.
  const switchMode = (next: 'times' | 'interval') => onChange({ ...config, mode: next })

  return (
    <div className="fx-bkp-control">
      <span className="fx-bkp-control-label">{t('admin.backup_schedule_mode_label')}</span>
      <div
        className="fx-bkp-modes"
        role="radiogroup"
        aria-label={t('admin.backup_schedule_mode_label')}
      >
        {MODES.map((m, i) => (
          <button
            key={m}
            type="button"
            role="radio"
            aria-checked={mode === m}
            /* Roving tabindex: a radiogroup is ONE tab stop, and the arrow
               keys move within it. Without this the role promises a keyboard
               behaviour the buttons do not have. */
            tabIndex={mode === m ? 0 : -1}
            className={'fx-bkp-mode' + (mode === m ? ' fx-bkp-mode-on' : '')}
            onClick={() => switchMode(m)}
            onKeyDown={(e) => {
              const step = ARROW_STEP[e.key]
              if (step === undefined) return
              e.preventDefault()
              // Wraps in both directions, as the pattern requires.
              const next = MODES[(i + step + MODES.length) % MODES.length]
              switchMode(next)
              e.currentTarget.parentElement
                ?.querySelectorAll<HTMLButtonElement>('[role="radio"]')
                [MODES.indexOf(next)]?.focus()
            }}
          >
            {t(`admin.backup_schedule_mode_${m}`)}
          </button>
        ))}
      </div>
    </div>
  )
}

/** The weekday set — multi-select, with the two shortcuts worth having. */
function WeekdayPicker({ config, onChange, floor }: PickerProps & { floor: number }) {
  const { t } = useTranslation()

  function toggleWeekday(day: string) {
    const picked = new Set(config.weekdays ?? [])
    if (picked.has(day)) picked.delete(day)
    else picked.add(day)
    // Rebuilt from the vocabulary, never from click order: the stored document
    // reads the same whichever way the owner assembled it.
    onChange({ ...config, weekdays: WEEKDAYS.filter((d) => picked.has(d)) })
  }

  return (
    <div className="fx-bkp-control">
      <span className="fx-bkp-control-label">{t('admin.backup_schedule_weekdays_label')}</span>
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
          onClick={() => onChange({ ...config, weekdays: [...WORKWEEK] })}
        >
          {t('admin.backup_schedule_weekdays_workweek')}
        </button>
      </div>
      {/* A floor of one is what "pick some days" already means; only a job
          that demands more than that has something to say. */}
      {floor > 1 && (
        <span className="fx-bkp-control-hint">
          {t('admin.backup_schedule_weekdays_floor_hint', { min: floor })}
        </span>
      )}
    </div>
  )
}

/** The daily wall times, between the server's floor and ceiling. */
function TimesPicker({
  config,
  onChange,
  bounds,
}: PickerProps & { bounds: BackupScheduleResponse['bounds'] }) {
  const { t } = useTranslation()
  // Bounded by construction: `seedDraft` trims a hand-written row to the
  // server's ceiling and the add button below disappears at it.
  const times = config.times ?? []

  return (
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
                onClick={() => onChange({ ...config, times: times.filter((_, j) => j !== i) })}
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
            onClick={() => onChange({ ...config, times: [...times, ADDED_TIME] })}
          >
            <span aria-hidden="true">+</span> {t('admin.backup_schedule_add_time')}
          </button>
        )}
      </div>
    </div>
  )
}

/** An interval in minutes, typed or picked. */
function IntervalPicker({
  config,
  onChange,
  bounds,
}: PickerProps & { bounds: BackupScheduleResponse['bounds'] }) {
  const { t } = useTranslation()
  return (
    <div className="fx-bkp-control">
      <span className="fx-bkp-control-label">{t('admin.backup_schedule_interval_label')}</span>
      <div className="fx-bkp-interval">
        <span className="fx-bkp-interval-field">
          <input
            className="fx-bkp-input"
            type="number"
            min={bounds.interval_min}
            max={bounds.interval_max}
            value={config.interval_min ?? 0}
            aria-label={t('admin.backup_schedule_interval_label')}
            /* Coerced here: the server's bounds check runs on a NUMBER, and a
               string would be refused by the JSON schema, not by the floor. */
            onChange={(e) => onChange({ ...config, interval_min: Number(e.target.value) })}
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
  )
}
