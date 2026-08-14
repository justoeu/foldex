import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AccountSection } from './AccountSection'
import { renderWithProviders, testAdminUser } from '../test/renderWithProviders'
import { http } from '../api/client'
import * as auth from '../api/auth'
import type { AuthUser, SessionState } from '../auth/types'

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
  return renderWithProviders(<AccountSection />, { session })
}

describe('AccountSection', () => {
  it('says a password is set and does not offer to set another', () => {
    render(sessionWith({ has_password: true }))
    expect(screen.getByText(/a password is set/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /set a password/i })).not.toBeInTheDocument()
  })

  it('offers to set one when the account is Google-only', () => {
    render(sessionWith({ has_password: false }))
    expect(screen.getByText(/no password/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /set a password/i })).toBeInTheDocument()
  })

  // The credential being created OUTLIVES the session presenting the request,
  // and there is no current password to prove, so the authenticator is the only
  // step-up available.
  it('demands the authenticator code when the account has one', async () => {
    const user = userEvent.setup()
    render(sessionWith({ has_password: false, totp_enabled: true }))

    const submit = screen.getByRole('button', { name: /set a password/i })
    await user.type(screen.getByLabelText(/new password/i), 'a good new password')
    await user.type(screen.getByLabelText(/confirm the password/i), 'a good new password')
    expect(submit).toBeDisabled()

    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells[0].focus()
    await user.paste('123456')
    await waitFor(() => expect(submit).toBeEnabled())
  })

  it('refuses to submit two passwords that disagree', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post')
    render(sessionWith({ has_password: false }))

    await user.type(screen.getByLabelText(/new password/i), 'a good new password')
    await user.type(screen.getByLabelText(/confirm the password/i), 'a different one')
    await user.click(screen.getByRole('button', { name: /set a password/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/do not match/i)
    expect(post).not.toHaveBeenCalled()
  })

  it('sends the new password', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    render(sessionWith({ has_password: false }))

    await user.type(screen.getByLabelText(/new password/i), 'a good new password')
    await user.type(screen.getByLabelText(/confirm the password/i), 'a good new password')
    await user.click(screen.getByRole('button', { name: /set a password/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/password/set', {
        password: 'a good new password',
        code: '',
      }),
    )
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
    expect(screen.getByLabelText(/current password/i)).toBeInTheDocument()
    expect(screen.queryByLabelText(/code from your authenticator/i)).not.toBeInTheDocument()
  })

  it('posts the password proof before using the API redirect URL', async () => {
    const user = userEvent.setup()
    const navigate = vi.spyOn(auth, 'navigateToOAuth').mockImplementation(() => {})
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      data: { redirect_url: 'https://accounts.google.test/oauth?state=safe-state' },
    } as never)
    render(sessionWith({ has_password: true }))

    await user.click(await screen.findByRole('button', { name: /connect google/i }))
    await user.type(screen.getByLabelText(/current password/i), 'hunter2hunter2')
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
    expect(screen.getByText(/code from your authenticator/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /use a recovery code/i })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /use a recovery code/i }))
    expect(screen.getByLabelText(/recovery code/i)).toBeInTheDocument()
  })

  it('shows the linked address once Google is connected', async () => {
    mockIdentities([{ provider: 'google', email_at_link: 'me@gmail.test', created_at: '2026-01-01' }])
    renderWithProviders(<AccountSection />, { session: sessionWith({ has_password: true }) })

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
    renderWithProviders(<AccountSection />, { session: sessionWith({ has_password: false }) })

    expect(await screen.findByText(/set a password above before disconnecting/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /disconnect google/i })).not.toBeInTheDocument()
  })

  it('requires the password to disconnect Google', async () => {
    const user = userEvent.setup()
    mockIdentities([{ provider: 'google', email_at_link: 'me@gmail.test', created_at: '2026-01-01' }])
    const del = vi.spyOn(http, 'delete').mockResolvedValue({ data: {} } as never)
    renderWithProviders(<AccountSection />, { session: sessionWith({ has_password: true }) })

    const button = await screen.findByRole('button', { name: /disconnect google/i })
    expect(button).toBeDisabled()

    await user.type(screen.getByLabelText(/current password/i), 'hunter2hunter2')
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
    renderWithProviders(<AccountSection />, { session: sessionWith({ has_password: true }) })

    await user.type(await screen.findByLabelText(/current password/i), 'hunter2hunter2')
    await user.click(screen.getByRole('button', { name: /disconnect google/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/set a password before/i)
  })
})
