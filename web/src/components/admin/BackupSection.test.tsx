import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BackupSection } from './BackupSection'
import { renderWithProviders } from '../../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../../test/server'

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

    expect(await screen.findByText('Database dump')).toBeInTheDocument()
    expect(screen.getByText('Restore drill')).toBeInTheDocument()
    expect(screen.getByText('Object mirror')).toBeInTheDocument()
    expect(screen.getByText('Per-user ZIPs')).toBeInTheDocument()

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
