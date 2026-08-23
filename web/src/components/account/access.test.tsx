import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AccountPage } from '../../pages/AccountPage'
import { renderWithProviders, testAdminUser } from '../../test/renderWithProviders'
import { http } from '../../api/client'
import * as auth from '../../api/auth'
import type { AuthUser, SessionState } from '../../auth/types'

afterEach(() => vi.restoreAllMocks())

const googleOn = { google_oauth: true, two_factor: true, email_delivery: true }

function sessionWith(over: Partial<AuthUser>, features = googleOn): SessionState {
  return {
    status: 'authenticated',
    user: { ...testAdminUser, ...over },
    csrfToken: 'test-csrf-token',
    features,
  }
}

function mockIdentities(list: unknown[]) {
  return vi.spyOn(http, 'get').mockResolvedValue({ data: { identities: list } } as never)
}

function render(session: SessionState) {
  mockIdentities([])
  // The sign-in methods live behind their own rail item now; every case in this
  // file is about that panel.
  return renderWithProviders(<AccountPage initialTab="access" />, { session })
}

/**
 * Opens the password disclosure.
 *
 * The form is behind a toggle now: an account WITH a password used to see no
 * form at all (the old card only offered one when there was none), so the page
 * showed a line of status and nothing to do. Collapsed-by-default is what lets
 * both branches — change and create — live in one row.
 */
async function openPasswordForm(user: ReturnType<typeof userEvent.setup>) {
  await user.click(within(passwordRow()).getByRole('button', { name: /set a password|change password/i }))
  return within(passwordRow())
}

/** The password row, so a query cannot match the hero chip reporting the same
 *  state one band above, or the Google row's own "current password" field. */
function passwordRow() {
  return screen.getByRole('group', { name: /^password$/i })
}

function googleRow() {
  return within(screen.getByRole('group', { name: /^google$/i }))
}

/**
 * The card, scoped by its own heading.
 *
 * The hero one band above reports the SAME password state with the same
 * string, so a bare `getByText('No password')` matches two elements — which is
 * the ambiguity that makes an unscoped chip assertion useless rather than
 * merely fragile.
 */
function accessCard() {
  const card = screen.getByRole('heading', { name: /ways in/i }).closest('section')
  if (!card) throw new Error('the access card has no section around its heading')
  return within(card as HTMLElement)
}

/** The step-up dialog. Its fields duplicate the page's — by design, the proof
 *  is the same — so a bare query would match two-factor's copy as well. */
function linkDialog() {
  return within(screen.getByRole('dialog'))
}

