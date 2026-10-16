import { useState } from 'react'
import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TwoFactorScreen } from './TwoFactorScreen'
import { renderWithProviders } from '../../test/renderWithProviders'
import { http } from '../../api/client'
import { useAuth } from '../../auth/AuthProvider'
import type { TwoFactorPending } from '../../auth/types'

const pending: TwoFactorPending = {
  purpose: 'totp',
  email: 'ad•••••@foldex.test',
  methods: ['totp', 'recovery_code'],
  maxAttempts: 5,
}

function rejectWith(status: number, code: string, extra: Record<string, unknown> = {}) {
  return Promise.reject({ response: { status, data: { error: { code }, ...extra } } })
}

afterEach(() => vi.restoreAllMocks())

/**
 * Renders the screen next to a probe that reports the live session status.
 *
 * Asserting only on `http.post` arguments would be asserting on the mock:
 * deleting `adopt(res)` from the component leaves every request-shape test
 * green while the user is never actually signed in. The probe makes the EFFECT
 * observable.
 */
function SessionProbe() {
  const { session } = useAuth()
  // A span, not an <output>: <output> carries an implicit ARIA role of
  // "status", which would collide with the screens' own role="status" notices
  // and make getByRole('status') ambiguous.
  return <span data-testid="session-status">{session.status}</span>
}

function renderScreen(p: Partial<TwoFactorPending> = {}) {
  return renderWithProviders(
    <>
      <TwoFactorScreen pending={{ ...pending, ...p }} />
      <SessionProbe />
    </>,
    { session: null },
  )
}

