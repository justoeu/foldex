import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TwoFactorScreen } from './TwoFactorScreen'
import { renderWithProviders } from '../../test/renderWithProviders'
import { http } from '../../api/client'
import { useAuth } from '../../auth/AuthProvider'
import type { TwoFactorPending } from '../../auth/types'

const pending: TwoFactorPending = {
  purpose: 'totp',
  email: 'ad•••••@foldex.test',
  methods: ['totp', 'recovery_code', 'email_otp'],
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

  // The e-mail fallback answers 202 whether it sent or throttled. Surfacing a
  // difference would turn the button into a probe for the send counter.
  it('shows the same confirmation whether the e-mail was sent or throttled', async () => {
    const user = userEvent.setup()
    vi.spyOn(http, 'post').mockImplementation(() => rejectWith(429, 'too_many_attempts') as never)

    renderScreen()
    await user.click(screen.getByRole('button', { name: /e-mail me a code/i }))

    expect(await screen.findByRole('status')).toHaveTextContent(/ad•••••@foldex\.test/)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
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
