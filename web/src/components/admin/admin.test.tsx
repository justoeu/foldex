import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AdminOverview, relativeTime } from './AdminOverview'
import { RolesMatrix } from './RolesMatrix'
import { AuditSection } from './AuditSection'
import { PolicySection } from './PolicySection'
import { renderWithProviders, testAdminSession, testAdminUser } from '../../test/renderWithProviders'
import { http } from '../../api/client'
import type { SessionState } from '../../auth/types'

afterEach(() => vi.restoreAllMocks())

const METRICS = {
  active_users: 18,
  active_users_added_30d: 3,
  pending_invites: 2,
  next_invite_expiry_hours: 23,
  roles_in_use: 4,
  permission_count: 14,
  two_factor_percent: 61,
}

const ROLES = {
  roles: [
    { role: 'owner', permissions: ['content.read', 'policy.write'], user_count: 1 },
    { role: 'admin', permissions: ['content.read', 'users.write'], user_count: 2 },
    { role: 'editor', permissions: ['content.read', 'content.write'], user_count: 11 },
    { role: 'viewer', permissions: ['content.read'], user_count: 4 },
  ],
  permissions: ['content.read', 'content.write', 'users.write', 'policy.write'],
}

const POLICY = {
  admin_second_factor: 'any',
  password_min_length: 8,
  otp_ttl_minutes: 5,
  otp_cooldown_seconds: 60,
  google_allowed_domains: ['example.com'],
  google_auto_provision: false,
  google_default_role: 'editor',
}

const AUDIT = {
  entries: [
    {
      id: 9, action: 'login.failed', actor_email: null,
      target_email: 'someone@foldex.test', detail: null,
      created_at: new Date(Date.now() - 2 * 3600_000).toISOString(),
    },
    {
      id: 8, action: 'user.role_changed', actor_email: 'admin@foldex.test',
      target_email: 'rafa@foldex.test', detail: 'admin',
      created_at: new Date(Date.now() - 26 * 3600_000).toISOString(),
    },
  ],
}

/** Answers every administration endpoint, so a component under test hits only its own. */
function mockAdminApi(over: Record<string, unknown> = {}) {
  return vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
    const path = url.split('?')[0]
    const table: Record<string, unknown> = {
      '/api/admin/metrics': METRICS,
      '/api/admin/roles': ROLES,
      '/api/admin/audit': AUDIT,
      '/api/admin/policy': POLICY,
      ...over,
    }
    return { data: table[path] ?? {} } as never
  })
}

/** A session whose role is the owner — the only one that may write policy. */
const ownerSession: SessionState = {
  ...testAdminSession,
  user: { ...testAdminUser, role: 'owner' },
} as SessionState

describe('AdminOverview', () => {
  it('renders the derived metrics rather than raw counters', async () => {
    mockAdminApi()
    renderWithProviders(<AdminOverview onOpen={vi.fn()} />)

    expect(await screen.findByText('18')).toBeInTheDocument()
    expect(screen.getByText('61%')).toBeInTheDocument()
    // The hint carries the delta, which is what makes the number readable.
    expect(screen.getByText(/\+3 in the last 30 days/i)).toBeInTheDocument()
  })

  it('routes each card to the section that owns the mutation', async () => {
    mockAdminApi()
    const onOpen = vi.fn()
    renderWithProviders(<AdminOverview onOpen={onOpen} />)

    await userEvent.setup().click(await screen.findByRole('button', { name: /manage accounts/i }))
    expect(onOpen).toHaveBeenCalledWith('users')
  })

  it('flags pending invitations on the card, and stays quiet when there are none', async () => {
    mockAdminApi()
    const { unmount } = renderWithProviders(<AdminOverview onOpen={vi.fn()} />)
    expect(await screen.findByText(/2 pending/i)).toBeInTheDocument()
    unmount()

    vi.restoreAllMocks()
    mockAdminApi({ '/api/admin/metrics': { ...METRICS, pending_invites: 0 } })
    renderWithProviders(<AdminOverview onOpen={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('0')).toBeInTheDocument())
    expect(screen.queryByText(/pending$/i)).not.toBeInTheDocument()
  })

  it('shows the recent trail, and an empty state when nothing happened', async () => {
    mockAdminApi()
    const { unmount } = renderWithProviders(<AdminOverview onOpen={vi.fn()} />)
    expect(await screen.findByText(/failed sign-in/i)).toBeInTheDocument()
    unmount()

    vi.restoreAllMocks()
    mockAdminApi({ '/api/admin/audit': { entries: [] } })
    renderWithProviders(<AdminOverview onOpen={vi.fn()} />)
    expect(await screen.findByText(/nothing needs attention/i)).toBeInTheDocument()
  })

  // A malformed payload arrives as a truthy object; indexing into it would take
  // the whole settings hub down over one bad response.
  it('survives a response of the wrong shape', async () => {
    mockAdminApi({ '/api/admin/metrics': { nonsense: true }, '/api/admin/roles': { nope: 1 } })
    renderWithProviders(<AdminOverview onOpen={vi.fn()} />)

    expect(await screen.findByText(/role matrix could not be loaded/i)).toBeInTheDocument()
  })
})

describe('relativeTime', () => {
  it('reads in the coarsest unit that still says something', () => {
    const now = Date.now()
    expect(relativeTime(new Date(now - 5 * 60_000).toISOString())).toBe('5m')
    expect(relativeTime(new Date(now - 3 * 3600_000).toISOString())).toBe('3h')
    expect(relativeTime(new Date(now - 50 * 3600_000).toISOString())).toBe('2d')
  })

  // Clock skew between the browser and the server can put a timestamp in the
  // future; "-3m ago" is worse than "0m".
  it('never goes negative', () => {
    expect(relativeTime(new Date(Date.now() + 60_000).toISOString())).toBe('0m')
  })
})

