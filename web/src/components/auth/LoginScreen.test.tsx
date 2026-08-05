import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { LoginScreen } from './LoginScreen'
import { SetupScreen } from './SetupScreen'
import { renderWithProviders } from '../../test/renderWithProviders'
import { http } from '../../api/client'

const features = { google_oauth: false, two_factor: false, email_delivery: false }

function rejectWith(code: string, status = 401) {
  return vi.spyOn(http, 'post').mockRejectedValue({
    response: { status, data: { error: { code, message: 'whatever' } } },
  })
}

afterEach(() => vi.restoreAllMocks())

describe('LoginScreen', () => {
  it('submits the credentials and adopts the session', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      data: { status: 'authenticated', user: { email: 'a@b.c' }, csrf_token: 't', features },
    } as never)
    renderWithProviders(<LoginScreen />, { session: null })
    const user = userEvent.setup()

    await user.type(screen.getByLabelText(/e-mail/i), 'a@b.c')
    await user.type(screen.getByLabelText(/^password$/i), 'a good password')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/login', {
        email: 'a@b.c',
        password: 'a good password',
      }),
    )
  })

  /**
   * The UI half of the anti-enumeration contract.
   *
   * The backend collapses unknown-address, wrong-password and disabled-account
   * into one `invalid_credentials` code, and spends a dummy bcrypt hash plus a
   * 250 ms duration floor to make them indistinguishable. A friendlier message
   * here ("no account with that e-mail") would hand the oracle straight back.
   */
  it('never reveals whether the account exists', async () => {
    rejectWith('invalid_credentials')
    renderWithProviders(<LoginScreen />, { session: null })
    const user = userEvent.setup()

    await user.type(screen.getByLabelText(/e-mail/i), 'ghost@example.com')
    await user.type(screen.getByLabelText(/^password$/i), 'wrong')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/invalid e-mail or password/i)
    expect(alert.textContent).not.toMatch(/exist|found|unknown|disabled|locked/i)
  })

  it('surfaces a rate-limit distinctly from a bad password', async () => {
    rejectWith('too_many_attempts', 429)
    renderWithProviders(<LoginScreen />, { session: null })
    const user = userEvent.setup()

    await user.type(screen.getByLabelText(/e-mail/i), 'a@b.c')
    await user.type(screen.getByLabelText(/^password$/i), 'x')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    // Distinct is correct here: the lockout is keyed on an address the caller
    // already typed, so it reveals nothing they did not supply, and "wrong
    // password" would send them into a pointless retry loop.
    expect(await screen.findByRole('alert')).toHaveTextContent(/too many attempts/i)
  })

  it('reports an unreachable backend as a network problem', async () => {
    vi.spyOn(http, 'post').mockRejectedValue({})
    renderWithProviders(<LoginScreen />, { session: null })
    const user = userEvent.setup()

    await user.type(screen.getByLabelText(/e-mail/i), 'a@b.c')
    await user.type(screen.getByLabelText(/^password$/i), 'x')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/could not reach the server/i)
  })

  it('disables the submit button while in flight', async () => {
    let release: (v: unknown) => void = () => {}
    vi.spyOn(http, 'post').mockImplementation(
      () => new Promise((r) => { release = r }) as never,
    )
    renderWithProviders(<LoginScreen />, { session: null })
    const user = userEvent.setup()

    await user.type(screen.getByLabelText(/e-mail/i), 'a@b.c')
    await user.type(screen.getByLabelText(/^password$/i), 'pw')
    const button = screen.getByRole('button', { name: /sign in/i })
    await user.click(button)

    // A double-submit burns two attempts from the 5-per-e-mail budget for one
    // impatient click.
    await waitFor(() => expect(button).toBeDisabled())
    release({ data: { status: 'anonymous', features } })
  })

  it('uses the right autocomplete hints so password managers work', () => {
    renderWithProviders(<LoginScreen />, { session: null })
    expect(screen.getByLabelText(/e-mail/i)).toHaveAttribute('autocomplete', 'username')
    expect(screen.getByLabelText(/^password$/i)).toHaveAttribute('autocomplete', 'current-password')
  })
})

describe('SetupScreen', () => {
  it('creates the first administrator', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      data: { status: 'authenticated', user: { email: 'a@b.c' }, csrf_token: 't', features },
    } as never)
    renderWithProviders(<SetupScreen />, { session: null })
    const user = userEvent.setup()

    await user.type(screen.getByLabelText(/name/i), 'Ana')
    await user.type(screen.getByLabelText(/e-mail/i), 'a@b.c')
    await user.type(screen.getByLabelText(/^password$/i), 'a good password')
    await user.type(screen.getByLabelText(/confirm password/i), 'a good password')
    await user.click(screen.getByRole('button', { name: /create account/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/bootstrap', {
        email: 'a@b.c',
        name: 'Ana',
        password: 'a good password',
      }),
    )
  })

  it('refuses to submit when the two passwords differ', async () => {
    const post = vi.spyOn(http, 'post')
    renderWithProviders(<SetupScreen />, { session: null })
    const user = userEvent.setup()

    await user.type(screen.getByLabelText(/e-mail/i), 'a@b.c')
    await user.type(screen.getByLabelText(/^password$/i), 'a good password')
    await user.type(screen.getByLabelText(/confirm password/i), 'a different one')
    await user.click(screen.getByRole('button', { name: /create account/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/do not match/i)
    expect(post).not.toHaveBeenCalled()
  })

  it('tells the user when someone else finished setup first', async () => {
    rejectWith('already_configured', 409)
    renderWithProviders(<SetupScreen />, { session: null })
    const user = userEvent.setup()

    await user.type(screen.getByLabelText(/e-mail/i), 'a@b.c')
    await user.type(screen.getByLabelText(/^password$/i), 'a good password')
    await user.type(screen.getByLabelText(/confirm password/i), 'a good password')
    await user.click(screen.getByRole('button', { name: /create account/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/already has an account/i)
  })

  it('relays the backend password policy verbatim', async () => {
    rejectWith('password_too_short', 400)
    renderWithProviders(<SetupScreen />, { session: null })
    const user = userEvent.setup()

    await user.type(screen.getByLabelText(/e-mail/i), 'a@b.c')
    await user.type(screen.getByLabelText(/^password$/i), 'shortpw')
    await user.type(screen.getByLabelText(/confirm password/i), 'shortpw')
    await user.click(screen.getByRole('button', { name: /create account/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/at least 8 characters/i)
  })
})
