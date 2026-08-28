import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AuditAnomalies } from './AuditAnomalies'
import { freshState, installAxiosMock, type AnomalyMock, type MockState } from '../../test/server'
import { renderWithProviders, testAdminSession } from '../../test/renderWithProviders'
import { http } from '../../api/client'
import type { SessionState } from '../../auth/types'

const ownerSession = {
  ...testAdminSession,
  user: { ...(testAdminSession as never as { user: object }).user, role: 'owner' },
} as SessionState

const NOW = Date.now()

function anomaly(over: Partial<AnomalyMock> = {}): AnomalyMock {
  return {
    kind: 'spray',
    ip: '189.42.11.7',
    ip_trusted: false,
    distinct_accounts: 14,
    failures: 22,
    throttles: 0,
    first_seen: new Date(NOW - 9 * 60_000).toISOString(),
    last_seen: new Date(NOW).toISOString(),
    blocked: false,
    severity: 'critical',
    ...over,
  }
}

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

describe('AuditAnomalies — an empty window', () => {
  // The healthy state, and the copy must not read as a failure: an operator who
  // learns to see "nothing found" as a broken panel stops opening it.
  it('describes a quiet window rather than an error', async () => {
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />)

    expect(await screen.findByText(/nothing anomalous in this window/i)).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})

describe('AuditAnomalies — what each row says', () => {
  it('names the signal and shows the evidence as numbers', async () => {
    state.anomalies = [anomaly()]
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />)

    const row = await screen.findByRole('listitem')
    expect(within(row).getByText(/account sweep/i)).toBeInTheDocument()
    expect(within(row).getByText(/14 distinct accounts · 22 failures · 9 min/i)).toBeInTheDocument()
  })

  it('translates each kind rather than printing the token', async () => {
    state.anomalies = [
      anomaly({ kind: 'hammer', ip: '10.1.1.1', severity: 'warning' }),
      anomaly({ kind: 'throttle', ip: '10.1.1.2', severity: 'warning', throttles: 9 }),
    ]
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />)

    expect(await screen.findByText(/hammering one account/i)).toBeInTheDocument()
    expect(screen.getByText(/origin already throttled/i)).toBeInTheDocument()
    expect(screen.queryByText(/^hammer$/)).not.toBeInTheDocument()
  })

  // THE defect that produced the SDD: behind a proxy, an address nothing
  // vouched for is the proxy's own, and the row is about everyone behind it.
  it('warns that an unvouched address may be the proxy itself', async () => {
    state.anomalies = [anomaly({ ip_trusted: false })]
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />)

    const row = await screen.findByRole('listitem')
    expect(within(row).getByText(/may be your proxy/i)).toBeInTheDocument()
  })

  it('says nothing of the sort when a proxy vouched for the address', async () => {
    state.anomalies = [anomaly({ ip_trusted: true })]
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />)

    const row = await screen.findByRole('listitem')
    expect(within(row).queryByText(/may be your proxy/i)).not.toBeInTheDocument()
    expect(within(row).getByText(/via trusted proxy/i)).toBeInTheDocument()
  })

  it('orders the critical signals above the merely notable ones', async () => {
    state.anomalies = [
      anomaly({ ip: '10.0.0.9', severity: 'warning', kind: 'throttle' }),
      anomaly({ ip: '10.0.0.1', severity: 'critical' }),
    ]
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />)

    await screen.findByText('10.0.0.1')
    const ips = screen.getAllByRole('listitem').map((li) => li.textContent)
    expect(ips[0]).toContain('10.0.0.1')
    expect(ips[1]).toContain('10.0.0.9')
  })
})

describe('AuditAnomalies — the two actions', () => {
  it('hands the address to the trail instead of navigating away', async () => {
    state.anomalies = [anomaly()]
    const onInspect = vi.fn()
    renderWithProviders(<AuditAnomalies canBlock onInspect={onInspect} />)

    await userEvent.setup().click(await screen.findByRole('button', { name: /see events/i }))
    expect(onInspect).toHaveBeenCalledWith('189.42.11.7')
  })

  // Never automatic: the block is permanent and, behind a proxy, collective.
  it('blocks only after the confirmation is accepted', async () => {
    state.anomalies = [anomaly()]
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />, { session: ownerSession })

    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /^block$/i }))
    expect(post).not.toHaveBeenCalled()

    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /^block$/i }))

    await waitFor(() => expect(post).toHaveBeenCalledWith(
      '/api/admin/audit/blocks',
      expect.objectContaining({ ip: '189.42.11.7' }),
    ))
    // The reason is DERIVED from the signal, not typed: the operator is looking
    // at the evidence when they click, and a free-text field yields either that
    // same sentence or nothing.
    expect(post.mock.calls[0][1]).toMatchObject({
      reason: expect.stringMatching(/14 distinct accounts/i),
    })
  })

  // A row that still offers "block" on an address just blocked asks for an
  // action already taken — and the second click answers 409 for no reason the
  // operator can see.
  it('re-reads the panel once the block lands', async () => {
    state.anomalies = [anomaly()]
    vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />, { session: ownerSession })

    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /^block$/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /^block$/i }))

    await waitFor(() => {
      const asked = state.anomalyWindows ?? []
      expect(asked.filter((w) => w === '24h').length).toBeGreaterThan(1)
    })
  })

  it('leaves the address alone when the operator backs out', async () => {
    state.anomalies = [anomaly()]
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />, { session: ownerSession })

    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /^block$/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /cancel/i }))

    expect(post).not.toHaveBeenCalled()
  })

  // A block that fails silently is worse than one that is refused loudly: the
  // operator walks away believing the address is parked.
  it('surfaces the server refusal when the block does not land', async () => {
    state.anomalies = [anomaly()]
    vi.spyOn(http, 'post').mockRejectedValue({
      response: {
        status: 400,
        data: { error: { code: 'invalid_ip', message: 'that is the address you are connected from' } },
      },
    })
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />, { session: ownerSession })

    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /^block$/i }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /^block$/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'that is the address you are connected from',
    )
  })

  it('marks an already-blocked address instead of offering the button again', async () => {
    state.anomalies = [anomaly({ blocked: true })]
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />)

    expect(await screen.findByText(/already blocked/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^block$/i })).not.toBeInTheDocument()
  })

  it('offers no block control to a reader who may not write the blocklist', async () => {
    state.anomalies = [anomaly()]
    renderWithProviders(<AuditAnomalies canBlock={false} onInspect={() => {}} />)

    await screen.findByText('189.42.11.7')
    expect(screen.queryByRole('button', { name: /^block$/i })).not.toBeInTheDocument()
    // The inspect action is a READ and stays available to an administrator.
    expect(screen.getByRole('button', { name: /see events/i })).toBeInTheDocument()
  })
})

