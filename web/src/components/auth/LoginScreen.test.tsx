import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { LoginScreen } from './LoginScreen'
import { SetupScreen } from './SetupScreen'
import { renderWithProviders, testAdminUser } from '../../test/renderWithProviders'
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

    await user.type(screen.getByRole('textbox', { name: /e-mail/i }), 'a@b.c')
    await user.type(screen.getByLabelText(/^password$/i), 'a good password')
    await user.click(screen.getByRole('button', { name: /sign in/i }))

    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/login', {
        identifier: 'a@b.c',
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

    await user.type(screen.getByRole('textbox', { name: /e-mail/i }), 'ghost@example.com')
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

    await user.type(screen.getByRole('textbox', { name: /e-mail/i }), 'a@b.c')
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

    await user.type(screen.getByRole('textbox', { name: /e-mail/i }), 'a@b.c')
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

    await user.type(screen.getByRole('textbox', { name: /e-mail/i }), 'a@b.c')
    await user.type(screen.getByLabelText(/^password$/i), 'pw')
    const button = screen.getByRole('button', { name: /sign in/i })
    await user.click(button)

    // A double-submit burns two attempts from the 5-per-e-mail budget for one
    // impatient click.
    await waitFor(() => expect(button).toBeDisabled())
    release({ data: { status: 'anonymous', features } })
  })

  /*
   * The button is the last thing standing between a half-typed form and a
   * request the server will refuse: it stays disabled until BOTH credentials
   * are present, so an empty submit never becomes a failed login attempt
   * against the rate limiter.
   */
  it('keeps the submit disabled until both credentials are typed', async () => {
    const user = userEvent.setup()
    renderWithProviders(<LoginScreen />, { session: null })

    const submit = screen.getByRole('button', { name: /sign in/i })
    expect(submit).toBeDisabled()

    await user.type(screen.getByLabelText(/e-mail or username/i), 'a@b.c')
    expect(submit).toBeDisabled()

    await user.type(screen.getByLabelText(/^password$/i), 'hunter2')
    expect(submit).toBeEnabled()
  })

  it('treats a whitespace-only identifier as empty, and never trims the password', async () => {
    const user = userEvent.setup()
    renderWithProviders(<LoginScreen />, { session: null })

    const submit = screen.getByRole('button', { name: /sign in/i })
    await user.type(screen.getByLabelText(/e-mail or username/i), '   ')
    // A password that is nothing but spaces is still a password.
    await user.type(screen.getByLabelText(/^password$/i), '   ')
    expect(submit).toBeDisabled()

    await user.type(screen.getByLabelText(/e-mail or username/i), 'a@b.c')
    expect(submit).toBeEnabled()
  })

  /*
   * The recovery link belongs beside the field it acts on. Orphaned under the
   * submit button it read as a second action of equal weight; on the password
   * label row it reads as what it is — the way out when that one field is the
   * problem.
   */
  it('offers the password recovery link on the password label row', async () => {
    const user = userEvent.setup()
    const onForgot = vi.fn()
    renderWithProviders(<LoginScreen onForgotPassword={onForgot} />, { session: null })

    const link = screen.getByRole('button', { name: /forgot your password/i })
    expect(link.closest('.fx-auth-label-row')).not.toBeNull()

    await user.click(link)
    expect(onForgot).toHaveBeenCalledTimes(1)
  })

  it('leaves the recovery link out when the screen has nowhere to send it', () => {
    renderWithProviders(<LoginScreen />, { session: null })
    expect(screen.queryByRole('button', { name: /forgot your password/i })).not.toBeInTheDocument()
  })

  it('uses the right autocomplete hints so password managers work', () => {
    renderWithProviders(<LoginScreen />, { session: null })
    expect(screen.getByRole('textbox', { name: /e-mail/i })).toHaveAttribute('autocomplete', 'username')
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
    await user.type(screen.getByRole('textbox', { name: /e-mail/i }), 'a@b.c')
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

    await user.type(screen.getByRole('textbox', { name: /e-mail/i }), 'a@b.c')
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

    await user.type(screen.getByRole('textbox', { name: /e-mail/i }), 'a@b.c')
    await user.type(screen.getByLabelText(/^password$/i), 'a good password')
    await user.type(screen.getByLabelText(/confirm password/i), 'a good password')
    await user.click(screen.getByRole('button', { name: /create account/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/already has an account/i)
  })

  it('relays the backend password policy verbatim', async () => {
    rejectWith('password_too_short', 400)
    renderWithProviders(<SetupScreen />, { session: null })
    const user = userEvent.setup()

    await user.type(screen.getByRole('textbox', { name: /e-mail/i }), 'a@b.c')
    await user.type(screen.getByLabelText(/^password$/i), 'shortpw')
    await user.type(screen.getByLabelText(/confirm password/i), 'shortpw')
    await user.click(screen.getByRole('button', { name: /create account/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/at least 8 characters/i)
  })
})

// Remembering the address is a convenience; the password must never be part
// of it, and unticking must ERASE rather than merely stop writing.
describe('remember my e-mail', () => {
  // test/storage.ts re-creates the store per FILE, not per test, so a key one
  // case writes is still there for the next — which is exactly what this
  // group asserts about.
  beforeEach(() => localStorage.removeItem('foldex.auth.email'))

  it('pre-fills the address and keeps the box ticked on a later visit', () => {
    localStorage.setItem('foldex.auth.email', 'saved@foldex.test')
    renderWithProviders(<LoginScreen />, { session: { status: 'anonymous', features } as never })

    expect(screen.getByRole('textbox', { name: /e-mail/i })).toHaveValue('saved@foldex.test')
    expect(screen.getByRole('checkbox', { name: /remember|lembrar|recordar/i })).toBeChecked()
  })

  it('stores the address only after the credentials are accepted', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 401, data: { error: { code: 'invalid_credentials', message: 'no' } } },
    })
    renderWithProviders(<LoginScreen />, { session: { status: 'anonymous', features } as never })

    await user.type(screen.getByRole('textbox', { name: /e-mail/i }), 'typo@foldex.test')
    await user.type(screen.getByLabelText(/^password$/i), 'wrong')
    // Ticked on purpose: with the box unticked the address is absent whether
    // the write happens before or after the await, so the test could not fail
    // for the reason it names.
    await user.click(screen.getByRole('checkbox', { name: /remember|lembrar|recordar/i }))
    await user.click(screen.getByRole('button', { name: /sign in|entrar/i }))

    await waitFor(() => expect(post).toHaveBeenCalled())
    expect(localStorage.getItem('foldex.auth.email')).toBeNull()
  })

  it('erases the stored address the moment the box is unticked', async () => {
    const user = userEvent.setup()
    localStorage.setItem('foldex.auth.email', 'saved@foldex.test')
    renderWithProviders(<LoginScreen />, { session: { status: 'anonymous', features } as never })

    await user.click(screen.getByRole('checkbox', { name: /remember|lembrar|recordar/i }))

    // Erased at the untick, with no sign-in at all: someone who unticks and
    // walks away must not leave the address exactly where they just asked for
    // it not to be.
    expect(localStorage.getItem('foldex.auth.email')).toBeNull()
  })

  it('never stores the password', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockResolvedValue({
      data: { status: 'authenticated', user: testAdminUser, csrf_token: 'c', features },
    } as never)
    renderWithProviders(<LoginScreen />, { session: { status: 'anonymous', features } as never })

    await user.type(screen.getByRole('textbox', { name: /e-mail/i }), 'a@b.c')
    await user.type(screen.getByLabelText(/^password$/i), 'hunter2')
    await user.click(screen.getByRole('checkbox', { name: /remember|lembrar|recordar/i }))
    await user.click(screen.getByRole('button', { name: /sign in|entrar/i }))

    await waitFor(() => expect(localStorage.getItem('foldex.auth.email')).toBe('a@b.c'))

    const dump = Array.from({ length: localStorage.length }, (_, i) => {
      const k = localStorage.key(i) as string
      return `${k}=${localStorage.getItem(k)}`
    }).join('\n')
    // `JSON.stringify(localStorage)` was tried and is VACUOUS against
    // test/storage.ts — the entries live in a closure, so it returns
    // `{"length":N}` and passes even when the password was written.
    expect(dump).toContain('foldex.auth.email=a@b.c') // proves the dump is real
    expect(dump).not.toContain('hunter2')
  })
})