describe('RolesMatrix', () => {
  it('renders every role from the server, including one nobody holds', async () => {
    mockAdminApi({
      '/api/admin/roles': {
        ...ROLES,
        roles: [...ROLES.roles.slice(0, 3), { role: 'viewer', permissions: [], user_count: 0 }],
      },
    })
    renderWithProviders(<RolesMatrix />)

    expect(await screen.findByText('Owner')).toBeInTheDocument()
    expect(screen.getByText('Viewer')).toBeInTheDocument()
    expect(screen.getByText('0 accounts')).toBeInTheDocument()
  })

  // The permission ids are the server's own identifiers: an administrator
  // comparing a role against the API needs to see the same string in both.
  it('shows raw permission identifiers, not prose', async () => {
    mockAdminApi()
    renderWithProviders(<RolesMatrix />)
    expect(await screen.findByText('policy.write')).toBeInTheDocument()
  })
})

describe('AuditSection', () => {
  it('lists entries with who did what to whom', async () => {
    mockAdminApi()
    renderWithProviders(<AuditSection />)

    // Awaited on the ROW's content, not on the action label: the filter bar
    // renders every action name immediately, so awaiting "role changed" would
    // resolve against a button while the list is still loading.
    expect(await screen.findByText(/rafa@foldex.test/)).toBeInTheDocument()
    expect(screen.getByText(/someone@foldex.test/)).toBeInTheDocument()
  })

  it('narrows to one action and restarts pagination when the filter changes', async () => {
    const get = mockAdminApi()
    renderWithProviders(<AuditSection />)
    await screen.findByText(/rafa@foldex.test/)

    await userEvent.setup().click(screen.getByRole('button', { name: /^failed sign-in$/i }))

    await waitFor(() =>
      expect(get).toHaveBeenCalledWith('/api/admin/audit', {
        params: { action: 'login.failed', before: undefined },
      }),
    )
  })

  // Keyset, not offset: the cursor is the last id shown.
  it('pages with the last id as the cursor', async () => {
    const get = mockAdminApi()
    renderWithProviders(<AuditSection />)
    // The pager only exists once a page has loaded.
    await screen.findByText(/rafa@foldex.test/)

    await userEvent.setup().click(screen.getByRole('button', { name: /older/i }))

    await waitFor(() =>
      expect(get).toHaveBeenCalledWith('/api/admin/audit', {
        params: { action: undefined, before: 8 },
      }),
    )
  })

  it('says so when nothing is recorded', async () => {
    mockAdminApi({ '/api/admin/audit': { entries: [] } })
    renderWithProviders(<AuditSection />)
    expect(await screen.findByText(/nothing recorded yet/i)).toBeInTheDocument()
  })
})

describe('PolicySection', () => {
  it('is read-only for an admin, mirroring the server', async () => {
    mockAdminApi()
    renderWithProviders(<PolicySection />)

    expect(await screen.findByText(/only the owner can change these rules/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^save$/i })).not.toBeInTheDocument()
  })

  it('lets the owner save, sending the domain list parsed at submit', async () => {
    mockAdminApi()
    const put = vi.spyOn(http, 'put').mockResolvedValue({ data: POLICY } as never)
    renderWithProviders(<PolicySection />, { session: ownerSession })

    const domains = await screen.findByLabelText(/allowed domains/i)
    await userEvent.setup().clear(domains)
    await userEvent.setup().type(domains, 'a.test\nB.TEST')
    await userEvent.setup().click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() =>
      expect(put).toHaveBeenCalledWith(
        '/api/admin/policy',
        // Lowercased and split at submit — a half-typed line must not be
        // dropped while the user is still typing it.
        expect.objectContaining({ google_allowed_domains: ['a.test', 'b.test'] }),
      ),
    )
  })

  // ADR-37: an admin whose only factor is e-mail is measurably weaker, because
  // the mailbox is already the recovery channel. The floor stays permissive and
  // the owner may tighten it — but only if the control exists, and it did not
  // when the READMEs already told owners to set it.
  it('lets the owner require an authenticator for administrators', async () => {
    mockAdminApi()
    const put = vi.spyOn(http, 'put').mockResolvedValue({ data: POLICY } as never)
    renderWithProviders(<PolicySection />, { session: ownerSession })

    const select = await screen.findByLabelText(/second factor accepted for administrators/i)
    expect(select).toHaveValue('any')

    await userEvent.setup().selectOptions(select, 'totp_only')
    await userEvent.setup().click(screen.getByRole('button', { name: /^save$/i }))

    await waitFor(() =>
      expect(put).toHaveBeenCalledWith(
        '/api/admin/policy',
        expect.objectContaining({ admin_second_factor: 'totp_only' }),
      ),
    )
  })

  it('surfaces the server refusing a value below the floor', async () => {
    mockAdminApi()
    vi.spyOn(http, 'put').mockRejectedValue({
      response: { data: { error: { code: 'invalid_policy' } } },
    } as never)
    renderWithProviders(<PolicySection />, { session: ownerSession })

    await userEvent.setup().click(await screen.findByRole('button', { name: /^save$/i }))

    expect(await screen.findByText(/outside the allowed range/i)).toBeInTheDocument()
  })

  // Owner and admin are the only roles that reach this screen at all, and the
  // default role offered can never be an administrative one.
  it('offers only non-administrative roles for auto-provisioned accounts', async () => {
    mockAdminApi()
    renderWithProviders(<PolicySection />, { session: ownerSession })

    const select = await screen.findByLabelText(/role for new accounts/i)
    const values = Array.from(select.querySelectorAll('option')).map((o) => o.getAttribute('value'))
    expect(values).toEqual(['editor', 'viewer'])
  })
})
