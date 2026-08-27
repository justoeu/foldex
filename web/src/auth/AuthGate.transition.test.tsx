import { describe, it, expect, vi, afterEach } from 'vitest'
import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AuthGate } from './AuthGate'
import { renderWithProviders } from '../test/renderWithProviders'
import { http } from '../api/client'

/*
 * The sign-in form and the code screen are two painted states of ONE flow, and
 * the code screen is the only lazy screen the gate reaches by a TRANSITION from
 * a screen already on the glass. Every other lazy screen is entered at mount,
 * from a URL token, where the boot overlay is the honest fallback because
 * nothing has been painted yet.
 *
 * The stand-in suspends until the test releases it, which is what the boundary
 * sees while a chunk is in flight — modelled here in React rather than in the
 * module registry, so the assertion is about the boundary and not about how
 * Vitest happens to cache a pending dynamic import.
 */
const chunk = vi.hoisted(() => {
  let release!: () => void
  const arrival = new Promise<void>((resolve) => {
    release = resolve
  })
  const state = { requested: false, arrived: false }
  return {
    arrival,
    state,
    land() {
      state.arrived = true
      release()
    },
  }
})

// The enrollment screen shares the boundary and the transition; it is warmed by
// the same effect, and on an instance with AUTH_REQUIRE_2FA_FOR_ADMINS on it is
// the screen an administrator's FIRST sign-in lands on.
const enrollChunk = vi.hoisted(() => ({ requested: false }))

vi.mock('../components/auth/EnrollTotpScreen', () => {
  enrollChunk.requested = true
  return { EnrollTotpScreen: () => <h1>Set up two-step verification</h1> }
})

vi.mock('../components/auth/TwoFactorScreen', () => {
  chunk.state.requested = true
  return {
    TwoFactorScreen: () => {
      if (!chunk.state.arrived) throw chunk.arrival
      return <h1>Enter your code</h1>
    },
  }
})

const features = { google_oauth: false, two_factor: true, email_delivery: false }

const challenge = {
  status: 'two_factor_required',
  purpose: 'totp',
  email: 'ad•••••@foldex.test',
  methods: ['totp', 'recovery_code'],
  max_attempts: 5,
  features,
}

afterEach(() => vi.restoreAllMocks())

function signInScreen() {
  vi.spyOn(http, 'get').mockImplementation(async (url: string) =>
    url === '/api/auth/me'
      ? ({ data: { status: 'anonymous', features } } as never)
      : ({ data: {} } as never),
  )
  vi.spyOn(http, 'post').mockResolvedValue({ data: challenge } as never)
  return renderWithProviders(
    <AuthGate>
      <div>the app</div>
    </AuthGate>,
    { session: null },
  )
}

async function signIn(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByLabelText(/e-mail or username/i), 'admin@foldex.test')
  await user.type(screen.getByLabelText(/^password$/i), 'a good password')
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
  // The login POST has resolved and the session has flipped; only the screen
  // itself is outstanding.
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

describe('the sign-in to second-factor transition', () => {
  // Warmed while the user is still typing, so the swap has nothing left to
  // fetch. Without it the request starts on the very render that needs it.
  it('asks for the code screen while the sign-in form is still on screen', async () => {
    signInScreen()
    expect(await screen.findByRole('heading', { name: /welcome back/i })).toBeInTheDocument()
    await waitFor(() => expect(chunk.state.requested).toBe(true))
    expect(enrollChunk.requested).toBe(true)
  })

  it('keeps the sign-in screen on the glass while the code screen loads', async () => {
    const user = userEvent.setup()
    signInScreen()
    await signIn(user)

    expect(screen.queryByText('Loading…')).not.toBeInTheDocument()
    expect(screen.getByRole('heading', { name: /welcome back/i })).toBeInTheDocument()
  })

  it('swaps in the code screen once it arrives', async () => {
    const user = userEvent.setup()
    signInScreen()
    await signIn(user)

    act(() => chunk.land())
    expect(await screen.findByRole('heading', { name: /enter your code/i })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: /welcome back/i })).not.toBeInTheDocument()
  })
})
