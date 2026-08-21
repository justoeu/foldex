import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ForgotScreen } from './ForgotScreen'
import { ResetScreen } from './ResetScreen'
import { renderWithProviders } from '../../test/renderWithProviders'
import { useAuth } from '../../auth/AuthProvider'
import { http } from '../../api/client'
import type { MeResponse } from '../../api/auth'
import i18n from '../../i18n'

afterEach(async () => {
  vi.restoreAllMocks()
  if (i18n.language !== 'en') await i18n.changeLanguage('en')
})

// See TwoFactorScreen.test.tsx: without this, deleting `adopt(...)` from the
// component leaves the request-shape assertions green while the user is never
// signed in.
function SessionProbe() {
  const { session } = useAuth()
  // A span, not an <output>: <output> carries an implicit ARIA role of
  // "status", which would collide with the screens' own role="status" notices
  // and make getByRole('status') ambiguous.
  return <span data-testid="session-status">{session.status}</span>
}

function rejectWith(status: number, code: string) {
  return Promise.reject({ response: { status, data: { error: { code } } } })
}

describe('ForgotScreen', () => {
  it('sends the request and shows the confirmation', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)

    renderWithProviders(<ForgotScreen onBack={() => {}} />, { session: null })
    await user.type(screen.getByLabelText(/e-mail/i), 'someone@example.com')
    await user.click(screen.getByRole('button', { name: /send reset link/i }))

    expect(post).toHaveBeenCalledWith('/api/auth/password/forgot',
      { email: 'someone@example.com', locale: 'en' })
    expect(await screen.findByRole('heading', { name: /check your e-mail/i })).toBeInTheDocument()
  })

  // The reason the request carries a locale at all: this screen is anonymous,
  // so the server has no account preference to read and falls back to
  // Accept-Language — a browser setting separate from the one that decided what
  // language this screen is in. A Portuguese login screen mailed an English
  // reset link, and the field is what closes that gap. The server ranks it
  // below any stored preference, so it can only ever pick the wording of a
  // message the caller already caused.
  it('tells the server which language the screen is speaking', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    await i18n.changeLanguage('pt')

    renderWithProviders(<ForgotScreen onBack={() => {}} />, { session: null })
    await user.type(screen.getByLabelText(/e-mail/i), 'alguem@example.com')
    await user.click(screen.getByRole('button', { name: /link/i }))

    expect(post).toHaveBeenCalledWith('/api/auth/password/forgot',
      { email: 'alguem@example.com', locale: 'pt' })
  })

  /**
   * The single most important behaviour on this screen.
   *
   * The backend answers 202 for every input precisely so the endpoint cannot
   * enumerate accounts, and the UI must not undo that by reacting differently
   * to a failure. A transport error, a rejected address, a real send — the user
   * sees one outcome.
   */
  it('shows the identical confirmation when the request fails', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockImplementation(() => rejectWith(500, 'internal') as never)

    renderWithProviders(<ForgotScreen onBack={() => {}} />, { session: null })
    await user.type(screen.getByLabelText(/e-mail/i), 'nobody@example.com')
    await user.click(screen.getByRole('button', { name: /send reset link/i }))

    expect(await screen.findByRole('heading', { name: /check your e-mail/i })).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  // The copy must not claim something the product will not confirm. "We sent
  // you an e-mail" is a statement about whether the account exists.
  it('hedges the confirmation copy', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)

    renderWithProviders(<ForgotScreen onBack={() => {}} />, { session: null })
    await user.type(screen.getByLabelText(/e-mail/i), 'someone@example.com')
    await user.click(screen.getByRole('button', { name: /send reset link/i }))

    expect(await screen.findByText(/if an account exists/i)).toBeInTheDocument()
  })

  it('can go back to the sign-in screen', async () => {
    const user = userEvent.setup()
    const onBack = vi.fn()
    renderWithProviders(<ForgotScreen onBack={onBack} />, { session: null })
    await user.click(screen.getByRole('button', { name: /back to sign in/i }))
    expect(onBack).toHaveBeenCalled()
  })
})

describe('ResetScreen', () => {
  it('submits the token with the new password and signs the user in', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      data: { status: 'authenticated', user: { id: 1 }, features: {} },
    } as never)

    renderWithProviders(
      <>
        <ResetScreen token="TOK" onGiveUp={() => {}} />
        <SessionProbe />
      </>,
      { session: null },
    )
    await user.type(screen.getByLabelText(/^new password$/i), 'a brand new password')
    await user.type(screen.getByLabelText(/confirm new password/i), 'a brand new password')
    await user.click(screen.getByRole('button', { name: /save and sign in/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/password/reset', {
        token: 'TOK',
        password: 'a brand new password',
      }),
    )
    // Resetting signs the user in directly: they proved the mailbox AND chose a
    // password, which is more than the login screen asks for.
    await waitFor(() =>
      expect(screen.getByTestId('session-status')).toHaveTextContent('authenticated'),
    )
  })

  // An account with an authenticator must NOT be signed in by the reset alone —
  // a compromised mailbox would otherwise bypass the second factor entirely.
  it('stops at the second factor when the server asks for one', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockResolvedValue({
      data: {
        status: 'two_factor_required',
        purpose: 'totp',
        email: 'a•••@b.test',
        methods: ['totp', 'recovery_code'],
        expires_in: 300,
        max_attempts: 5,
        features: { google_oauth: false, two_factor: true, email_delivery: false },
      } satisfies MeResponse,
    } as never)

    renderWithProviders(
      <>
        <ResetScreen token="TOK" onGiveUp={() => {}} />
        <SessionProbe />
      </>,
      { session: null },
    )
    await user.type(screen.getByLabelText(/^new password$/i), 'a brand new password')
    await user.type(screen.getByLabelText(/confirm new password/i), 'a brand new password')
    await user.click(screen.getByRole('button', { name: /save and sign in/i }))

    await waitFor(() =>
      expect(screen.getByTestId('session-status')).toHaveTextContent('two_factor_required'),
    )
  })

  // Catching the mismatch client-side is not security — the server does not
  // care — it is about not spending a single-use token on a typo.
  it('refuses to submit when the confirmation does not match', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)

    renderWithProviders(<ResetScreen token="TOK" onGiveUp={() => {}} />, { session: null })
    await user.type(screen.getByLabelText(/^new password$/i), 'a brand new password')
    await user.type(screen.getByLabelText(/confirm new password/i), 'a different password')

    const submit = screen.getByRole('button', { name: /save and sign in/i })
    expect(submit).toBeDisabled()
    expect(screen.getByLabelText(/confirm new password/i)).toHaveAttribute('aria-invalid', 'true')
    expect(post).not.toHaveBeenCalled()
  })

  it('explains a dead link rather than showing a generic error', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockImplementation(() => rejectWith(404, 'reset_invalid') as never)

    renderWithProviders(<ResetScreen token="DEAD" onGiveUp={() => {}} />, { session: null })
    await user.type(screen.getByLabelText(/^new password$/i), 'a brand new password')
    await user.type(screen.getByLabelText(/confirm new password/i), 'a brand new password')
    await user.click(screen.getByRole('button', { name: /save and sign in/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/no longer valid/i)
  })

  it('surfaces the password policy from the server', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockImplementation(() => rejectWith(400, 'password_too_short') as never)

    renderWithProviders(<ResetScreen token="TOK" onGiveUp={() => {}} />, { session: null })
    await user.type(screen.getByLabelText(/^new password$/i), 'short')
    await user.type(screen.getByLabelText(/confirm new password/i), 'short')
    await user.click(screen.getByRole('button', { name: /save and sign in/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/at least/i)
  })
})
