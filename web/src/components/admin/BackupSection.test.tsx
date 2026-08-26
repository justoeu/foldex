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

    // Each job name renders twice: the status table row and the schedule row.
    expect(await screen.findAllByText('Database dump')).not.toHaveLength(0)
    expect(screen.getAllByText('Restore drill')).not.toHaveLength(0)
    expect(screen.getAllByText('Object mirror')).not.toHaveLength(0)
    expect(screen.getAllByText('Per-user ZIPs')).not.toHaveLength(0)

    // Never ran + no history = the service is off, and the band says so
    // instead of looking healthy (the mailer incident's lesson).
    expect(screen.getByText('The backup service is not active')).toBeInTheDocument()
    expect(screen.getByText('COMPOSE_PROFILES=backup')).toBeInTheDocument()
    expect(screen.getAllByText('never ran')).toHaveLength(4)
    expect(screen.getByText('No runs recorded.')).toBeInTheDocument()
  })

  it('marks a dump older than 26h as stale, and a fresh one not', async () => {
    const fresh = run({ id: 10, started_at: new Date(Date.now() - 2 * HOUR).toISOString() })
    state.backupJobs = jobsSummary(fresh)
    state.backupStatusRuns = [fresh]

    const { unmount } = renderWithProviders(<BackupSection />)
    const freshCell = (await screen.findByText(/2h/)).closest('span')
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
    expect(screen.getByText(/Validated dump run #21/)).toBeInTheDocument()
    expect(screen.getByText('link: 42')).toBeInTheDocument()
    expect(screen.getByText('note: 7')).toBeInTheDocument()

    // The artifact row renders truncated but titled with the full key, and the
    // SHA is copiable behind a visible label (INV-151).
    expect(screen.getAllByTitle('foldex/dump/2026-08-26.dump.age').length).toBeGreaterThan(0)
    expect(screen.getByRole('button', { name: 'Copy' })).toBeInTheDocument()
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

    expect(await screen.findByText(/Agent seen/)).toBeInTheDocument()
    expect(screen.getByText(/version 2\.10\.1/)).toBeInTheDocument()
    // The agenda strings the agent reported, verbatim.
    expect(screen.getByText('03:30')).toBeInTheDocument()
    expect(screen.getByText('04:00 wed')).toBeInTheDocument()
    expect(screen.getByText('every 360m')).toBeInTheDocument()
    // Origin badges: the drill row came from the database, the rest from env.
    expect(screen.getAllByText('env default')).toHaveLength(3)
    expect(screen.getByText('configured here')).toBeInTheDocument()
  })

  it('shows the translated reason and no editors for a job the agent cannot run', async () => {
    state.backupAgent = healthyAgent({
      jobs: {
        ...healthyAgent().jobs,
        mirror: { capable: false, reason: 'mirror_off', source: 'env', schedule: 'every 360m' },
      },
    })

    renderWithProviders(<BackupSection />, { session: ownerSession })

    expect(await screen.findByText('the mirror source is not configured')).toBeInTheDocument()
    // Three editable jobs get a save button; the incapable mirror gets none.
    expect(screen.getAllByRole('button', { name: 'Save schedule' })).toHaveLength(3)
    expect(screen.queryByLabelText('Interval (minutes)')).not.toBeInTheDocument()
  })

  it('lets the owner pre-configure the agenda before any agent ever reported', async () => {
    // No heartbeat at all is NOT the same as "cannot run": the owner sets
    // the agenda first and enables the backup profile after. A stricter
    // gate here would break that ordering.
    state.backupAgent = undefined

    renderWithProviders(<BackupSection />, { session: ownerSession })

    expect(await screen.findAllByRole('button', { name: 'Save schedule' })).toHaveLength(4)
  })

  it('treats a job missing from a live report as not runnable — no editors', async () => {
    // An older agent build that never heard of a job reports nothing for it.
    // With a live heartbeat, silence means "will not run", not "go ahead":
    // editors here would store a row no process reads (the mailer lesson).
    const jobs: any = { ...healthyAgent().jobs }
    delete jobs.mirror
    state.backupAgent = healthyAgent({ jobs })

    renderWithProviders(<BackupSection />, { session: ownerSession })

    expect(await screen.findAllByRole('button', { name: 'Save schedule' })).toHaveLength(3)
    expect(screen.queryByLabelText('Interval (minutes)')).not.toBeInTheDocument()
  })

  it('lets the owner add a dump time and PUTs the full times list', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })

    await user.click(await screen.findByRole('button', { name: 'Add time' }))
    await user.click(screen.getAllByRole('button', { name: 'Save schedule' })[0])

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
    const removes = await screen.findAllByRole('button', { name: 'Remove' })
    await user.click(removes[1])
    await user.click(screen.getAllByRole('button', { name: 'Save schedule' })[0])

    expect(await screen.findByText('Reduce “Database dump” protection?')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(state.backupSchedulePuts ?? []).toHaveLength(0)

    await user.click(screen.getAllByRole('button', { name: 'Save schedule' })[0])
    await user.click(await screen.findByRole('button', { name: 'Apply schedule' }))
    await waitFor(() =>
      expect(state.backupSchedulePuts).toEqual([{ job: 'dump', config: { times: ['03:30'] } }]),
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
    await user.click(screen.getAllByRole('button', { name: 'Save schedule' })[0])

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
    expect(screen.getAllByText('no agent report for this job')).toHaveLength(4)
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

    expect(await screen.findByText('03:30')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Save schedule' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Restore env default' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Add time' })).not.toBeInTheDocument()
  })
})
