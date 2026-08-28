import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AuditSection } from './AuditSection'
import { renderWithProviders, testAdminSession } from '../../test/renderWithProviders'
import { http } from '../../api/client'
import type { SessionState } from '../../auth/types'

const ownerSession = {
  ...testAdminSession,
  user: { ...(testAdminSession as never as { user: object }).user, role: 'owner' },
} as SessionState

const EMPTY_STATS = {
  totals: {
    events: 0, events_prev: 0, failures: 0, failures_prev: 0,
    access_changes: 0, access_changes_prev: 0, actors: 0, active_users: 4,
  },
  days: [{ day: new Date().toISOString(), logins: 0, failed: 0, admin: 0, content: 0 }],
  distribution: [],
  actors: [],
  origins: [],
  risk: null,
}

const BUSY_STATS = {
  ...EMPTY_STATS,
  totals: { ...EMPTY_STATS.totals, events: 12, actors: 2 },
  distribution: [{ action: 'login.failed', category: 'identity' as const, count: 12 }],
  actors: [{ email: 'lucas.marques@foldex.test', role: 'owner' as const, count: 12 }],
  origins: [
    {
      ip: '189.42.11.7', trusted: false, user_agent: null,
      count: 12, failures: 12, last_seen: new Date().toISOString(), blocked: false,
    },
    {
      ip: '177.20.4.19', trusted: true, user_agent: 'Chrome/141',
      count: 4, failures: 0, last_seen: new Date().toISOString(), blocked: true,
    },
  ],
}

const BLOCKS = {
  blocks: [
    {
      id: 1, ip: '189.42.11.7', reason: 'brute force',
      created_by: 'owner@foldex.test', created_at: new Date().toISOString(),
    },
  ],
  max: 1000,
}

function mockApi(over: Record<string, unknown> = {}) {
  return vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
    const table: Record<string, unknown> = {
      '/api/admin/audit': { entries: [] },
      '/api/admin/audit/stats': EMPTY_STATS,
      '/api/admin/audit/blocks': { blocks: [], max: 1000 },
      ...over,
    }
    return { data: table[url.split('?')[0]] ?? {} } as never
  })
}

beforeEach(() => vi.restoreAllMocks())
afterEach(() => vi.restoreAllMocks())

describe('AuditSection — header', () => {
  it('reports an unavailable header without hiding the trail', async () => {
    vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
      if (url.startsWith('/api/admin/audit/stats')) throw new Error('boom')
      return { data: { entries: [], blocks: [] } } as never
    })
    renderWithProviders(<AuditSection />)

    expect(await screen.findByText(/could not be loaded|unavailable/i)).toBeInTheDocument()
    // The list is a separate query: an aggregate failing must not take the
    // trail down with it, because the trail is the thing being consulted.
    expect(screen.getByRole('heading', { name: /timeline/i })).toBeInTheDocument()
  })

  it('describes an empty period rather than rendering nothing', async () => {
    mockApi()
    renderWithProviders(<AuditSection />)

    // The distribution card says the same words, so the hint is what
    // distinguishes the TIMELINE's empty state — and the hint is the half that
    // tells the operator what to do about it.
    const hint = await screen.findByText(/adjust the event type or the period/i)
    expect(hint).toBeInTheDocument()
    expect(hint.closest('.fx-empty')).toHaveTextContent(/nothing recorded yet/i)
  })

  it('says there is no burst when nothing looks like an attack', async () => {
    mockApi()
    renderWithProviders(<AuditSection />)
    expect(await screen.findByText(/no burst in the period/i)).toBeInTheDocument()
  })
})

describe('AuditSection — search', () => {
  // The predicate behind the box is a LIKE over the window and deliberately
  // not indexed. One request per keystroke would be one unindexed scan per
  // keystroke.
  it('debounces the typed filter into a single request', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const get = mockApi()
    renderWithProviders(<AuditSection />)
    await screen.findByRole('heading', { name: /timeline/i })
    get.mockClear()

    await userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
      .type(screen.getByRole('searchbox'), '189.42')

    expect(get).not.toHaveBeenCalledWith('/api/admin/audit', { params: { window: '7d', q: '189.42' } })
    await vi.advanceTimersByTimeAsync(400)
    await waitFor(() =>
      expect(get).toHaveBeenCalledWith('/api/admin/audit', { params: { window: '7d', q: '189.42' } }),
    )
    vi.useRealTimers()
  })
})