describe('account page — sign-in methods', () => {
  it('says a password is set and offers to change it, not to create one', () => {
    render(sessionWith({ has_password: true }))
    const row = within(passwordRow())
    expect(row.getByText(/a password is set/i)).toBeInTheDocument()
    expect(row.getByRole('button', { name: /change password/i })).toBeInTheDocument()
    expect(row.queryByRole('button', { name: /set a password/i })).not.toBeInTheDocument()
  })

  // The chip is the only summary of credential posture visible without opening
  // a row, and nothing asserted it: hardcoding `on: true` in PasswordRow made
  // the row claim "Set" on an account with no password, with the whole file
  // green. The hint is a separate element and was already covered, so a query
  // that matched either would not have caught it.
  it('states each method in a chip, not only in the hint', () => {
    render(sessionWith({ has_password: false }))
    expect(within(passwordRow()).getByText('Not set')).toBeInTheDocument()
    expect(googleRow().getByText('Not set')).toBeInTheDocument()
    // ...and the card's own badge summarises the same thing one level up.
    expect(accessCard().getByText('No password')).toBeInTheDocument()
  })

  it('flips the chip and the card badge once a password exists', () => {
    render(sessionWith({ has_password: true }))
    expect(within(passwordRow()).getByText('Set')).toBeInTheDocument()
    expect(accessCard().getByText('Password set')).toBeInTheDocument()
  })

  // The server enforces the floor either way, so this is not a bypass — it is
  // a needless round-trip and a refusal the user could have been spared.
  it('keeps save disabled while the new password is under the floor', async () => {
    const user = userEvent.setup()
    render(sessionWith({ has_password: true, totp_enabled: false, email_2fa_enabled: false }))
    const row = await openPasswordForm(user)

    await user.type(row.getByLabelText(/current password/i), 'old-password-here')
    await user.type(row.getByLabelText(/^new password$/i), 'abc')
    await user.type(row.getByLabelText(/confirm the password/i), 'abc')

    expect(row.getByRole('button', { name: /save password/i })).toBeDisabled()
  })

  it('offers to set one when the account is Google-only', () => {
    render(sessionWith({ has_password: false }))
    const row = within(passwordRow())
    expect(row.getByText(/no password/i)).toBeInTheDocument()
    expect(row.getByRole('button', { name: /set a password/i })).toBeInTheDocument()
  })

  // The credential being created OUTLIVES the session presenting the request,
  // and there is no current password to prove, so the authenticator is the only
  // step-up available.
  it('demands the authenticator code when the account has one', async () => {
    const user = userEvent.setup()
    render(sessionWith({ has_password: false, totp_enabled: true }))

    const row = await openPasswordForm(user)
    const submit = row.getByRole('button', { name: /save password/i })
    await user.type(row.getByLabelText(/new password/i), 'a good new password')
    await user.type(row.getByLabelText(/confirm the password/i), 'a good new password')
    expect(submit).toBeDisabled()

    const cells = row.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')
    await waitFor(() => expect(submit).toBeEnabled())
  })

  // ADR-37 made e-mail a factor an account can hold on its own, and the server
  // demands a step-up proof from ANY account that has one. Gating this field on
  // totp_enabled left an e-mail-only account with the field hidden, the button
  // enabled, and a refusal it had no way to answer — a dead end on the happy
  // path of the feature that created the state.
  it('demands a code from an account whose only factor is e-mail', async () => {
    const user = userEvent.setup()
    render(sessionWith({ has_password: false, totp_enabled: false, email_2fa_enabled: true }))

    const row = await openPasswordForm(user)
    const submit = row.getByRole('button', { name: /save password/i })
    await user.type(row.getByLabelText(/new password/i), 'a good new password')
    await user.type(row.getByLabelText(/confirm the password/i), 'a good new password')
    expect(submit).toBeDisabled()

    const cells = row.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')
    await waitFor(() => expect(submit).toBeEnabled())
  })

  // The only proof such an account can produce here without spending a recovery
  // code, which is a lockout credential rather than an everyday one.
  it('offers to mail a code to an e-mail-only account', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    render(sessionWith({ has_password: false, totp_enabled: false, email_2fa_enabled: true }))

    const row = await openPasswordForm(user)
    await user.click(row.getByRole('button', { name: /e-mail me a code/i }))
    expect(post).toHaveBeenCalledWith('/api/auth/2fa/email/send')
  })

  it('leaves the code field out when the account has no second factor', async () => {
    const user = userEvent.setup()
    render(sessionWith({ has_password: false, totp_enabled: false }))

    const row = await openPasswordForm(user)
    expect(row.queryByLabelText(/current 6-digit code/i)).not.toBeInTheDocument()
    expect(row.queryByRole('button', { name: /e-mail me a code/i })).not.toBeInTheDocument()
  })

  it('refuses to submit two passwords that disagree', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post')
    render(sessionWith({ has_password: false }))

    const row = await openPasswordForm(user)
    await user.type(row.getByLabelText(/new password/i), 'a good new password')
    await user.type(row.getByLabelText(/confirm the password/i), 'a different one')
    await user.click(row.getByRole('button', { name: /save password/i }))

    expect(await row.findByRole('alert')).toHaveTextContent(/do not match/i)
    expect(post).not.toHaveBeenCalled()
  })

  it('sends the new password', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    render(sessionWith({ has_password: false }))

    const row = await openPasswordForm(user)
    await user.type(row.getByLabelText(/new password/i), 'a good new password')
    await user.type(row.getByLabelText(/confirm the password/i), 'a good new password')
    await user.click(row.getByRole('button', { name: /save password/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/password/set', {
        password: 'a good new password',
        code: '',
      }),
    )
  })

  // ── Changing an existing password ───────────────────────────────────
  //
  // POST /api/auth/password/change existed since ADR-30, was tested on the
  // server, and NOTHING in the SPA called it: changing a password meant signing
  // out and using the reset-by-e-mail flow — a recovery path standing in for an
  // ordinary edit, on an account whose owner is signed in and proving nothing.

  it('offers to change the password when the account already has one', async () => {
    const user = userEvent.setup()
    render(sessionWith({ has_password: true }))

    const row = await openPasswordForm(user)
    expect(row.getByLabelText(/current password/i)).toBeInTheDocument()
    expect(row.getByLabelText(/new password/i)).toBeInTheDocument()
  })

  it('sends the current and the new password', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    render(sessionWith({ has_password: true }))

    const row = await openPasswordForm(user)
    await user.type(row.getByLabelText(/current password/i), 'the old one')
    await user.type(row.getByLabelText(/new password/i), 'a good new password')
    await user.type(row.getByLabelText(/confirm the password/i), 'a good new password')
    await user.click(row.getByRole('button', { name: /save password/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/password/change', {
        current_password: 'the old one',
        new_password: 'a good new password',
      }),
    )
  })

  // No second factor is asked for on this branch, and that is the contract, not
  // an omission: the CURRENT password is the step-up. Asking for a code as well
  // would lock out an account whose owner knows their password and has an
  // authenticator they cannot reach.
  it('does not ask for a second factor when the current password is the proof', async () => {
    const user = userEvent.setup()
    render(sessionWith({ has_password: true, totp_enabled: true }))

    // `/current 6-digit code/i` was used here and matched NOTHING: that is
    // TwoFactorSection's label, in another band. The test passed against a
    // mutant that always rendered the field.
    const row = await openPasswordForm(user)
    expect(row.queryByLabelText(/code from your authenticator/i)).not.toBeInTheDocument()
    expect(row.getByLabelText(/current password/i)).toBeInTheDocument()
  })

  // The positive counterpart. Without it the negative above is satisfied by a
  // tree where that label never renders anywhere.
  it('does ask for the authenticator code on the create branch', async () => {
    const user = userEvent.setup()
    render(sessionWith({ has_password: false, totp_enabled: true }))

    const row = await openPasswordForm(user)
    expect(row.getByLabelText(/code from your authenticator/i)).toBeInTheDocument()
  })

  // A Google-only account has no current password; asking for one would be a
  // field it can never fill.
  it('does not ask for a current password on the create branch', async () => {
    const user = userEvent.setup()
    render(sessionWith({ has_password: false }))

    const row = await openPasswordForm(user)
    expect(row.queryByLabelText(/current password/i)).not.toBeInTheDocument()
  })

  it('keeps save disabled until the current password is typed', async () => {
    const user = userEvent.setup()
    render(sessionWith({ has_password: true }))

    const row = await openPasswordForm(user)
    await user.type(row.getByLabelText(/new password/i), 'a good new password')
    await user.type(row.getByLabelText(/confirm the password/i), 'a good new password')
    expect(row.getByRole('button', { name: /save password/i })).toBeDisabled()

    await user.type(row.getByLabelText(/current password/i), 'the old one')
    expect(row.getByRole('button', { name: /save password/i })).toBeEnabled()
  })

  // The whole success outcome was untested: the form closing, the confirmation,
  // and the /me reload that flips has_password (and the hero chip) on create.
  it('closes the form and confirms after a successful change', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    const get = vi.spyOn(http, 'get')
    render(sessionWith({ has_password: true }))

    const row = await openPasswordForm(user)
    await user.type(row.getByLabelText(/current password/i), 'the old one')
    await user.type(row.getByLabelText(/new password/i), 'a good new password')
    await user.type(row.getByLabelText(/confirm the password/i), 'a good new password')
    await user.click(row.getByRole('button', { name: /save password/i }))

    expect(await screen.findByText(/password changed/i)).toBeInTheDocument()
    expect(within(passwordRow()).queryByLabelText(/new password/i)).not.toBeInTheDocument()
    await waitFor(() => expect(get).toHaveBeenCalledWith('/api/auth/me'))
  })

  // A spent code resubmitted verbatim burns another slot from the server's
  // step-up budget, and on the e-mail path another message.
  it('clears the code but keeps the passwords when the step-up is rejected', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 401, data: { error: { code: 'invalid_code' } } },
    })
    render(sessionWith({ has_password: false, totp_enabled: true }))

    const row = await openPasswordForm(user)
    await user.type(row.getByLabelText(/new password/i), 'a good new password')
    await user.type(row.getByLabelText(/confirm the password/i), 'a good new password')
    const cells = row.getAllByRole('textbox') as HTMLInputElement[]
    cells[0]!.focus()
    await user.paste('123456')
    await user.click(row.getByRole('button', { name: /save password/i }))

    await row.findByRole('alert')
    expect(row.getByLabelText(/new password/i)).toHaveValue('a good new password')
    expect((row.getAllByRole('textbox') as HTMLInputElement[])[0]!.value).toBe('')
  })

  it('says so when the current password is wrong, and keeps the form open', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 401, data: { error: { code: 'invalid_credentials' } } },
    })
    render(sessionWith({ has_password: true }))

    const row = await openPasswordForm(user)
    await user.type(row.getByLabelText(/current password/i), 'wrong')
    await user.type(row.getByLabelText(/new password/i), 'a good new password')
    await user.type(row.getByLabelText(/confirm the password/i), 'a good new password')
    await user.click(row.getByRole('button', { name: /save password/i }))

    expect(await row.findByRole('alert')).toBeInTheDocument()
    expect(row.getByLabelText(/new password/i)).toHaveValue('a good new password')
  })

  // The form is collapsed by default; without that, an account WITH a password
  // saw no form at all under the old card, which is what made the whole screen
  // a line of status with nothing to do.
  it('keeps the form collapsed until asked', () => {
    render(sessionWith({ has_password: true }))
    expect(within(passwordRow()).queryByLabelText(/new password/i)).not.toBeInTheDocument()
  })

  // ── Google ──────────────────────────────────────────────────────────

  it('hides the Google controls when the instance has no client configured', () => {
    render(sessionWith({ has_password: true }, { ...googleOn, google_oauth: false }))
    expect(screen.queryByRole('button', { name: /connect google/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /disconnect google/i })).not.toBeInTheDocument()
  })

  it('offers to connect Google when nothing is linked', async () => {
    render(sessionWith({ has_password: true }))
    expect(await screen.findByRole('button', { name: /connect google/i })).toBeInTheDocument()
  })

  it('opens a credential step-up dialog before connecting Google', async () => {
    const user = userEvent.setup()
    render(sessionWith({ has_password: true }))

    await user.click(await screen.findByRole('button', { name: /connect google/i }))

    expect(screen.getByRole('dialog', { name: /connect google/i })).toBeInTheDocument()
    expect(linkDialog().getByLabelText(/current password/i)).toBeInTheDocument()
    expect(linkDialog().queryByLabelText(/code from your authenticator/i)).not.toBeInTheDocument()
  })

  it('posts the password proof before using the API redirect URL', async () => {
    const user = userEvent.setup()
    const navigate = vi.spyOn(auth, 'navigateToOAuth').mockImplementation(() => {})
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      data: { redirect_url: 'https://accounts.google.test/oauth?state=safe-state' },
    } as never)
    render(sessionWith({ has_password: true }))

    await user.click(await screen.findByRole('button', { name: /connect google/i }))
    await user.type(linkDialog().getByLabelText(/current password/i), 'hunter2hunter2')
    await user.click(screen.getByRole('button', { name: /continue to google/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/oauth/google/start', {
        current_password: 'hunter2hunter2',
        code: '',
      }),
    )
    expect(post.mock.calls[0]?.[0]).not.toContain('hunter2hunter2')
    expect(navigate).toHaveBeenCalledWith('https://accounts.google.test/oauth?state=safe-state')
  })

  it('requires TOTP or a recovery code when two-step verification is enabled', async () => {
    const user = userEvent.setup()
    render(sessionWith({ has_password: true, totp_enabled: true }))

    await user.click(await screen.findByRole('button', { name: /connect google/i }))
    const dlg = linkDialog()
    expect(dlg.getByText(/code from your authenticator/i)).toBeInTheDocument()
    expect(dlg.getByRole('button', { name: /use a recovery code/i })).toBeInTheDocument()

    await user.click(dlg.getByRole('button', { name: /use a recovery code/i }))
    expect(dlg.getByLabelText(/recovery code/i)).toBeInTheDocument()
  })

  it('shows the linked address once Google is connected', async () => {
    mockIdentities([{ provider: 'google', email_at_link: 'me@gmail.test', created_at: '2026-01-01' }])
    renderWithProviders(<AccountPage initialTab="access" />, { session: sessionWith({ has_password: true }) })

    expect(await screen.findByText(/me@gmail\.test/)).toBeInTheDocument()
  })

  /**
   * The ordering rule, mirrored in the UI so the user never reaches a dead end.
   *
   * A Google-only account that unlinked would have no way to sign in at all —
   * the database refuses that outright — so the button is not offered until a
   * password exists.
   */
  it('will not offer to disconnect Google while it is the only credential', async () => {
    mockIdentities([{ provider: 'google', email_at_link: 'me@gmail.test', created_at: '2026-01-01' }])
    renderWithProviders(<AccountPage initialTab="access" />, { session: sessionWith({ has_password: false }) })

    expect(await screen.findByText(/set a password above before disconnecting/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /disconnect google/i })).not.toBeInTheDocument()
  })

  it('requires the password to disconnect Google', async () => {
    const user = userEvent.setup()
    mockIdentities([{ provider: 'google', email_at_link: 'me@gmail.test', created_at: '2026-01-01' }])
    const del = vi.spyOn(http, 'delete').mockResolvedValue({ data: {} } as never)
    renderWithProviders(<AccountPage initialTab="access" />, { session: sessionWith({ has_password: true }) })

    const button = await screen.findByRole('button', { name: /disconnect google/i })
    expect(button).toBeDisabled()

    await user.type(googleRow().getByLabelText(/current password/i), 'hunter2hunter2')
    await user.click(button)

    await waitFor(() =>
      expect(del).toHaveBeenCalledWith('/api/auth/oauth/google', {
        data: { password: 'hunter2hunter2' },
      }),
    )
  })

  it('explains the 409 when the server says a password is needed first', async () => {
    const user = userEvent.setup()
    mockIdentities([{ provider: 'google', email_at_link: 'me@gmail.test', created_at: '2026-01-01' }])
    vi.spyOn(http, 'delete').mockRejectedValue({
      response: { status: 409, data: { error: { code: 'password_required' } } },
    })
    renderWithProviders(<AccountPage initialTab="access" />, { session: sessionWith({ has_password: true }) })

    await screen.findByRole('button', { name: /disconnect google/i })
    await user.type(googleRow().getByLabelText(/current password/i), 'hunter2hunter2')
    await user.click(googleRow().getByRole('button', { name: /disconnect google/i }))

    expect(await googleRow().findByRole('alert')).toHaveTextContent(/set a password before/i)
  })
})
