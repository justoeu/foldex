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
    // The owner holds EVERY permission — the server resolves that role from
    // the compiled matrix and never from the store, so a fixture where it
    // holds two would test a state the server cannot produce.
    {
      role: 'owner',
      permissions: [
        'content.read', 'content.write',
        'backup.export', 'backup.restore', 'import.run',
        'users.read', 'users.write', 'roles.assign', 'invites.read', 'invites.write',
        'audit.read', 'policy.read', 'policy.write', 'instance.transfer',
      ],
      user_count: 1,
      editable: false,
    },
    { role: 'admin', permissions: ['content.read', 'users.write'], user_count: 2, editable: true },
    { role: 'editor', permissions: ['content.read', 'content.write'], user_count: 11, editable: true },
    { role: 'viewer', permissions: ['content.read'], user_count: 4, editable: true },
  ],
  locked: ['content.read', 'roles.assign', 'policy.write', 'instance.transfer'],
  caller_role: 'owner',
  can_edit: true,
  editable_disabled: false,
  // The full ordered vocabulary the server sends — the matrix's rows. A short
  // fixture would hide every case about a permission it happened to omit.
  permissions: [
    'content.read', 'content.write',
    'backup.export', 'backup.restore', 'import.run',
    'users.read', 'users.write', 'roles.assign', 'invites.read', 'invites.write',
    'audit.read', 'policy.read', 'policy.write', 'instance.transfer',
  ],
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
  /** The checkbox for one cell of the grid. */
  const cell = (permission: string, role: string) =>
    // The dot is escaped: unescaped, `content.read` would also match a future
    // `contentXread` and the query would stop being about one cell.
    screen.getByRole('checkbox', {
      name: new RegExp(`^${permission.replace(/\./g, '\\.')} for ${role}$`, 'i'),
    })

  it('renders every role from the server, including one nobody holds', async () => {
    mockAdminApi({
      '/api/admin/roles': {
        ...ROLES,
        roles: [...ROLES.roles.slice(0, 3), { role: 'viewer', permissions: [], user_count: 0, editable: true }],
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

  // The whole reason the chip list became a grid: a chip list can only show
  // what a role HAS, so "denied" and "does not exist" looked identical.
  it('shows an absence, not just what each role holds', async () => {
    mockAdminApi()
    renderWithProviders(<RolesMatrix />)

    expect(await screen.findByText('Owner')).toBeInTheDocument()
    expect(cell('content.write', 'Editor')).toBeChecked()
    expect(cell('content.write', 'Viewer')).not.toBeChecked()
  })

  it('never offers to edit the owner column', async () => {
    mockAdminApi()
    renderWithProviders(<RolesMatrix />)
    await screen.findByText('Owner')

    expect(cell('users.write', 'Owner')).toBeDisabled()
    expect(cell('users.write', 'Editor')).toBeEnabled()
  })

  it('never offers a locked permission on any role', async () => {
    mockAdminApi()
    renderWithProviders(<RolesMatrix />)
    await screen.findByText('Owner')

    // roles.assign is the meta-permission; content.read is locked in the other
    // direction, so it cannot be REMOVED either.
    for (const role of ['Admin', 'Editor', 'Viewer']) {
      expect(cell('roles.assign', role)).toBeDisabled()
      expect(cell('content.read', role)).toBeDisabled()
    }
  })

  // An admin must not be able to give itself owner-level powers. The gate is
  // stated against what the CALLER holds, so it also covers a permission the
  // admin simply lost.
  it('refuses to GRANT what the caller does not hold, and still allows revoking', async () => {
    mockAdminApi({
      '/api/admin/roles': {
        ...ROLES,
        caller_role: 'admin',
        // The admin has no import.run; the editor does.
        roles: [
          { role: 'owner', permissions: ['content.read'], user_count: 1, editable: false },
          { role: 'admin', permissions: ['content.read', 'users.write'], user_count: 2, editable: true },
          { role: 'editor', permissions: ['content.read', 'import.run'], user_count: 3, editable: true },
          { role: 'viewer', permissions: ['content.read'], user_count: 4, editable: true },
        ],
      },
    })
    renderWithProviders(<RolesMatrix />)
    await screen.findByText('Owner')

    // Granting it to a role that lacks it: refused.
    expect(cell('import.run', 'Viewer')).toBeDisabled()
    // Taking it away from a role that HAS it: allowed, or an admin could never
    // undo a grant the owner made.
    expect(cell('import.run', 'Editor')).toBeEnabled()
  })

  it('renders read-only, with a reason, for a caller who cannot write', async () => {
    mockAdminApi({ '/api/admin/roles': { ...ROLES, can_edit: false } })
    renderWithProviders(<RolesMatrix />)

    expect(await screen.findByText(/can read this matrix but not change it/i)).toBeInTheDocument()
    expect(cell('content.write', 'Editor')).toBeDisabled()
    expect(screen.queryByRole('button', { name: /save permissions/i })).toBeNull()
  })

  it('says so when the instance serves the compiled matrix', async () => {
    mockAdminApi({ '/api/admin/roles': { ...ROLES, can_edit: false, editable_disabled: true } })
    renderWithProviders(<RolesMatrix />)
    expect(await screen.findByText(/built-in matrix, which cannot be configured/i)).toBeInTheDocument()
  })

  it('sends the FULL set for each changed role, and only for changed roles', async () => {
    mockAdminApi()
    const put = vi.spyOn(http, 'put').mockResolvedValue({ data: { roles: ROLES.roles } } as never)
    renderWithProviders(<RolesMatrix />)
    await screen.findByText('Owner')

    const user = userEvent.setup()
    await user.click(cell('backup.export', 'Viewer'))
    await user.click(screen.getByRole('button', { name: /save permissions/i }))

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
    // Absent means revoked, so the whole resulting set travels — and the locked
    // entries are stripped, because Resolve puts them back from the compiled
    // matrix and storing them would create a second source of truth.
    expect(put).toHaveBeenCalledWith('/api/admin/roles/viewer/permissions', {
      permissions: ['backup.export'],
    })
  })

  it('keeps Save unreachable until something actually changed', async () => {
    mockAdminApi()
    renderWithProviders(<RolesMatrix />)
    await screen.findByText('Owner')

    const save = screen.getByRole('button', { name: /save permissions/i })
    expect(save).toBeDisabled()

    const user = userEvent.setup()
    await user.click(cell('backup.export', 'Viewer'))
    expect(save).toBeEnabled()

    // Toggling back is not "dirty": the set matches the server again.
    await user.click(cell('backup.export', 'Viewer'))
    expect(save).toBeDisabled()
  })

  it('surfaces the server refusal instead of a generic failure', async () => {
    mockAdminApi()
    vi.spyOn(http, 'put').mockRejectedValue({
      response: { status: 403, data: { error: { code: 'permission_escalation' } } },
    })
    renderWithProviders(<RolesMatrix />)
    await screen.findByText('Owner')

    const user = userEvent.setup()
    await user.click(cell('backup.export', 'Viewer'))
    await user.click(screen.getByRole('button', { name: /save permissions/i }))

    expect(await screen.findByRole('alert'))
      .toHaveTextContent(/cannot grant a permission your own role does not hold/i)
  })

  // The PUT already answers with the whole matrix, resolved after its own
  // write. Refetching would be a fourth request for an answer in hand.
  //
  // The server's answer deliberately DIFFERS from the draft: with the two
  // identical, the checkbox stays checked whether or not the component writes
  // the response into the cache, and dropping setQueryData altogether left
  // this green. What is under test is that the grid follows the SERVER.
  it('takes the fresh matrix from the write, without a further GET', async () => {
    const get = mockAdminApi()
    // The owner ticked backup.export for the viewer; the server answers with
    // content.write instead — a state only the response can explain.
    const fresh = ROLES.roles.map((r) =>
      r.role === 'viewer' ? { ...r, permissions: ['content.read', 'content.write'] } : r,
    )
    vi.spyOn(http, 'put').mockResolvedValue({ data: { roles: fresh } } as never)
    renderWithProviders(<RolesMatrix />)
    await screen.findByText('Owner')
    const before = get.mock.calls.filter(([url]) => url === '/api/admin/roles').length

    const user = userEvent.setup()
    await user.click(cell('backup.export', 'Viewer'))
    await user.click(screen.getByRole('button', { name: /save permissions/i }))

    // The grid follows the server, not the draft.
    await waitFor(() => expect(cell('content.write', 'Viewer')).toBeChecked())
    expect(cell('backup.export', 'Viewer')).not.toBeChecked()
    // And the draft is gone, so Save is unreachable again.
    expect(screen.getByRole('button', { name: /save permissions/i })).toBeDisabled()
    // Without a further GET.
    expect(get.mock.calls.filter(([url]) => url === '/api/admin/roles').length).toBe(before)
  })

  // Each refusal is a different sentence to the person reading it; the backend
  // emits four distinct codes precisely so the screen can say which.
  it.each([
    ['role_not_editable', /owner always holds every permission/i],
    ['permission_locked', /not configurable on any role/i],
    ['permission_escalation', /cannot grant a permission your own role does not hold/i],
    ['roles_not_configurable', /built-in matrix, which cannot be configured/i],
    ['something_unmapped', /something went wrong|try again/i],
  ])('maps the server code %s to its own message', async (code, expected) => {
    mockAdminApi()
    vi.spyOn(http, 'put').mockRejectedValue({
      response: { status: 403, data: { error: { code } } },
    })
    renderWithProviders(<RolesMatrix />)
    await screen.findByText('Owner')

    const user = userEvent.setup()
    await user.click(cell('backup.export', 'Viewer'))
    await user.click(screen.getByRole('button', { name: /save permissions/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(expected)
  })

  // The only way to discard a draft without un-toggling every box by hand.
  it('discards the draft on Cancel, and clears a standing error with it', async () => {
    mockAdminApi()
    vi.spyOn(http, 'put').mockRejectedValue({
      response: { status: 403, data: { error: { code: 'permission_escalation' } } },
    })
    renderWithProviders(<RolesMatrix />)
    await screen.findByText('Owner')

    const user = userEvent.setup()
    await user.click(cell('backup.export', 'Viewer'))
    await user.click(screen.getByRole('button', { name: /save permissions/i }))
    expect(await screen.findByRole('alert')).toBeInTheDocument()

    await user.click(cell('backup.export', 'Viewer'))
    await user.click(screen.getByRole('button', { name: /cancel/i }))

    expect(screen.queryByRole('alert')).toBeNull()
    await waitFor(() => expect(cell('backup.export', 'Viewer')).not.toBeChecked())
    expect(screen.getByRole('button', { name: /save permissions/i })).toBeDisabled()
  })

  // A vocabulary the server grew and this file did not must not VANISH from
  // the one screen whose job is to show what the server enforces.
  it('still renders a permission it does not know how to group', async () => {
    mockAdminApi({
      '/api/admin/roles': {
        ...ROLES,
        permissions: [...ROLES.permissions, 'webhooks.write'],
      },
    })
    renderWithProviders(<RolesMatrix />)
    expect(await screen.findByText('webhooks.write')).toBeInTheDocument()
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