describe('AuditSection — CSV export', () => {
  it('saves the file under the filter currently on screen', async () => {
    const get = mockApi()
    get.mockImplementation(async (url: string) => {
      if (url.startsWith('/api/admin/audit/export.csv')) {
        return { data: new Blob(['id,action\n'], { type: 'text/csv' }) } as never
      }
      const table: Record<string, unknown> = {
        '/api/admin/audit': { entries: [] },
        '/api/admin/audit/stats': EMPTY_STATS,
        '/api/admin/audit/blocks': { blocks: [], max: 1000 },
      }
      return { data: table[url.split('?')[0]] ?? {} } as never
    })
    const createURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:mock')
    // Revoked, not merely created: leaking the URL pins the whole file in
    // memory for the life of the document, and this file may be large.
    const revokeURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    renderWithProviders(<AuditSection />)
    await userEvent.setup().click(await screen.findByRole('button', { name: /export csv/i }))

    await waitFor(() =>
      expect(get).toHaveBeenCalledWith('/api/admin/audit/export.csv', {
        params: { window: '7d' },
        responseType: 'blob',
      }),
    )
    expect(click).toHaveBeenCalled()
    expect(createURL).toHaveBeenCalled()
    await waitFor(() => expect(revokeURL).toHaveBeenCalledWith('blob:mock'))
  })

  it('reports a failed export instead of silently doing nothing', async () => {
    const get = mockApi()
    get.mockImplementation(async (url: string) => {
      if (url.startsWith('/api/admin/audit/export.csv')) throw new Error('boom')
      const table: Record<string, unknown> = {
        '/api/admin/audit': { entries: [] },
        '/api/admin/audit/stats': EMPTY_STATS,
        '/api/admin/audit/blocks': { blocks: [], max: 1000 },
      }
      return { data: table[url.split('?')[0]] ?? {} } as never
    })
    renderWithProviders(<AuditSection />)

    await userEvent.setup().click(await screen.findByRole('button', { name: /export csv/i }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/csv could not be generated/i)
  })
})

describe('AuditSection — origins and blocklist', () => {
  it('shows whether a configured proxy vouched for each address', async () => {
    mockApi({ '/api/admin/audit/stats': BUSY_STATS })
    renderWithProviders(<AuditSection />)

    expect(await screen.findByText(/direct address, no proxy/i)).toBeInTheDocument()
    expect(screen.getByText(/via trusted proxy/i)).toBeInTheDocument()
  })

  // An address already blocked must not be offered again — the button would
  // ask for an action that has been taken.
  it('marks an already-blocked origin instead of offering to block it', async () => {
    mockApi({ '/api/admin/audit/stats': BUSY_STATS })
    renderWithProviders(<AuditSection />, { session: ownerSession })
    await screen.findByText('177.20.4.19')

    expect(screen.getByText(/^blocked$/i)).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /^block$/i })).toHaveLength(1)
  })

  it('offers no block control at all to an administrator', async () => {
    mockApi({ '/api/admin/audit/stats': BUSY_STATS })
    renderWithProviders(<AuditSection />)
    await screen.findByText('189.42.11.7')

    expect(screen.queryByRole('button', { name: /^block$/i })).not.toBeInTheDocument()
  })

  it('lists the blocklist with the way back out', async () => {
    mockApi({ '/api/admin/audit/blocks': BLOCKS })
    const del = vi.spyOn(http, 'delete').mockResolvedValue({ data: null } as never)
    renderWithProviders(<AuditSection />, { session: ownerSession })

    expect(await screen.findByText(/blocked addresses/i)).toBeInTheDocument()
    expect(screen.getByText(/brute force/)).toBeInTheDocument()

    await userEvent.setup().click(screen.getByRole('button', { name: /unblock/i }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.setup().click(within(dialog).getByRole('button', { name: /^unblock$/i }))

    await waitFor(() =>
      expect(del).toHaveBeenCalledWith('/api/admin/audit/blocks/189.42.11.7'),
    )
  })

  // An admin can READ the blocklist — without it they cannot interpret the
  // silence from an address — but is offered no control.
  it('shows an administrator the blocklist without the unblock control', async () => {
    mockApi({ '/api/admin/audit/blocks': BLOCKS })
    renderWithProviders(<AuditSection />)

    expect(await screen.findByText('189.42.11.7')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /unblock/i })).not.toBeInTheDocument()
  })

  it('hides the blocklist card entirely when nothing is blocked', async () => {
    mockApi()
    renderWithProviders(<AuditSection />, { session: ownerSession })
    await screen.findByRole('heading', { name: /timeline/i })

    expect(screen.queryByText(/blocked addresses/i)).not.toBeInTheDocument()
  })

  it('leaves the dialog without acting when the operator backs out', async () => {
    mockApi({ '/api/admin/audit/blocks': BLOCKS })
    const del = vi.spyOn(http, 'delete').mockResolvedValue({ data: null } as never)
    renderWithProviders(<AuditSection />, { session: ownerSession })

    await userEvent.setup().click(await screen.findByRole('button', { name: /unblock/i }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.setup().click(within(dialog).getByRole('button', { name: /cancel/i }))

    expect(del).not.toHaveBeenCalled()
  })
})

describe('AuditSection — actors and distribution', () => {
  it('names the busiest accounts with their current role', async () => {
    mockApi({ '/api/admin/audit/stats': BUSY_STATS })
    renderWithProviders(<AuditSection />)

    expect(await screen.findByText('lucas.marques@foldex.test')).toBeInTheDocument()
    expect(screen.getByText(/^owner$/i)).toBeInTheDocument()
    // One initial, not two: the aggregate returns no display name, and the
    // avatar goes through the SAME lib/initials helper every other avatar in
    // the app uses. A second scheme here would give one person two different
    // monograms on two screens.
    expect(screen.getByText('L')).toBeInTheDocument()
  })

  it('says so when there is nothing to distribute', async () => {
    mockApi()
    renderWithProviders(<AuditSection />)
    await screen.findByRole('heading', { name: /distribution by type/i })

    // Both the distribution card and the timeline report emptiness; the point
    // is that neither renders a bar of width NaN.
    expect(screen.getAllByText(/nothing recorded yet/i).length).toBeGreaterThan(0)
  })
})
