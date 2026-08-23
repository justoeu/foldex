import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AdminUsersPage } from './AdminUsersPage'
import { renderWithProviders, testAdminUser, testAdminSession } from '../test/renderWithProviders'
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

function mockApi(users: AuthUser[], invites: unknown[] = [], taken: string[] = []) {
  return vi.spyOn(http, 'get').mockImplementation(async (url: string, cfg?: any) => {
    if (url === '/api/admin/users') return { data: { users } } as never
    // The create dialog probes this while the administrator types. Without it
    // the catch-all answered `{invites}`, `available` came back undefined, and
    // the dialog treated every address as taken — which is also why the Add
    // button's gating needs a test of its own rather than a lucky default.
    if (url === '/api/admin/users/email-available') {
      const email = String(cfg?.params?.email ?? '').trim().toLowerCase()
      return { data: taken.includes(email) ? { available: false, reason: 'taken' } : { available: true } } as never
    }
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

  // ── Adding a user ───────────────────────────────────────────────────
  //
  // A deliberate exception to §4 ("an administrator never chooses another
  // user's credential"), taken by the instance owner with the trade stated.
  // The warning and the role floor are what is left of the rule.

  it('offers to add a user and sends the typed credential', async () => {
    const u = userEvent.setup()
    render([me])
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: { ...me, id: 9 } } as never)

    await u.click(await screen.findByRole('button', { name: /add user/i }))
    const dlg = within(screen.getByRole('dialog', { name: /add a user/i }))

    await u.type(dlg.getByLabelText(/e-?mail/i), 'new@foldex.test')
    await u.type(dlg.getByLabelText(/display name/i), 'New Person')
    await u.type(dlg.getByLabelText(/temporary password/i), 'a fine temporary password')
    await u.click(dlg.getByRole('button', { name: /add user/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/admin/users', {
        email: 'new@foldex.test',
        name: 'New Person',
        password: 'a fine temporary password',
        role: 'editor',
      }),
    )
  })

  // The dialog must SAY what it costs. The owner accepted the trade knowing it;
  // the next administrator to open this dialog did not.
  it('states that two people will know the password', async () => {
    const u = userEvent.setup()
    render([me])

    await u.click(await screen.findByRole('button', { name: /add user/i }))
    expect(
      within(screen.getByRole('dialog')).getByText(/two people will know it/i),
    ).toBeInTheDocument()
  })

  // owner is refused by the server; offering it would be a promise the API breaks.
  it('does not offer the owner role', async () => {
    const u = userEvent.setup()
    render([me])

    await u.click(await screen.findByRole('button', { name: /add user/i }))
    const roles = within(screen.getByRole('dialog')).getByLabelText(/role/i)
    expect(within(roles).queryByRole('option', { name: /owner/i })).not.toBeInTheDocument()
    expect(within(roles).getByRole('option', { name: /editor/i })).toBeInTheDocument()
  })

  it('keeps submit disabled until the password clears the floor', async () => {
    const u = userEvent.setup()
    render([me])

    await u.click(await screen.findByRole('button', { name: /add user/i }))
    const dlg = within(screen.getByRole('dialog'))
    await u.type(dlg.getByLabelText(/e-?mail/i), 'new@foldex.test')
    await u.type(dlg.getByLabelText(/temporary password/i), 'short')

    expect(dlg.getByRole('button', { name: /add user/i })).toBeDisabled()
  })

  // The floor is owner-configurable (ADR-35), so a client constant cannot state
  // it. An instance demanding twenty characters was being told "at least 8".
  it('reports the SERVER floor, not the client constant', async () => {
    const u = userEvent.setup()
    render([me])
    vi.spyOn(http, 'post').mockRejectedValue({
      response: {
        status: 400,
        data: { error: { code: 'password_too_short', message: 'password must be at least 20 characters' } },
      },
    })

    await u.click(await screen.findByRole('button', { name: /add user/i }))
    const dlg = within(screen.getByRole('dialog'))
    await u.type(dlg.getByLabelText(/e-?mail/i), 'new@foldex.test')
    await u.type(dlg.getByLabelText(/temporary password/i), 'twelve chars')
    await u.click(dlg.getByRole('button', { name: /add user/i }))

    expect(await dlg.findByRole('alert')).toHaveTextContent(/at least 20 characters/i)
  })

  // Without this, deleting onClose()/invalidateQueries survives: every other
  // case stops at the POST.
  it('closes and refreshes the list on success', async () => {
    const u = userEvent.setup()
    const get = mockApi([me])
    renderWithProviders(<AdminUsersPage />)
    vi.spyOn(http, 'post').mockResolvedValue({ data: { ...me, id: 9 } } as never)

    await u.click(await screen.findByRole('button', { name: /add user/i }))
    const dlg = within(screen.getByRole('dialog'))
    await u.type(dlg.getByLabelText(/e-?mail/i), 'new@foldex.test')
    await u.type(dlg.getByLabelText(/temporary password/i), 'a fine temporary password')
    const before = get.mock.calls.filter(([url]) => url === '/api/admin/users').length
    await u.click(dlg.getByRole('button', { name: /add user/i }))

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    // The list is re-read, or the new account would not appear until a reload.
    await waitFor(() =>
      expect(get.mock.calls.filter(([url]) => url === '/api/admin/users').length)
        .toBeGreaterThan(before),
    )
  })

  it('surfaces a duplicate address without closing the dialog', async () => {
    const u = userEvent.setup()
    render([me])
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 409, data: { error: { code: 'email_taken' } } },
    })

    await u.click(await screen.findByRole('button', { name: /add user/i }))
    const dlg = within(screen.getByRole('dialog'))
    await u.type(dlg.getByLabelText(/e-?mail/i), 'admin@foldex.test')
    await u.type(dlg.getByLabelText(/temporary password/i), 'a fine temporary password')
    await u.click(dlg.getByRole('button', { name: /add user/i }))

    expect(await dlg.findByRole('alert')).toHaveTextContent(/already registered/i)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  // Every row action is icon-only now, so the accessible name is the ONLY thing
  // naming it. Transfer was the one with no test at all: deleting its aria-label
  // left an unnamed glyph that moves ownership, and nothing went red. The other
  // four are named by the queries already used throughout this file.
  it('names every icon-only row action for a screen reader', async () => {
    mockApi([{ ...me, role: 'owner' }, user({ id: 2 })])
    renderWithProviders(<AdminUsersPage />, {
      session: { ...testAdminSession, user: { ...testAdminUser, role: 'owner' } } as never,
    })

    const row = await rowFor('user2@foldex.test')
    for (const name of [
      /disable \(user2@foldex\.test\)/i,
      /sign out everywhere \(user2@foldex\.test\)/i,
      /send recovery \(user2@foldex\.test\)/i,
      /transfer ownership \(user2@foldex\.test\)/i,
      /delete user2@foldex\.test/i,
    ]) {
      expect(row.getByRole('button', { name })).toBeInTheDocument()
    }
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

describe('creating a user', () => {
  // The whole admin half of the availability feature had no test: replacing the
  // gate with `false` left the suite green, so the hint and the block could
  // both regress to nothing and the administrator would learn the address was
  // taken only from the 409 the probe exists to pre-empt.
  it('warns and blocks Add user on an address that already has an account', async () => {
    mockApi([me], [], ['taken@foldex.test'])
    renderWithProviders(<AdminUsersPage />, { session: testAdminSession })
    const u = userEvent.setup()

    await u.click(await screen.findByRole('button', { name: /add a user|add user/i }))
    const dialog = within(await screen.findByRole('dialog'))
    await u.type(dialog.getByLabelText(/e-mail/i), 'taken@foldex.test')
    // A valid password too, so the availability refusal is the ONLY thing left
    // that could disable the button. Without this the assertion passes on an
    // empty password field and the gate itself goes unmeasured — which is
    // exactly how the first version of this test survived deleting the gate.
    await u.type(dialog.getByLabelText(/^temporary password$/i), 'a long enough password')

    expect(await dialog.findByText(/already in use/i)).toBeInTheDocument()
    expect(dialog.getByRole('button', { name: /add user/i })).toBeDisabled()
  })

  // A free address must leave the button reachable — the mirror case, without
  // which the test above passes on a dialog that blocks everything.
  it('leaves Add user reachable on a free address', async () => {
    mockApi([me])
    renderWithProviders(<AdminUsersPage />, { session: testAdminSession })
    const u = userEvent.setup()

    await u.click(await screen.findByRole('button', { name: /add a user|add user/i }))
    const dialog = within(await screen.findByRole('dialog'))
    await u.type(dialog.getByLabelText(/e-mail/i), 'new@foldex.test')
    await u.type(dialog.getByLabelText(/^temporary password$/i), 'a long enough password')

    await waitFor(() => expect(dialog.getByText(/available/i)).toBeInTheDocument())
    expect(dialog.getByRole('button', { name: /add user/i })).toBeEnabled()
  })
})
