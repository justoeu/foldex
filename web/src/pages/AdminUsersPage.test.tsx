import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AdminUsersPage } from './AdminUsersPage'
import { renderWithProviders, testAdminUser } from '../test/renderWithProviders'
import { http } from '../api/client'
import type { AuthUser } from '../auth/types'

afterEach(() => vi.restoreAllMocks())

const me: AuthUser = { ...testAdminUser }

function user(over: Partial<AuthUser> & { id: number }): AuthUser {
  return {
    email: `user${over.id}@foldex.test`,
    name: 'Someone',
    role: 'editor',
    status: 'active',
    has_password: true,
    totp_enabled: false,
    created_at: '2026-01-01T00:00:00Z',
    ...over,
  }
}

function mockApi(users: AuthUser[], invites: unknown[] = []) {
  return vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
    if (url === '/api/admin/users') return { data: { users } } as never
    return { data: { invites } } as never
  })
}

function render(users: AuthUser[], invites: unknown[] = []) {
  mockApi(users, invites)
  return renderWithProviders(<AdminUsersPage />)
}

/** The row for one account, so assertions do not match another row's controls. */
async function rowFor(email: string) {
  const cell = await screen.findByText(email)
  // eslint-disable-next-line testing-library/no-node-access
  return within(cell.closest('tr') as HTMLElement)
}

describe('AdminUsersPage', () => {
  it('lists every account with its role and status', async () => {
    render([me, user({ id: 2, status: 'disabled' })])

    expect(await screen.findByText(me.email)).toBeInTheDocument()
    expect(screen.getByText('user2@foldex.test')).toBeInTheDocument()
    expect(screen.getByText(/disabled/i)).toBeInTheDocument()
  })

  it('marks accounts that sign in with Google only', async () => {
    render([me, user({ id: 2, has_password: false })])
    expect(await screen.findByText(/google only/i)).toBeInTheDocument()
  })

  /**
   * Both guards below are enforced by the SERVER inside a transaction — the UI
   * only mirrors them. Mirroring matters anyway: an admin who clicks a control
   * the server will refuse learns nothing except that something broke.
   */
  it('will not let an admin act on their own account', async () => {
    const row = await (render([me, user({ id: 2 })]), rowFor(me.email))

    expect(row.getByRole('combobox')).toBeDisabled()
    expect(row.getByRole('button', { name: /disable/i })).toBeDisabled()
    expect(row.getByRole('button', { name: new RegExp(`delete ${me.email}`, 'i') })).toBeDisabled()
  })

  it('will not let the last active administrator be removed', async () => {
    // A second admin exists but is DISABLED, so `me` is still the only active
    // one — the count the server applies is over ACTIVE admins, not all of them.
    const row = await (render([me, user({ id: 2, role: 'admin', status: 'disabled' })]),
    rowFor(me.email))
    expect(row.getByRole('button', { name: /disable/i })).toBeDisabled()
  })

  it('promotes a user', async () => {
    const u = userEvent.setup()
    const patch = vi.spyOn(http, 'patch').mockResolvedValue({ data: {} } as never)
    render([me, user({ id: 2 })])

    const row = await rowFor('user2@foldex.test')
    await u.selectOptions(row.getByRole('combobox'), 'admin')

    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/admin/users/2', { role: 'admin', status: undefined }),
    )
  })

  it('disables and re-enables an account', async () => {
    const u = userEvent.setup()
    const patch = vi.spyOn(http, 'patch').mockResolvedValue({ data: {} } as never)
    render([me, user({ id: 2 })])

    const row = await rowFor('user2@foldex.test')
    await u.click(row.getByRole('button', { name: /disable/i }))

    await waitFor(() =>
      expect(patch).toHaveBeenCalledWith('/api/admin/users/2', {
        role: undefined,
        status: 'disabled',
      }),
    )
  })

  // Deleting an account destroys everything it owns, so it goes through the
  // destructive confirmation and says so.
  it('confirms before deleting, and warns what goes with it', async () => {
    const u = userEvent.setup()
    const del = vi.spyOn(http, 'delete').mockResolvedValue({ data: {} } as never)
    render([me, user({ id: 2 })])

    const row = await rowFor('user2@foldex.test')
    await u.click(row.getByRole('button', { name: /delete user2@foldex\.test/i }))

    expect(await screen.findByText(/delete this account/i)).toBeInTheDocument()
    expect(screen.getByText(/links, notes, folders/i)).toBeInTheDocument()
    expect(del).not.toHaveBeenCalled()

    await u.click(screen.getByRole('button', { name: /^confirm$/i }))
    await waitFor(() => expect(del).toHaveBeenCalledWith('/api/admin/users/2'))
  })

  it('confirms that recovery went only to the target mailbox', async () => {
    const u = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    render([me, user({ id: 2, has_password: false })])

    const row = await rowFor('user2@foldex.test')
    await u.click(row.getByRole('button', { name: /send recovery/i }))
    await u.click(await screen.findByRole('button', { name: /^confirm$/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/admin/users/2/force-password-reset'),
    )
    expect(await screen.findByText(/recovery link was sent/i)).toBeInTheDocument()
    expect(screen.queryByTestId('temp-password')).not.toBeInTheDocument()
  })

  it('refuses a forced reset on your own account', async () => {
    const row = await (render([me, user({ id: 2 })]), rowFor(me.email))
    expect(row.getByRole('button', { name: /send recovery/i })).toBeDisabled()
  })

  // ── Invitations ─────────────────────────────────────────────────────

  it('shows the invite link once, because the log driver has no inbox', async () => {
    const u = userEvent.setup()
    vi.spyOn(http, 'post').mockResolvedValue({
      data: { id: 1, email: 'new@foldex.test', role: 'editor', accept_url: 'https://x/#invite=tok' },
    } as never)
    render([me])

    await u.type(await screen.findByLabelText(/e-mail address/i), 'new@foldex.test')
    await u.click(screen.getByRole('button', { name: /send invitation/i }))

    expect(await screen.findByTestId('invite-link')).toHaveTextContent('https://x/#invite=tok')
  })

  it('reports a duplicate address instead of failing silently', async () => {
    const u = userEvent.setup()
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 409, data: { error: { code: 'email_taken' } } },
    })
    render([me])

    await u.type(await screen.findByLabelText(/e-mail address/i), 'admin@foldex.test')
    await u.click(screen.getByRole('button', { name: /send invitation/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/already registered/i)
  })

  it('surfaces the server refusing to remove the last admin', async () => {
    const u = userEvent.setup()
    vi.spyOn(http, 'patch').mockRejectedValue({
      response: { status: 409, data: { error: { code: 'last_admin' } } },
    })
    // Two active admins, so the UI itself does not block the click — this is
    // about the SERVER's answer arriving after a concurrent demotion.
    render([me, user({ id: 2, role: 'admin' })])

    const row = await rowFor('user2@foldex.test')
    await u.click(row.getByRole('button', { name: /disable/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/last active administrator/i)
  })

  it('revokes every session for an account', async () => {
    const u = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    render([me, user({ id: 2 })])

    const row = await rowFor('user2@foldex.test')
    await u.click(row.getByRole('button', { name: /sign out everywhere/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/admin/users/2/sessions/revoke'),
    )
  })
})
