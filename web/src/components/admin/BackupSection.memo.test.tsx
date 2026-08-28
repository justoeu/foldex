import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act, screen, waitFor } from '@testing-library/react'
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
import { useCopy } from '../../hooks/useCopy'
import { formatMinutes } from './backupFormat'

/*
 * Two render counters, and nothing else mocked: `useCopy` is called by the job
 * cards alone and `formatMinutes` by the agenda form's interval presets alone,
 * so a call after the render under test is a memo that did not hold. Both
 * spies delegate to the real implementation — the screen behaves exactly as it
 * does in the other suites.
 */
vi.mock('../../hooks/useCopy', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../hooks/useCopy')>()
  return { ...actual, useCopy: vi.fn(actual.useCopy) }
})
vi.mock('./backupFormat', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./backupFormat')>()
  return { ...actual, formatMinutes: vi.fn(actual.formatMinutes) }
})

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
  vi.mocked(useCopy).mockClear()
  vi.mocked(formatMinutes).mockClear()
})
afterEach(() => vi.clearAllMocks())

const ownerSession: SessionState = {
  ...testAdminSession,
  user: { ...testAdminUser, role: 'owner' },
} as SessionState

const ALL_DAYS = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat']

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
 * The band re-renders on every heartbeat poll (~60 s) and on every local
 * control the operator touches. Both memo boundaries below exist so that
 * churn stops at the cards; each one had an input that changed on every
 * render, which made the `memo` decoration pure ceremony.
 */
describe('BackupSection render boundaries', () => {
  it('keeps the job cards memoized when only a local filter changes', async () => {
    const user = userEvent.setup()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession })
    await screen.findAllByRole('button', { name: 'Run now' })

    // `useMutation` answers a NEW object every render, so a "run now" callback
    // that depends on the whole mutation is never stable — and four memoized
    // cards downstream of it are never memoized at all.
    const before = vi.mocked(useCopy).mock.calls.length
    // The counter has to be counting: if `useCopy` ever leaves JobCard, both
    // sides go to zero and this test passes having asserted nothing.
    expect(before).toBeGreaterThan(0)

    const failed = screen.getByRole('button', { name: 'Failed' })
    await user.click(failed)
    // And the click has to have done something — a filter that did not move
    // re-renders nothing, which is not the same as a memo that held.
    await waitFor(() => expect(failed).toHaveAttribute('aria-pressed', 'true'))
    expect(vi.mocked(useCopy).mock.calls.length).toBe(before)
  })

  it('does not re-render the agenda form when only the heartbeat timestamp advances', async () => {
    const user = userEvent.setup()
    const client = makeQueryClient()
    state.backupAgent = healthyAgent()

    renderWithProviders(<BackupSection />, { session: ownerSession, client })
    // The mirror opens on an interval, so the presets — the counter — render.
    await user.click(await screen.findByRole('tab', { name: 'Object mirror' }))
    expect(await screen.findByLabelText('Interval (minutes)')).toBeInTheDocument()

    // A heartbeat every ~30 s means `seen_at` differs on every poll, and a
    // card that depends on the whole response re-renders the live form under
    // the owner's cursor once a minute.
    state.backupAgent = healthyAgent({ seen_at: new Date().toISOString(), version: '9.9.9' })
    const before = vi.mocked(formatMinutes).mock.calls.length
    expect(before).toBeGreaterThan(0)
    await act(async () => {
      await client.invalidateQueries({ queryKey: backupScheduleQueryKey })
    })

    // The poll really landed: the band renders what the new heartbeat says.
    await waitFor(() => expect(screen.getByText(/version 9\.9\.9/)).toBeInTheDocument())
    expect(vi.mocked(formatMinutes).mock.calls.length).toBe(before)
  })
})