describe('TwoFactorScreen', () => {
  // The masked address is the server's; the screen must render it as given and
  // never try to be more helpful, since the full address is deliberately
  // withheld from a caller who has only proven a password.
  it('shows the masked address without unmasking it', () => {
    renderScreen()
    expect(screen.getByText(/ad•••••@foldex\.test/)).toBeInTheDocument()
  })

  it('submits automatically once six digits are entered, and signs the user in', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      data: { status: 'authenticated', user: { id: 1 }, features: {} },
    } as never)

    renderScreen()
    // The provider probes /me on mount, so it starts at `loading` and settles
    // on `anonymous` — wait for that before asserting the transition away.
    await waitFor(() =>
      expect(screen.getByTestId('session-status')).toHaveTextContent('anonymous'),
    )

    await user.paste('123456')

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))
    expect(post).toHaveBeenCalledWith('/api/auth/2fa/verify', { code: '123456' })
    // The point of the whole screen: a verified code produces a session.
    await waitFor(() =>
      expect(screen.getByTestId('session-status')).toHaveTextContent('authenticated'),
    )
  })

  // A single-use code submitted twice always fails the second time, painting an
  // error over a login that actually succeeded.
  it('does not submit the same code twice', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      data: { status: 'authenticated', user: {}, features: {} },
    } as never)

    renderScreen()
    await user.paste('123456')
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1))

    // Pressing the button after the auto-submit must not fire a second request.
    await user.click(screen.getByRole('button', { name: /verify/i }))
    expect(post).toHaveBeenCalledTimes(1)
  })

  it('reports the remaining attempts on a wrong code', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockImplementation(() =>
      rejectWith(401, 'invalid_code', { attempts_remaining: 3 }) as never,
    )

    renderScreen()
    await user.paste('000000')

    expect(await screen.findByRole('alert')).toHaveTextContent(/3 attempts left/i)
    // The field is cleared so the next attempt starts empty rather than making
    // the user delete six digits by hand.
    const cells = screen.getAllByRole('textbox') as HTMLInputElement[]
    cells.forEach((c) => expect(c).toHaveValue(''))
  })

  it('explains an expired challenge instead of showing a generic error', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockImplementation(() => rejectWith(401, 'challenge_invalid') as never)

    renderScreen()
    await user.paste('123456')

    expect(await screen.findByRole('alert')).toHaveTextContent(/expired/i)
  })

  it('switches to the recovery-code field and submits it verbatim', async () => {
    const user = userEvent.setup()
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      data: { status: 'authenticated', user: { id: 1 }, features: {} },
    } as never)

    renderScreen()
    await user.click(screen.getByRole('button', { name: /use a recovery code/i }))

    const field = screen.getByLabelText(/recovery code/i)
    await user.type(field, '1A2B3-4C5D6')
    await user.click(screen.getByRole('button', { name: /verify/i }))

    // Sent as typed: normalisation is the server's job, and doing it here too
    // would mean two places that must agree on the same rules forever.
    await waitFor(() =>
      expect(post).toHaveBeenCalledWith('/api/auth/2fa/verify', { code: '1A2B3-4C5D6' }),
    )
    await waitFor(() =>
      expect(screen.getByTestId('session-status')).toHaveTextContent('authenticated'),
    )
  })

  it('shows the non-enumerating confirmation after the e-mail request succeeds', async () => {
    vi.spyOn(http, 'post').mockResolvedValue({ status: 202 } as never)
    renderScreen({ methods: ['totp', 'recovery_code', 'email_otp'] })
    // Auto-send fires on mount; the notice arrives without a click.
    expect(await screen.findByRole('status')).toHaveTextContent(/ad•••••@foldex\.test/)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('reports a transport failure without claiming that the e-mail was sent', async () => {
    vi.spyOn(http, 'post').mockImplementation(() => Promise.reject({ request: {} }) as never)
    renderScreen({ methods: ['totp', 'recovery_code', 'email_otp'] })
    expect(await screen.findByRole('alert')).toHaveTextContent(/check your connection and try again/i)
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('auto-sends once on mount and the manual button still works after settle', async () => {
    let resolve!: () => void
    const post = vi.spyOn(http, 'post').mockImplementation(() =>
      new Promise((done) => { resolve = () => done({ status: 202 }) }) as never)
    renderScreen({ methods: ['totp', 'recovery_code', 'email_otp'] })

    // The auto-send is in flight; the button reflects that.
    const button = screen.getByRole('button', { name: /e-mail me a code/i })
    expect(button).toBeDisabled()
    resolve()
    expect(await screen.findByRole('status')).toHaveTextContent(/if a code could be sent/i)
    expect(button).toBeEnabled()

    // A manual click after settle sends a second code.
    const user = userEvent.setup()
    await user.click(button)
    expect(post).toHaveBeenCalledTimes(2)
  })

  // The e-mail option only exists when the server said it does — an instance
  // with no mail driver must not offer a fallback that cannot arrive.
  it('hides the e-mail option when the server did not offer it', () => {
    renderScreen({ methods: ['totp', 'recovery_code'] })
    expect(screen.queryByRole('button', { name: /e-mail me a code/i })).not.toBeInTheDocument()
  })

  // With auto-submit on the sixth digit, "complete but not yet sent" is not an
  // observable state — so what is worth asserting is only that an INCOMPLETE
  // code cannot be submitted by hand.
  it('keeps the submit button disabled while the code is incomplete', async () => {
    const user = userEvent.setup()
    renderScreen()
    const submit = screen.getByRole('button', { name: /verify/i })
    expect(submit).toBeDisabled()

    await user.paste('12345')
    expect(submit).toBeDisabled()
  })
})

describe('the entry window (60s countdown)', () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  function renderWithProbe(p: Partial<TwoFactorPending> = {}) {
    return renderScreen(p)
  }

  // The window is the product contract: sixty ticks from mount, and the
  // number the user reads must be the one being counted.
  it('counts down from 60', async () => {
    renderWithProbe()
    expect(screen.getByText(/expires in 60s/i)).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(2000) })
    expect(screen.getByText(/expires in 58s/i)).toBeInTheDocument()
  })

  // A verification in flight owns the screen: the window closing under it
  // must not swap in the expired panel mid-submit.
  it('does not expire while a verification is in flight', async () => {
    let resolveVerify!: (v: unknown) => void
    vi.spyOn(http, 'post').mockImplementation(
      () => new Promise((r) => { resolveVerify = r }) as never,
    )
    renderWithProbe({ methods: ['totp', 'recovery_code'] })
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    await user.paste('123456')

    act(() => { vi.advanceTimersByTime(60_000) })
    expect(screen.queryByText(/window has closed|código acabou/i)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /verify/i })).toBeInTheDocument()

    await act(async () => {
      resolveVerify({ data: { status: 'authenticated', user: { id: 1 }, features: {} } })
    })
    await waitFor(() =>
      expect(screen.getByTestId('session-status')).toHaveTextContent('authenticated'),
    )
  })

  // A verify response landing after the screen was left must not adopt the
  // session: the user already walked away from this challenge.
  it('does not sign in when verification resolves after unmount', async () => {
    let resolveVerify!: (v: unknown) => void
    vi.spyOn(http, 'post').mockImplementation(
      () => new Promise((r) => { resolveVerify = r }) as never,
    )
    function Harness() {
      const [show, setShow] = useState(true)
      return (
        <>
          {show && <TwoFactorScreen pending={pending} />}
          <SessionProbe />
          <button type="button" onClick={() => setShow(false)}>leave-screen</button>
        </>
      )
    }
    renderWithProviders(<Harness />, { session: null })
    await waitFor(() =>
      expect(screen.getByTestId('session-status')).toHaveTextContent('anonymous'),
    )
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    await user.paste('123456')
    await user.click(screen.getByRole('button', { name: 'leave-screen' }))

    await act(async () => {
      resolveVerify({ data: { status: 'authenticated', user: { id: 1 }, features: {} } })
    })
    // Give adopt() every chance to land; the guard must have vetoed it.
    await act(async () => { await vi.advanceTimersByTimeAsync(50) })
    expect(screen.getByTestId('session-status')).toHaveTextContent('anonymous')
  })

  // App mode (no email asked): expiry says so and returns to the login form —
  // the signOut path, so the pre-auth cookie dies with the screen.
  it('expires to login when no email was requested', async () => {
    const post = vi.spyOn(http, 'post')
    renderWithProbe({ methods: ['totp', 'recovery_code'] })
    act(() => { vi.advanceTimersByTime(60_000) })
    expect(await screen.findByText(/window has closed|código acabou/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /back to login/i })).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(3000) })
    await waitFor(() =>
      expect(screen.getByTestId('session-status').textContent).toBe('anonymous'),
    )
    expect(post).not.toHaveBeenCalledWith('/api/auth/2fa/email', expect.anything())
  })

  // Email mode: the first expiry OFFERS the resend instead of bouncing —
  // the code may simply be stuck in a slow relay.
  it('offers one resend after the first email window closes', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({ status: 202 } as never)
    renderWithProbe({ methods: ['totp', 'recovery_code', 'email_otp'] })
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })

    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    expect(screen.getByText(/send another|enviar outro/i)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /send again|enviar novamente/i }))
    expect(post).toHaveBeenCalledWith('/api/auth/2fa/email')
    // The second window runs a fresh 60.
    expect(await screen.findByText(/expires in 60s/i)).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(59_000) })
    expect(screen.getByText(/expires in 1s/i)).toBeInTheDocument()
  })

  // The resend is one-time by spec: the second expiry is final and signs the
  // caller out, so "send again" can never become its own loop.
  it('is final on the second expiry', async () => {
    vi.spyOn(http, 'post').mockResolvedValue({ status: 202 } as never)
    renderWithProbe({ methods: ['totp', 'recovery_code', 'email_otp'] })
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    await act(async () => { await vi.advanceTimersByTimeAsync(60_000) })
    await user.click(await screen.findByRole('button', { name: /send again|enviar novamente/i }))

    act(() => { vi.advanceTimersByTime(60_000) })
    expect(await screen.findByText(/window has closed|código acabou/i)).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(3000) })
    await waitFor(() =>
      expect(screen.getByTestId('session-status').textContent).toBe('anonymous'),
    )
  })
})
