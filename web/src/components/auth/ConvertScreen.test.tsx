import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ConvertScreen } from './ConvertScreen'
import { renderWithProviders } from '../../test/renderWithProviders'
import { http } from '../../api/client'
import { defaultFeatures } from '../../auth/types'
import { useAuth } from '../../auth/AuthProvider'
import type { MeResponse } from '../../api/auth'
import { AuthGate } from '../../auth/AuthGate'

/**
 * The account-portability screen: someone came back from Google with an address
 * that already belongs to a PASSWORD account.
 *
 * What it must never become is a login. A matching e-mail address is not a
 * secret and anyone can put one in a Google profile, so the only thing standing
 * between "that address exists here" and "you are now signed in as them" is the
 * password field on this screen.
 */

afterEach(() => vi.restoreAllMocks())

/**
 * See TwoFactorScreen.test.tsx: without this, deleting `adopt(...)` from the
 * component leaves the request-shape assertions green while the user is never
 * signed in — or, worse, never diverted to the second factor (INV-016).
 */
function SessionProbe() {
  const { session } = useAuth()
  return <span data-testid="session-status">{session.status}</span>
}

const convertSession = {
  status: 'convert_password_account' as const,
  email: 'a••@b.test',
  features: defaultFeatures,
}

const twoFactorChallenge: MeResponse = {
  status: 'two_factor_required',
  purpose: 'totp',
  email: 'a••@b.test',
  methods: ['totp', 'recovery_code'],
  expires_in: 300,
  max_attempts: 5,
  features: { google_oauth: false, two_factor: true, email_delivery: false },
}

function render() {
  return renderWithProviders(
    <>
      <ConvertScreen email="a••@b.test" />
      <SessionProbe />
    </>,
    { session: convertSession },
  )
}

describe('ConvertScreen', () => {
  it('shows the masked address it is about', () => {
    render()
    expect(screen.getByText(/a••@b\.test/)).toBeInTheDocument()
  })

  // The password is not coming back, and this screen is the only place the user
  // is told so before it happens.
  it('warns that the password will be removed', () => {
    render()
    expect(screen.getByRole('note')).toHaveTextContent(/removes your password/i)
  })

  it('cannot be submitted without a password', async () => {
    const user = userEvent.setup()
    render()

    const submit = screen.getByRole('button', { name: /link google/i })
    expect(submit).toBeDisabled()

    await user.type(screen.getByLabelText(/current password/i), 'hunter2hunter2')
    expect(submit).toBeEnabled()
  })

  it('sends the password and adopts the resulting session', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      data: { status: 'authenticated', user: {}, csrf_token: 't', features: defaultFeatures },
    } as never)
    render()

    await user.type(screen.getByLabelText(/current password/i), 'hunter2hunter2')
    await user.click(screen.getByRole('button', { name: /link google/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/oauth/google/convert', {
        password: 'hunter2hunter2',
      }),
    )
    await waitFor(() =>
      expect(screen.getByTestId('session-status')).toHaveTextContent('authenticated'),
    )
  })

  // Conversion proves the password, which is one factor. INV-016: OAuth never
  // skips the second. `adopt` is what routes the challenge; this screen must
  // not swallow it as a finished sign-in.
  it('stops at the second factor when the server asks for one', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockResolvedValue({ data: twoFactorChallenge } as never)
    render()

    await user.type(screen.getByLabelText(/current password/i), 'hunter2hunter2')
    await user.click(screen.getByRole('button', { name: /link google/i }))

    await waitFor(() =>
      expect(screen.getByTestId('session-status')).toHaveTextContent('two_factor_required'),
    )
  })

  // The probe above proves `adopt` ran. This one proves the GATE actually
  // swaps the convert form for the code screen — the UI half of INV-016.
  it('shows the second-factor screen instead of the app when conversion still owes a code', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockResolvedValue({ data: twoFactorChallenge } as never)
    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: convertSession },
    )

    await user.type(await screen.findByLabelText(/current password/i), 'hunter2hunter2')
    await user.click(screen.getByRole('button', { name: /link google/i }))

    expect(await screen.findByRole('heading', { name: /enter your code/i })).toBeInTheDocument()
    expect(screen.queryByText('the app')).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: /confirm your password/i })).not.toBeInTheDocument()
  })

  it('reports a wrong password and clears the field', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 401, data: { error: { code: 'invalid_credentials' } } },
    })
    render()

    await user.type(screen.getByLabelText(/current password/i), 'wrong-password')
    await user.click(screen.getByRole('button', { name: /link google/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/not correct/i)
    expect(screen.getByLabelText(/current password/i)).toHaveValue('')
  })

  /**
   * An exhausted challenge cannot be retried here: the server has already
   * cleared the pre-auth cookie, so every further submit would fail the same
   * way. Re-probing /me is what moves the user back to a screen that can
   * actually start the flow again — without it they would sit on a form that
   * can only ever produce errors.
   */
  it('re-probes the session when the challenge is spent', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 429, data: { error: { code: 'too_many_attempts' } } },
    })
    const get = vi.spyOn(http, 'get').mockResolvedValue({
      data: { status: 'anonymous', features: {} },
    } as never)
    render()

    await user.type(screen.getByLabelText(/current password/i), 'wrong-password')
    await user.click(screen.getByRole('button', { name: /link google/i }))

    await waitFor(() => expect(get).toHaveBeenCalledWith('/api/auth/me'))
  })

  /**
   * Backing out has to CLEAR the pre-auth cookie, not just re-probe.
   *
   * The cookie is still live at this point, so /me would answer with this very
   * screen again and the button would silently do nothing. Logout is what
   * abandons the challenge — the same way the second-factor and enrollment
   * screens back out.
   */
  it('abandons the challenge rather than re-probing it', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    render()

    await user.click(screen.getByRole('button', { name: /never mind/i }))
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/auth/logout'))
  })

  // The alternative — a "skip" that signs them in anyway — is exactly the
  // takeover this screen exists to prevent.
  it('offers no way to reach the app without the password', () => {
    render()
    expect(screen.queryByRole('button', { name: /skip|continue anyway|later/i })).not.toBeInTheDocument()
  })
})
