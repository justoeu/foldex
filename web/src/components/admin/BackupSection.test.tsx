import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BackupSection } from './BackupSection'
import {
  makeQueryClient,
  renderWithProviders,
  testAdminSession,
  testAdminUser,
} from '../../test/renderWithProviders'
import { backupScheduleQueryKey } from '../../api/admin'
import { freshState, installAxiosMock, type MockState } from '../../test/server'
import type { SessionState } from '../../auth/types'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})
afterEach(() => vi.restoreAllMocks())

const HOUR = 60 * 60 * 1000

/** One backup_run row with every nullable field null unless overridden. */
function run(over: Record<string, unknown>) {
  return {
    id: 1,
    job: 'dump',
    status: 'succeeded',
    scheduled_for: new Date(Date.now() - HOUR).toISOString(),
    started_at: new Date(Date.now() - HOUR).toISOString(),
    finished_at: new Date(Date.now() - HOUR + 9500).toISOString(),
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

function jobsSummary(
  dumpLastSuccess: Record<string, unknown> | null,
  extra: Record<string, Record<string, unknown>> = {},
) {
  return [
    { job: 'dump', last_success: dumpLastSuccess, consecutive_failures: 0 },
    { job: 'drill', last_success: null, consecutive_failures: 0 },
    { job: 'mirror', last_success: null, consecutive_failures: 0 },
    { job: 'user_zip', last_success: null, consecutive_failures: 0 },
  ].map((j) => ({ ...j, ...extra[j.job] }))
}

describe('BackupSection', () => {
  it('renders one row per job and the honest empty state when nothing ever ran', async () => {
    renderWithProviders(<BackupSection />)

    // One card per job, plus the agenda tab that mirrors it.
    expect(await screen.findAllByText('Database dump')).not.toHaveLength(0)
    expect(screen.getAllByText('Restore drill')).not.toHaveLength(0)
    expect(screen.getAllByText('Object mirror')).not.toHaveLength(0)
    expect(screen.getAllByText('Per-user ZIPs')).not.toHaveLength(0)
    expect(document.querySelectorAll('.fx-bkp-job')).toHaveLength(4)

    // Never ran + no history = the service is off, and the band says so
    // instead of looking healthy (the mailer incident's lesson).
    expect(screen.getByText('The backup service is not active')).toBeInTheDocument()
    expect(screen.getAllByText('COMPOSE_PROFILES=backup').length).toBeGreaterThan(0)
    // One "never ran" chip per job card, plus the two KPI hints that have no
    // run to describe.
    expect(screen.getAllByText('never ran').length).toBeGreaterThanOrEqual(4)
    expect(screen.getByText('No runs recorded.')).toBeInTheDocument()
  })

  it('marks a dump older than 26h as stale, and a fresh one not', async () => {
    const fresh = run({ id: 10, started_at: new Date(Date.now() - 2 * HOUR).toISOString() })
    state.backupJobs = jobsSummary(fresh)
    state.backupStatusRuns = [fresh]

    const { unmount } = renderWithProviders(<BackupSection />)
    await screen.findAllByText(/2h/)
    const freshCell = document.querySelector('.fx-bkp-metric-value')
    expect(freshCell?.className).not.toContain('fx-bkp-stale')
    expect(screen.queryByText(/older than 26h/)).not.toBeInTheDocument()
    unmount()

    const stale = run({ id: 11, started_at: new Date(Date.now() - 30 * HOUR).toISOString() })
    state.backupJobs = jobsSummary(stale)
    state.backupStatusRuns = [stale]
    renderWithProviders(<BackupSection />)
    const staleCell = (await screen.findByText(/older than 26h/)).closest('span')
    expect(staleCell?.className).toContain('fx-bkp-stale')
  })

  it('asks for confirmation before POSTing, and a cancel sends nothing', async () => {
    const user = userEvent.setup()
    renderWithProviders(<BackupSection />)

    const buttons = await screen.findAllByRole('button', { name: 'Run now' })
    expect(buttons).toHaveLength(4)
    await user.click(buttons[0])

    // The dialog names the job it is about to queue (INV-122).
    expect(await screen.findByText('Run “Database dump” now?')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(state.backupRunRequests ?? []).toHaveLength(0)

    await user.click(screen.getAllByRole('button', { name: 'Run now' })[0])
    await user.click(await screen.findByRole('button', { name: 'Queue run' }))

    await waitFor(() => expect(state.backupRunRequests).toEqual(['dump']))
    // The refetch shows the enqueued row as the trigger's only feedback.
    expect(await screen.findByText('requested')).toBeInTheDocument()
  })

  it('renders the 409 as its verbatim code when a run is already pending', async () => {
    const user = userEvent.setup()
    state.backupStatusRuns = [run({ id: 5, status: 'requested', finished_at: null })]

    renderWithProviders(<BackupSection />)
    await user.click((await screen.findAllByRole('button', { name: 'Run now' }))[0])
    await user.click(await screen.findByRole('button', { name: 'Queue run' }))

    expect(await screen.findByText('Could not enqueue the run')).toBeInTheDocument()
    const code = screen.getByText('backup_run_pending')
    expect(code.tagName).toBe('CODE')
  })

  it('shows the normalized failure reason as code in the history', async () => {
    state.backupStatusRuns = [run({ id: 7, status: 'failed', last_error: 'upload_failed' })]

    renderWithProviders(<BackupSection />)
    const reason = await screen.findByText('upload_failed')
    expect(reason.tagName).toBe('CODE')
    expect(screen.getByText('failed')).toBeInTheDocument()
  })

  it('warns when a requested run has aged past five minutes without a claim', async () => {
    state.backupStatusRuns = [
      run({
        id: 8,
        status: 'requested',
        finished_at: null,
        scheduled_for: new Date(Date.now() - 10 * 60 * 1000).toISOString(),
      }),
    ]

    renderWithProviders(<BackupSection />)
    expect(await screen.findByText('A requested run is not being picked up')).toBeInTheDocument()
  })

  it('does not warn about a requested run younger than five minutes', async () => {
    state.backupStatusRuns = [
      run({
        id: 9,
        status: 'requested',
        finished_at: null,
        scheduled_for: new Date().toISOString(),
      }),
    ]

    renderWithProviders(<BackupSection />)
    await screen.findByText('requested')
    expect(screen.queryByText('A requested run is not being picked up')).not.toBeInTheDocument()
  })

  it('highlights the last drill with the dump it validated and the restored counts', async () => {
    const dump = run({
      id: 21,
      artifact_key: 'foldex/dump/2026-08-26.dump.age',
      artifact_bytes: 4321,
      artifact_sha256: 'deadbeefcafe0123456789',
    })
    const drill = run({
      id: 22,
      job: 'drill',
      drill_of_run_id: 21,
      meta: { tables: { link: 42, note: 7 }, schema_version: 41 },
    })
    state.backupJobs = jobsSummary(dump, { drill: { last_success: drill } })
    state.backupStatusRuns = [drill, dump]

    renderWithProviders(<BackupSection />)
    expect(await screen.findByText('Last restore drill')).toBeInTheDocument()
    expect(screen.getByText('Dump #21 validated successfully')).toBeInTheDocument()
    // Table and count are separate elements so the number can carry its own
    // weight; the chip still reads "link 42".
    const counts = [...document.querySelectorAll('.fx-bkp-count')].map((n) => n.textContent)
    expect(counts).toContain('link42')
    expect(counts).toContain('note7')

    // The artifact row renders truncated but titled with the full key, and the
    // SHA is copiable behind a visible label (INV-151).
    expect(screen.getAllByTitle('foldex/dump/2026-08-26.dump.age').length).toBeGreaterThan(0)
    expect(screen.getByRole('button', { name: /Copy/ })).toBeInTheDocument()
  })
})

describe('BackupSection layout', () => {
  /*
   * The four headline numbers are values the API answered — nothing here is
   * derived from a window the query does not have. "Retained artifacts" and
   * "failures in 7 days" are deliberately absent for exactly that reason.
   */
  it('summarises the instance with numbers the server actually returned', async () => {
    const dump = run({ id: 30, artifact_bytes: 134_000 })
    const drill = run({ id: 31, job: 'drill', drill_of_run_id: 30, meta: { tables: { link: 42, note: 7 } } })
    state.backupJobs = jobsSummary(dump, {
      drill: { last_success: drill },
      // Two jobs failing, with DIFFERENT counts: a regression from
      // max-across-jobs to sum-across-jobs would read 5 here, not 3.
      mirror: { consecutive_failures: 3 },
      user_zip: { consecutive_failures: 2 },
    })
    state.backupStatusRuns = [drill, dump]

    renderWithProviders(<BackupSection />)

    expect(await screen.findByText('Last dump')).toBeInTheDocument()
    expect(screen.getAllByText('131 KB').length).toBeGreaterThan(0)
    expect(screen.getByText('drill validated 2 tables')).toBeInTheDocument()
    // The failure KPI is the longest streak across jobs — the same number the
    // alert rule compares against.
    expect(screen.getByText('Consecutive failures')).toBeInTheDocument()
    expect(document.querySelectorAll('.fx-bkp-kpi-value')[3]).toHaveTextContent('3')
  })

  it('moves the agenda to whichever job card is selected', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()
    renderWithProviders(<BackupSection />)

    expect(await screen.findByText('Database dump schedule')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Show the schedule for Object mirror' }))
    expect(await screen.findByText('Object mirror schedule')).toBeInTheDocument()
    // The card and the tab are one selection, so both report it.
    expect(screen.getByRole('tab', { name: 'Object mirror' })).toHaveAttribute(
      'aria-selected',
      'true',
    )

    // …and the binding runs the other way too: choosing a tab rings its card.
    await user.click(screen.getByRole('tab', { name: 'Per-user ZIPs' }))
    expect(await screen.findByText('Per-user ZIPs schedule')).toBeInTheDocument()
    const selectedCard = document.querySelector('.fx-bkp-job-on')
    expect(selectedCard?.textContent).toContain('Per-user ZIPs')
  })

  /*
   * The job cards sit above the fold and the agenda below it, so a click that
   * only changed state moved a form the reader could not see. The card brings
   * the agenda to them; a TAB inside the agenda does not, because the click
   * already happened in that card and moving it under the cursor is gratuitous.
   */
  it('brings the agenda into view from a job CARD, and leaves it alone from a TAB', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()
    renderWithProviders(<BackupSection />)
    await screen.findByText('Database dump schedule')

    const agenda = document.querySelector('.fx-bkp-agenda')!
    // jsdom implements no scrollIntoView, so assigning it is what makes the
    // call observable at all (same shape as useDialogInitialFocus.test.tsx).
    const scrollIntoView = vi.fn()
    ;(agenda as unknown as { scrollIntoView: () => void }).scrollIntoView = scrollIntoView
    expect(agenda).toHaveAttribute('tabindex', '-1')

    await user.click(screen.getByRole('button', { name: 'Show the schedule for Object mirror' }))
    await waitFor(() => expect(scrollIntoView).toHaveBeenCalled())
    expect(scrollIntoView).toHaveBeenCalledWith({
      block: 'center',
      behavior: expect.stringMatching(/^(auto|smooth)$/),
    })

    scrollIntoView.mockClear()
    await user.click(screen.getByRole('tab', { name: 'Per-user ZIPs' }))
    await new Promise((resolve) => requestAnimationFrame(resolve))
    expect(scrollIntoView).not.toHaveBeenCalled()
  })

  it('filters the history without asking the server for a different page', async () => {
    const user = userEvent.setup()
    const ok = run({ id: 40, status: 'succeeded' })
    const bad = run({ id: 41, job: 'mirror', status: 'failed', last_error: 'upload_failed' })
    state.backupStatusRuns = [bad, ok]

    renderWithProviders(<BackupSection />)
    expect(await screen.findByText('2 runs on this page.')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Failed' }))
    expect(screen.getByText('upload_failed')).toBeInTheDocument()
    expect(screen.queryByText('succeeded')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Succeeded' }))
    expect(screen.queryByText('upload_failed')).not.toBeInTheDocument()
    // The count keeps describing the PAGE, not the filter — it is what the
    // keyset query returned, and the filter is a local view of it.
    expect(screen.getByText('2 runs on this page.')).toBeInTheDocument()
  })
})

/** The owner — the only role the schedule editors render for. */
const ownerSession: SessionState = {
  ...testAdminSession,
  user: { ...testAdminUser, role: 'owner' },
} as SessionState

const ALL_DAYS = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat']

/**
 * A healthy heartbeat, fresh, every job capable, all on the env baseline —
 * which each report now carries STRUCTURED, because it is what the editor
 * seeds from when no row is stored.
 */
function healthyAgent(over: Record<string, unknown> = {}) {
  return {
    seen_at: new Date(Date.now() - 30_000).toISOString(),
    version: '2.10.1',
    jobs: {
      dump: {
        capable: true, source: 'env', schedule: '03:30',
        baseline: { mode: 'times', times: ['03:30'], weekdays: ALL_DAYS },
      },
      drill: {
        capable: true, source: 'env', schedule: '01:00 sun',
        baseline: { mode: 'times', times: ['01:00'], weekdays: ['sun'] },
      },
      mirror: {
        capable: true, source: 'env', schedule: 'every 360m',
        baseline: { mode: 'interval', interval_min: 360 },
      },
      user_zip: {
        capable: true, source: 'env', schedule: '02:30',
        baseline: { mode: 'times', times: ['02:30'], weekdays: ALL_DAYS },
      },
    },
    ...over,
  }
}


/**
 * The agenda editor speaks ONE vocabulary for all four jobs (ADR-44): a mode
 * (times or interval), a multi-select of weekdays, a list of wall times, an
 * interval — and, for user_zip alone, an off switch. What still differs per
 * job is only the floor the SERVER enforces, which this screen renders as a
 * hint and never re-derives (INV-138 by analogy).
 */
describe('BackupSection schedule', () => {
  it('renders the effective agenda from the agent report with a source badge per job', async () => {
    state.backupAgent = healthyAgent({
      jobs: {
        ...healthyAgent().jobs,
        drill: {
          capable: true, source: 'db', schedule: '04:00 wed',
          baseline: { mode: 'times', times: ['01:00'], weekdays: ['sun'] },
        },
      },
    })
    state.backupScheduleRows = {
      drill: {
        job: 'drill',
        config: { mode: 'times', times: ['04:00'], weekdays: ['wed'] },
        updated_at: '2026-08-01T00:00:00Z',
        updated_by_email: 'owner@foldex.test',
      },
    }

    renderWithProviders(<BackupSection />)

    const user = userEvent.setup()
    expect(await screen.findByText(/Agent seen/)).toBeInTheDocument()
    expect(screen.getByText(/version 2\.10\.1/)).toBeInTheDocument()
    // The agenda strings the agent reported, verbatim — on the job card and
    // again in the agent panel.
    expect(screen.getAllByText('03:30').length).toBeGreaterThan(0)
    expect(screen.getAllByText('04:00 wed').length).toBeGreaterThan(0)
    expect(screen.getAllByText('every 360m').length).toBeGreaterThan(0)

    // The origin badge belongs to the job on screen: dump is on the env
    // baseline, and switching to the drill shows the stored row's origin.
    expect(screen.getByText('env default')).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: 'Restore drill' }))
    expect(await screen.findByText('configured here')).toBeInTheDocument()
  })

  it('shows the translated reason and no editors for a job the agent cannot run', async () => {
    state.backupAgent = healthyAgent({
      jobs: {
        ...healthyAgent().jobs,
        mirror: {
          capable: false, reason: 'mirror_off', source: 'env', schedule: 'every 360m',
          // An env agenda that is off answers `{}` — no mode, nothing claimed.
          baseline: {},
        },
      },
    })

    renderWithProviders(<BackupSection />, { session: ownerSession })

    const user = userEvent.setup()
    // The dump is editable…
    expect(await screen.findByRole('button', { name: 'Save schedule' })).toBeInTheDocument()

    // …and the mirror, which the agent says it cannot run, is not: its reason
    // is shown instead of controls that would store a row no process reads.
    await user.click(screen.getByRole('tab', { name: 'Object mirror' }))
    // Twice on purpose: once where the agenda would be edited, once in the
    // agent panel that reports what the process can actually do.
    expect((await screen.findAllByText('the mirror source is not configured')).length).toBe(2)
    expect(screen.queryByRole('button', { name: 'Save schedule' })).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Interval (minutes)')).not.toBeInTheDocument()
  })

  it('lets the owner pre-configure the agenda before any agent ever reported', async () => {
    // No heartbeat at all is NOT the same as "cannot run": the owner sets
    // the agenda first and enables the backup profile after. A stricter
    // gate here would break that ordering.
    state.backupAgent = undefined

    const user = userEvent.setup()
    renderWithProviders(<BackupSection />, { session: ownerSession })

    expect(await screen.findByRole('button', { name: 'Save schedule' })).toBeInTheDocument()
    // Every job, not just the one that happens to be selected first.
    for (const job of ['Restore drill', 'Object mirror', 'Per-user ZIPs']) {
      await user.click(screen.getByRole('tab', { name: job }))
      expect(await screen.findByRole('button', { name: 'Save schedule' })).toBeInTheDocument()
    }
  })

  it('treats a job missing from a live report as not runnable — no editors', async () => {
    // An older agent build that never heard of a job reports nothing for it.
    // With a live heartbeat, silence means "will not run", not "go ahead":
    // editors here would store a row no process reads (the mailer lesson).
    const jobs: any = { ...healthyAgent().jobs }
    delete jobs.mirror
    state.backupAgent = healthyAgent({ jobs })

    const user = userEvent.setup()
    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Object mirror' }))
    expect((await screen.findAllByText('no agent report for this job')).length).toBeGreaterThan(1)
    expect(screen.queryByRole('button', { name: 'Save schedule' })).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Interval (minutes)')).not.toBeInTheDocument()
  })

  /*
   * The `.env` is the first option and a row is the override (INV-173). The
   * editor used to open on hardcoded constants, so an owner who saved without
   * touching anything silently REPLACED their environment's agenda with the
   * screen's opinion of a good one.
   */
  it('seeds the draft from the env baseline the agent publishes, not from a constant', async () => {
    const user = userEvent.setup()
    const jobs: any = { ...healthyAgent().jobs }
    jobs.dump = {
      capable: true, source: 'env', schedule: '04:20, 16:20 mon,wed,fri,sat,sun',
      baseline: { mode: 'times', times: ['04:20', '16:20'], weekdays: ['sun', 'mon', 'wed', 'fri', 'sat'] },
    }
    state.backupAgent = healthyAgent({ jobs })

    renderWithProviders(<BackupSection />, { session: ownerSession })

    expect(await screen.findByLabelText('Time 1')).toHaveValue('04:20')
    expect(screen.getByLabelText('Time 2')).toHaveValue('16:20')
    expect(screen.getByRole('button', { name: 'Sunday' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Tuesday' })).toHaveAttribute('aria-pressed', 'false')

    // Saving an untouched draft writes the environment's own agenda back —
    // never a default the screen invented.
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        {
          job: 'dump',
          config: {
            mode: 'times',
            times: ['04:20', '16:20'],
            weekdays: ['sun', 'mon', 'wed', 'fri', 'sat'],
          },
        },
      ]),
    )
  })

  it('lets the owner add a time and PUTs the whole unified document', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('button', { name: 'Add time' }))
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))

    // More dumps than today — no confirmation stands between click and PUT.
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        {
          job: 'dump',
          config: { mode: 'times', times: ['03:30', '12:00'], weekdays: ALL_DAYS },
        },
      ]),
    )
    expect(screen.queryByText(/Reduce “Database dump” protection\?/)).not.toBeInTheDocument()
  })

  /*
   * Every job gets BOTH shapes now. The mirror used to be the only one with an
   * interval and the dump the only one with times; a weekly mirror and a
   * six-hourly dump were both unsayable.
   */
  it('switches a job between times and interval, keeping what the other mode held', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    // The dump opens on its env baseline, which is a times agenda…
    expect(await screen.findByLabelText('Time 1')).toBeInTheDocument()
    await user.click(screen.getByRole('radio', { name: 'Interval' }))
    expect(screen.queryByLabelText('Time 1')).not.toBeInTheDocument()
    expect(screen.getByLabelText('Interval (minutes)')).toBeInTheDocument()

    // …and coming back restores the times the draft was holding, rather than
    // the screen's default.
    await user.click(screen.getByRole('radio', { name: 'Times' }))
    expect(screen.getByLabelText('Time 1')).toHaveValue('03:30')

    await user.click(screen.getByRole('radio', { name: 'Interval' }))
    await user.click(screen.getByRole('button', { name: '6h' }))
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))

    // The payload carries the chosen mode and NOTHING from the other one: an
    // agenda no process reads is the defect this shape exists to kill.
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        { job: 'dump', config: { mode: 'interval', interval_min: 360 } },
      ]),
    )
  })

  it('lets the mirror run on wall times, which it never could before', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Object mirror' }))
    // The mirror's env baseline is an interval, so it opens there.
    expect(screen.getByLabelText('Interval (minutes)')).toHaveValue(360)
    await user.click(screen.getByRole('radio', { name: 'Times' }))
    await user.click(screen.getByRole('button', { name: 'Weekdays' }))
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))

    // 1 time × 5 days is far below 28 firings a week, so it confirms first.
    await user.click(await screen.findByRole('button', { name: 'Apply schedule' }))
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        {
          job: 'mirror',
          config: { mode: 'times', times: ['03:30'], weekdays: ['mon', 'tue', 'wed', 'thu', 'fri'] },
        },
      ]),
    )
  })

  it('toggles weekdays on and off — it is a multi-select, not a radio', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Restore drill' }))
    const sunday = screen.getByRole('button', { name: 'Sunday' })
    const wednesday = screen.getByRole('button', { name: 'Wednesday' })
    expect(sunday).toHaveAttribute('aria-pressed', 'true')
    expect(wednesday).toHaveAttribute('aria-pressed', 'false')

    // "mon, wed and fri" is the whole point: a second day must not unpick the
    // first the way the old single-select did.
    await user.click(wednesday)
    expect(sunday).toHaveAttribute('aria-pressed', 'true')
    expect(wednesday).toHaveAttribute('aria-pressed', 'true')

    await user.click(sunday)
    expect(sunday).toHaveAttribute('aria-pressed', 'false')

    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    // Written in the server's own sun-first order, never in click order.
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        { job: 'drill', config: { mode: 'times', times: ['01:00'], weekdays: ['wed'] } },
      ]),
    )
  })

  it('fills the week from the shortcuts', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Restore drill' }))
    await user.click(screen.getByRole('button', { name: 'Every day' }))
    for (const day of ['Sunday', 'Monday', 'Saturday']) {
      expect(screen.getByRole('button', { name: day })).toHaveAttribute('aria-pressed', 'true')
    }

    await user.click(screen.getByRole('button', { name: 'Weekdays' }))
    expect(screen.getByRole('button', { name: 'Saturday' })).toHaveAttribute('aria-pressed', 'false')
    expect(screen.getByRole('button', { name: 'Monday' })).toHaveAttribute('aria-pressed', 'true')

    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        {
          job: 'drill',
          config: { mode: 'times', times: ['01:00'], weekdays: ['mon', 'tue', 'wed', 'thu', 'fri'] },
        },
      ]),
    )
  })

  /*
   * The dump's floor is five days a week and every other job's is one. The
   * hint states the server's number; the refusal itself stays the server's.
   */
  it('states the dump weekday floor and states no floor where there is none', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    expect(await screen.findByText('At least 5 days a week for this job.')).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: 'Restore drill' }))
    expect(screen.queryByText(/At least .* days a week/)).not.toBeInTheDocument()
  })

  it('asks for confirmation when the change reduces protection, and only writes after it', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()
    state.backupScheduleRows = {
      dump: {
        job: 'dump',
        config: { mode: 'times', times: ['03:30', '15:30'], weekdays: ALL_DAYS },
        updated_at: '2026-08-01T00:00:00Z',
        updated_by_email: 'owner@foldex.test',
      },
    }

    renderWithProviders(<BackupSection />, { session: ownerSession })

    // Two stored times seed the editor; dropping one halves the firings.
    await user.click(await screen.findByRole('button', { name: 'Remove time 2' }))
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))

    expect(await screen.findByText('Reduce “Database dump” protection?')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(state.backupSchedulePuts ?? []).toHaveLength(0)

    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await user.click(await screen.findByRole('button', { name: 'Apply schedule' }))
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        { job: 'dump', config: { mode: 'times', times: ['03:30'], weekdays: ALL_DAYS } },
      ]),
    )
  })

  it('writes the typed interval as a number, not the input string', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Object mirror' }))
    fireEvent.change(screen.getByLabelText('Interval (minutes)'), { target: { value: '45' } })
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))

    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        { job: 'mirror', config: { mode: 'interval', interval_min: 45 } },
      ]),
    )
    // A string would pass the server's JSON decode and fail its bounds check —
    // the coercion is the contract.
    expect(typeof state.backupSchedulePuts![0].config.interval_min).toBe('number')
  })

  it('turning the per-user ZIP off writes a bare enabled:false and drops the agenda', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Per-user ZIPs' }))
    // On by default, and the whole agenda goes with the switch.
    expect(screen.getByLabelText('Time 1')).toBeInTheDocument()
    await user.click(screen.getByRole('checkbox', { name: 'Generate per-user ZIPs' }))
    expect(screen.queryByLabelText('Time 1')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    // Switching a job OFF reduces protection, so it is behind the confirmation.
    await user.click(await screen.findByRole('button', { name: 'Apply schedule' }))
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        { job: 'user_zip', config: { mode: 'times', enabled: false } },
      ]),
    )
  })

  it('offers the off switch to user_zip and to nothing else', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    // The other three are the instance's floor: the server refuses to switch
    // them off, so the screen never offers it (INV-173).
    expect(await screen.findByRole('button', { name: 'Save schedule' })).toBeInTheDocument()
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: 'Per-user ZIPs' }))
    expect(screen.getByRole('checkbox', { name: 'Generate per-user ZIPs' })).toBeInTheDocument()
  })

  /*
   * The switch keeps the agenda the owner typed. What reaches the server is a
   * bare `{mode, enabled:false}` — a time on a disabled job is an agenda
   * nothing reads — but toggling back on must return their own times, not the
   * default, or the switch quietly discards their edit.
   */
  it('keeps the typed ZIP agenda across an off/on toggle', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Per-user ZIPs' }))
    fireEvent.change(screen.getByLabelText('Time 1'), { target: { value: '04:45' } })

    const toggle = screen.getByRole('checkbox', { name: 'Generate per-user ZIPs' })
    await user.click(toggle)
    await user.click(toggle)

    expect(screen.getByLabelText('Time 1')).toHaveValue('04:45')
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        {
          job: 'user_zip',
          config: { mode: 'times', times: ['04:45'], weekdays: ALL_DAYS, enabled: true },
        },
      ]),
    )
  })

  it('renders the server message of a 400 invalid_schedule verbatim', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    // A second dump time equal to the first — the mock refuses it the way
    // backupagent.ValidateJobConfig does, and the band shows THAT message.
    await user.click(await screen.findByRole('button', { name: 'Add time' }))
    fireEvent.change(screen.getByLabelText('Time 2'), { target: { value: '03:30' } })
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))

    expect(await screen.findByText('dump time "03:30" repeats')).toBeInTheDocument()
    expect(state.backupSchedulePuts ?? []).toHaveLength(0)
  })

  /*
   * The client never enforces the weekday floor itself: unpicking the last day
   * is allowed, the PUT goes out, and the SERVER's own message is what says no.
   * Pinning a day client-side would be a second copy of a policy that lives in
   * one place — and it could not express the dump's floor of five anyway.
   */
  it('lets the server refuse an empty weekday set, in its own words', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Restore drill' }))
    await user.click(screen.getByRole('button', { name: 'Sunday' }))
    expect(screen.getByRole('button', { name: 'Sunday' })).toHaveAttribute('aria-pressed', 'false')

    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await user.click(await screen.findByRole('button', { name: 'Apply schedule' }))
    expect(
      await screen.findByText(
        'drill needs at least 1 weekday(s) — an agenda that fires on no day is the job switched off',
      ),
    ).toBeInTheDocument()
  })

  it('resets to the env baseline via DELETE, always behind a confirmation', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()
    state.backupScheduleRows = {
      mirror: {
        job: 'mirror',
        config: { mode: 'interval', interval_min: 60 },
        updated_at: '2026-08-01T00:00:00Z',
        updated_by_email: 'owner@foldex.test',
      },
    }

    renderWithProviders(<BackupSection />, { session: ownerSession })

    // The stored row belongs to the mirror, so the editor has to be on it.
    await user.click(await screen.findByRole('tab', { name: 'Object mirror' }))
    await user.click(await screen.findByRole('button', { name: 'Restore env default' }))
    expect(await screen.findByText('Reset “Object mirror” to the env default?')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(state.backupScheduleDeletes ?? []).toHaveLength(0)

    await user.click(screen.getByRole('button', { name: 'Restore env default' }))
    const dialogConfirm = await screen.findAllByRole('button', { name: 'Restore env default' })
    // The dialog's confirm button is the last rendered.
    await user.click(dialogConfirm[dialogConfirm.length - 1])
    await waitFor(() => expect(state.backupScheduleDeletes).toEqual(['mirror']))
  })

  it('shows the honest empty state when the agent never reported', async () => {
    renderWithProviders(<BackupSection />)

    expect(await screen.findByText('The agent never reported')).toBeInTheDocument()
    expect(screen.getAllByText('COMPOSE_PROFILES=backup').length).toBeGreaterThan(0)
    // No heartbeat = no per-job report to show, and the agenda says exactly
    // that rather than implying the env baseline is running.
    expect(screen.getAllByText('no agent report for this job').length).toBeGreaterThan(0)
  })

  it('warns when the heartbeat is older than two minutes', async () => {
    state.backupAgent = healthyAgent({
      seen_at: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
    })

    renderWithProviders(<BackupSection />)
    expect(await screen.findByText('The agent stopped reporting')).toBeInTheDocument()
  })

  it('renders no editing controls for a non-owner', async () => {
    state.backupAgent = healthyAgent()

    // The default test session is an admin — may read, never write (the
    // permission is owner-only and locked).
    renderWithProviders(<BackupSection />)

    expect((await screen.findAllByText('03:30')).length).toBeGreaterThan(0)
    expect(screen.queryByRole('button', { name: 'Save schedule' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Restore env default' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Add time' })).not.toBeInTheDocument()
    expect(screen.queryByRole('radio', { name: 'Interval' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Every day' })).not.toBeInTheDocument()
  })

  /** A stored row, with the boilerplate every one of them carries. */
  function storedRow(job: string, config: Record<string, unknown>) {
    return {
      [job]: {
        job,
        config,
        updated_at: '2026-08-01T00:00:00Z',
        updated_by_email: 'owner@foldex.test',
      },
    }
  }

  /*
   * The first successful fetch can land before the agent's first heartbeat, so
   * the editor opens on the last-resort draft. The baseline arriving later is
   * a NEW document to seed from — an editor that ignored it would let an owner
   * write this screen's opinion over their own env agenda by saving an
   * untouched form (INV-173).
   */
  it('reseeds the draft when the env baseline arrives after the first fetch', async () => {
    const client = makeQueryClient()
    state.backupAgent = undefined

    renderWithProviders(<BackupSection />, { session: ownerSession, client })
    expect(await screen.findByLabelText('Time 1')).toHaveValue('03:30')

    const jobs: any = { ...healthyAgent().jobs }
    jobs.dump = {
      capable: true, source: 'env', schedule: '04:20',
      baseline: { mode: 'times', times: ['04:20'], weekdays: ALL_DAYS },
    }
    state.backupAgent = healthyAgent({ jobs })
    await act(async () => {
      await client.invalidateQueries({ queryKey: backupScheduleQueryKey })
    })

    await waitFor(() => expect(screen.getByLabelText('Time 1')).toHaveValue('04:20'))
  })

  /*
   * The other half of the rule above: once a baseline is present its MODE is
   * what the editor keys on, and a heartbeat is written every ~30 s. Keying on
   * anything that moves with the clock would throw the owner's half-typed
   * agenda away once a minute.
   */
  it('keeps a half-typed draft across a poll that only refreshes the heartbeat', async () => {
    const client = makeQueryClient()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession, client })
    fireEvent.change(await screen.findByLabelText('Time 1'), { target: { value: '05:15' } })

    state.backupAgent = healthyAgent({ seen_at: new Date().toISOString(), version: '9.9.9' })
    await act(async () => {
      await client.invalidateQueries({ queryKey: backupScheduleQueryKey })
    })
    await waitFor(() => expect(screen.getByText(/version 9\.9\.9/)).toBeInTheDocument())

    expect(screen.getByLabelText('Time 1')).toHaveValue('05:15')
  })

  /*
   * A stored row states only the mode it uses. Seeding the draft from it
   * literally left the OTHER half empty — an off ZIP toggled back on had no
   * day and no time at all, which is a guaranteed 400. The draft is fat on
   * purpose; `payloadOf` is what canonicalises it on the way out.
   */
  it('toggling the per-user ZIP back on restores an agenda instead of an empty form', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()
    state.backupScheduleRows = storedRow('user_zip', { mode: 'times', enabled: false })

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Per-user ZIPs' }))
    await user.click(screen.getByRole('checkbox', { name: 'Generate per-user ZIPs' }))

    // The env baseline fills what the disabled row never stated.
    expect(screen.getByLabelText('Time 1')).toHaveValue('02:30')
    expect(screen.getByRole('button', { name: 'Sunday' })).toHaveAttribute('aria-pressed', 'true')

    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        {
          job: 'user_zip',
          config: { mode: 'times', times: ['02:30'], weekdays: ALL_DAYS, enabled: true },
        },
      ]),
    )
  })

  it('fills the mode a stored row does not state from the env baseline', async () => {
    const user = userEvent.setup()
    const jobs: any = { ...healthyAgent().jobs }
    jobs.dump = {
      capable: true, source: 'db', schedule: 'every 60m',
      baseline: { mode: 'times', times: ['04:20'], weekdays: ['mon', 'tue', 'wed', 'thu', 'fri'] },
    }
    jobs.mirror = {
      capable: true, source: 'db', schedule: '02:00 sun',
      baseline: { mode: 'interval', interval_min: 720 },
    }
    state.backupAgent = healthyAgent({ jobs })
    state.backupScheduleRows = {
      ...storedRow('dump', { mode: 'interval', interval_min: 60 }),
      ...storedRow('mirror', { mode: 'times', times: ['02:00'], weekdays: ['sun'] }),
    }

    renderWithProviders(<BackupSection />, { session: ownerSession })

    // The dump's row states an interval, so the times half comes from the env.
    expect(await screen.findByLabelText('Interval (minutes)')).toHaveValue(60)
    await user.click(screen.getByRole('radio', { name: 'Times' }))
    expect(screen.getByLabelText('Time 1')).toHaveValue('04:20')
    expect(screen.getByRole('button', { name: 'Saturday' })).toHaveAttribute('aria-pressed', 'false')

    // And the mirror's row states times, so the interval half comes from the env.
    await user.click(screen.getByRole('tab', { name: 'Object mirror' }))
    expect(screen.getByLabelText('Time 1')).toHaveValue('02:00')
    await user.click(screen.getByRole('radio', { name: 'Interval' }))
    expect(screen.getByLabelText('Interval (minutes)')).toHaveValue(720)
  })

  /*
   * The env is exempt from the floors by design — it IS the baseline — so
   * `BACKUP_DUMP_AT="03:30 sun"` is legal there and the agent publishes it.
   * Opening the form on a document the server would refuse teaches the owner
   * the screen is broken, so the seed widens the days it cannot keep.
   */
  it('widens an env baseline below the job floor, keeping its times', async () => {
    const user = userEvent.setup()
    const jobs: any = { ...healthyAgent().jobs }
    jobs.dump = {
      capable: true, source: 'env', schedule: '03:30 sun',
      baseline: { mode: 'times', times: ['03:30'], weekdays: ['sun'] },
    }
    state.backupAgent = healthyAgent({ jobs })

    renderWithProviders(<BackupSection />, { session: ownerSession })

    expect(await screen.findByLabelText('Time 1')).toHaveValue('03:30')
    for (const day of ['Sunday', 'Monday', 'Saturday']) {
      expect(screen.getByRole('button', { name: day })).toHaveAttribute('aria-pressed', 'true')
    }

    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        { job: 'dump', config: { mode: 'times', times: ['03:30'], weekdays: ALL_DAYS } },
      ]),
    )
  })

  /*
   * ScheduleStore.Load returns invalid rows on purpose, so the env fallback
   * stays visible — which means a row written by hand in SQL reaches this
   * form. Rendering one input per element of an unbounded array is a list the
   * server would never accept and the browser has to lay out anyway.
   */
  it('renders no more times than the server accepts, even from a hand-written row', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()
    state.backupScheduleRows = storedRow('dump', {
      mode: 'times',
      times: ['00:00', '01:00', '02:00', '03:00', '04:00', '05:00', '06:00', '07:00', '08:00'],
      weekdays: ALL_DAYS,
    })

    renderWithProviders(<BackupSection />, { session: ownerSession })

    expect(await screen.findByLabelText('Time 6')).toBeInTheDocument()
    expect(screen.queryByLabelText('Time 7')).not.toBeInTheDocument()
    expect(document.querySelectorAll('.fx-bkp-time')).toHaveLength(6)

    // And what the screen shows is what the wire carries: six inputs beside a
    // 400 saying "between 1 and 6" is a refusal the owner cannot act on.
    // Trimming thins the agenda, so it goes through the confirmation.
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await user.click(await screen.findByRole('button', { name: 'Apply schedule' }))
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        {
          job: 'dump',
          config: {
            mode: 'times',
            times: ['00:00', '01:00', '02:00', '03:00', '04:00', '05:00'],
            weekdays: ALL_DAYS,
          },
        },
      ]),
    )
  })

  /*
   * Two mutually exclusive options that swap the form's fields are a
   * radiogroup. They were dressed as tabs with no panel and no `aria-controls`
   * — a shape a screen reader announces as navigation that goes nowhere. The
   * JOB picker above stays a tablist, because those really are tabs.
   */
  it('offers the two schedule modes as a radiogroup, not as tabs', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    const group = await screen.findByRole('radiogroup', { name: 'Schedule mode' })
    const times = within(group).getByRole('radio', { name: 'Times' })
    const interval = within(group).getByRole('radio', { name: 'Interval' })
    expect(times).toHaveAttribute('aria-checked', 'true')
    expect(interval).toHaveAttribute('aria-checked', 'false')

    await user.click(interval)
    expect(interval).toHaveAttribute('aria-checked', 'true')
    expect(times).toHaveAttribute('aria-checked', 'false')

    expect(screen.getByRole('tab', { name: 'Database dump' })).toBeInTheDocument()
  })

  /*
   * A radiogroup is a SINGLE tab stop whose options move with the arrow keys —
   * that is the half of the role that is a keyboard contract, not a label. Two
   * plain buttons announced as radios but reachable only by Tab tell a screen
   * reader user to press an arrow key that does nothing.
   */
  it('moves between the schedule modes with the arrow keys, as one tab stop', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    const group = await screen.findByRole('radiogroup', { name: 'Schedule mode' })
    const times = within(group).getByRole('radio', { name: 'Times' })
    const interval = within(group).getByRole('radio', { name: 'Interval' })

    // Only the checked option is in the tab order (roving tabindex).
    expect(times).toHaveAttribute('tabindex', '0')
    expect(interval).toHaveAttribute('tabindex', '-1')

    times.focus()
    await user.keyboard('{ArrowRight}')
    expect(interval).toHaveAttribute('aria-checked', 'true')
    expect(interval).toHaveFocus()
    expect(interval).toHaveAttribute('tabindex', '0')

    // And it wraps, in both directions.
    await user.keyboard('{ArrowRight}')
    expect(times).toHaveAttribute('aria-checked', 'true')
    await user.keyboard('{ArrowLeft}')
    expect(interval).toHaveAttribute('aria-checked', 'true')
  })
})
