import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BackupSection } from './BackupSection'
import { renderWithProviders, testAdminSession, testAdminUser } from '../../test/renderWithProviders'
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

/** A healthy heartbeat, fresh, every job capable, all on the env baseline. */
function healthyAgent(over: Record<string, unknown> = {}) {
  return {
    seen_at: new Date(Date.now() - 30_000).toISOString(),
    version: '2.10.1',
    jobs: {
      dump: { capable: true, source: 'env', schedule: '03:30' },
      drill: { capable: true, source: 'env', schedule: '01:00 sun' },
      mirror: { capable: true, source: 'env', schedule: 'every 360m' },
      user_zip: { capable: true, source: 'env', schedule: '02:30' },
    },
    ...over,
  }
}

describe('BackupSection schedule', () => {
  it('renders the effective agenda from the agent report with a source badge per job', async () => {
    state.backupAgent = healthyAgent({
      jobs: {
        ...healthyAgent().jobs,
        drill: { capable: true, source: 'db', schedule: '04:00 wed' },
      },
    })
    state.backupScheduleRows = {
      drill: {
        job: 'drill',
        config: { time: '04:00', weekday: 'wed' },
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
        mirror: { capable: false, reason: 'mirror_off', source: 'env', schedule: 'every 360m' },
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

  it('lets the owner add a dump time and PUTs the full times list', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('button', { name: 'Add time' }))
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))

    // More dumps than today — no confirmation stands between click and PUT.
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        { job: 'dump', config: { times: ['03:30', '12:00'] } },
      ]),
    )
    expect(screen.queryByText(/Reduce “Database dump” protection\?/)).not.toBeInTheDocument()
  })

  it('asks for confirmation when the change reduces protection, and only writes after it', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()
    state.backupScheduleRows = {
      dump: {
        job: 'dump',
        config: { times: ['03:30', '15:30'] },
        updated_at: '2026-08-01T00:00:00Z',
        updated_by_email: 'owner@foldex.test',
      },
    }

    renderWithProviders(<BackupSection />, { session: ownerSession })

    // Two stored times seed the editor; dropping one is a reduction.
    await user.click(await screen.findByRole('button', { name: 'Remove time 2' }))
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))

    expect(await screen.findByText('Reduce “Database dump” protection?')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(state.backupSchedulePuts ?? []).toHaveLength(0)

    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await user.click(await screen.findByRole('button', { name: 'Apply schedule' }))
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([{ job: 'dump', config: { times: ['03:30'] } }]),
    )
  })

  /*
   * One test per remaining job, because each writes a DIFFERENT config shape
   * and the editors that produce them share no code: the weekday pills, the
   * interval field and the on/off switch each have their own draft state.
   */
  it('writes the drill weekday and time the pills and field hold', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Restore drill' }))
    await user.click(await screen.findByRole('button', { name: 'Wednesday' }))
    fireEvent.change(screen.getByLabelText('Time'), { target: { value: '05:15' } })
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))

    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        { job: 'drill', config: { time: '05:15', weekday: 'wed' } },
      ]),
    )
  })

  it('writes the mirror interval as a NUMBER when a preset is picked', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Object mirror' }))
    // 30m is more frequent than the 360m baseline, so no confirmation stands
    // between the click and the PUT.
    await user.click(screen.getByRole('button', { name: '30m' }))
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))

    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([{ job: 'mirror', config: { interval_min: 30 } }]),
    )
  })

  it('writes the typed mirror interval as a number, not the input string', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Object mirror' }))
    fireEvent.change(screen.getByLabelText('Interval (minutes)'), { target: { value: '45' } })
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))

    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([{ job: 'mirror', config: { interval_min: 45 } }]),
    )
    // A string would pass the server's JSON decode and fail its bounds check —
    // the coercion is the contract.
    expect(typeof state.backupSchedulePuts![0].config.interval_min).toBe('number')
  })

  it('turning the per-user ZIP off writes enabled:false and drops the time', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Per-user ZIPs' }))
    // On by default, and the time field goes with it.
    expect(screen.getByLabelText('Time')).toBeInTheDocument()
    await user.click(screen.getByRole('checkbox', { name: 'Generate per-user ZIPs' }))
    expect(screen.queryByLabelText('Time')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    // Switching a job OFF reduces protection, so it is behind the confirmation.
    await user.click(await screen.findByRole('button', { name: 'Apply schedule' }))
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([{ job: 'user_zip', config: { enabled: false } }]),
    )
  })

  /*
   * The switch remembers the time the owner typed. The config that reaches the
   * server must be a bare `{enabled: false}` — a time on a disabled job is an
   * agenda nothing reads — but toggling back on should return their own time,
   * not the default, or the switch quietly discards their edit.
   */
  it('keeps the typed ZIP time across an off/on toggle', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('tab', { name: 'Per-user ZIPs' }))
    fireEvent.change(screen.getByLabelText('Time'), { target: { value: '04:45' } })

    const toggle = screen.getByRole('checkbox', { name: 'Generate per-user ZIPs' })
    await user.click(toggle)
    await user.click(toggle)

    expect(screen.getByLabelText('Time')).toHaveValue('04:45')
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([
        { job: 'user_zip', config: { enabled: true, time: '04:45' } },
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

  it('resets to the env baseline via DELETE, always behind a confirmation', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()
    state.backupScheduleRows = {
      mirror: {
        job: 'mirror',
        config: { interval_min: 60 },
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
  })
})