describe('AuditSection — the panel inside the trail', () => {
  const EMPTY_STATS = {
    totals: {
      events: 0, events_prev: 0, failures: 0, failures_prev: 0,
      access_changes: 0, access_changes_prev: 0, actors: 0, active_users: 1,
    },
    days: [{ day: new Date().toISOString(), logins: 0, failed: 0, admin: 0, content: 0 }],
    distribution: [], actors: [], origins: [], risk: null,
  }

  function mockTrail() {
    return vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
      const table: Record<string, unknown> = {
        '/api/admin/audit': { entries: [] },
        '/api/admin/audit/stats': EMPTY_STATS,
        '/api/admin/audit/blocks': { blocks: [] },
        '/api/admin/anomalies': {
          window: '24h',
          thresholds: { spray_accounts: 10, hammer_failures: 20, window_minutes: 15 },
          anomalies: [anomaly()],
        },
      }
      return { data: table[url.split('?')[0]] ?? {} } as never
    })
  }

  // The trail's search already matches host(ip), so "see events" is the query
  // the operator would have typed — not a second screen and not a second
  // endpoint.
  it('filters the trail by the address the signal names', async () => {
    const { AuditSection } = await import('./AuditSection')
    const get = mockTrail()
    renderWithProviders(<AuditSection />, { session: ownerSession })

    await userEvent.setup().click(await screen.findByRole('button', { name: /see events/i }))

    expect(screen.getByRole('searchbox')).toHaveValue('189.42.11.7')
    await waitFor(() => expect(get).toHaveBeenCalledWith(
      '/api/admin/audit',
      { params: { window: '7d', q: '189.42.11.7' } },
    ))
  })

  // A leftover chip would show a filtered subset of the address's events and
  // read as "there is nothing here".
  it('clears the other filters so the address is the whole question', async () => {
    const get = mockTrail()
    get.mockImplementation(async (url: string) => {
      const table: Record<string, unknown> = {
        '/api/admin/audit': { entries: [] },
        '/api/admin/audit/stats': {
          ...EMPTY_STATS,
          distribution: [{ action: 'login.failed', category: 'identity' as const, count: 9 }],
        },
        '/api/admin/audit/blocks': { blocks: [] },
        '/api/admin/anomalies': {
          window: '24h',
          thresholds: { spray_accounts: 10, hammer_failures: 20, window_minutes: 15 },
          anomalies: [anomaly()],
        },
      }
      return { data: table[url.split('?')[0]] ?? {} } as never
    })
    const { AuditSection } = await import('./AuditSection')
    renderWithProviders(<AuditSection />, { session: ownerSession })

    const user = userEvent.setup()
    const chip = await screen.findByRole('button', { name: /failed sign-in|login\.failed/i })
    await user.click(chip)
    await waitFor(() => expect(chip).toHaveAttribute('aria-pressed', 'true'))

    await user.click(screen.getByRole('button', { name: /see events/i }))

    expect(chip).toHaveAttribute('aria-pressed', 'false')
    await waitFor(() => expect(get).toHaveBeenCalledWith(
      '/api/admin/audit',
      { params: { window: '7d', q: '189.42.11.7' } },
    ))
  })
})

describe('AuditAnomalies — the window', () => {
  it('re-asks the server when the lookback changes', async () => {
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />)
    await screen.findByText(/nothing anomalous/i)
    expect(state.anomalyWindows).toContain('24h')

    await userEvent.setup().click(screen.getByRole('button', { name: '15m' }))
    await waitFor(() => expect(state.anomalyWindows).toContain('15m'))
  })

  it('states the thresholds it is applying', async () => {
    state.abusePolicy = { anomaly_spray_accounts: 4, anomaly_hammer_failures: 30 }
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />)

    expect(await screen.findByText(/from 4 distinct accounts/i)).toBeInTheDocument()
    expect(screen.getByText(/from 30 failures/i)).toBeInTheDocument()
  })

  it('reports an unreachable panel rather than an empty window', async () => {
    vi.spyOn(http, 'get').mockRejectedValue(new Error('boom'))
    renderWithProviders(<AuditAnomalies canBlock onInspect={() => {}} />)

    expect(await screen.findByText(/anomalies could not be loaded/i)).toBeInTheDocument()
  })
})
