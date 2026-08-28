import { describe, it, expect, vi, afterEach } from 'vitest'
import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AuthGate } from './AuthGate'
import { makeQueryClient, renderWithProviders } from '../test/renderWithProviders'
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

/*
 * The recovery screen is the THIRD one reached this way, and it is reached by a
 * plain click on the sign-in form — no token, no round-trip. It suspends the
 * same boundary, so it gets the same stand-in.
 */
const forgotChunk = vi.hoisted(() => {
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

vi.mock('../components/auth/ForgotScreen', () => {
  forgotChunk.state.requested = true
  return {
    ForgotScreen: () => {
      if (!forgotChunk.state.arrived) throw forgotChunk.arrival
      return <h1>Recover your account</h1>
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

function signInScreen(client?: ReturnType<typeof makeQueryClient>) {
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
    { session: null, ...(client ? { client } : {}) },
  )
}

async function signIn(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByLabelText(/e-mail or username/i), 'admin@foldex.test')
  await user.type(screen.getByLabelText(/^password$/i), 'a good password')
  const submit = screen.getByRole('button', { name: 'Sign in' })
  await user.click(submit)
  // The form re-enables in the `finally` that follows `adopt`, so an enabled
  // button means the session has already flipped and only the screen itself is
  // outstanding. Counting microtask ticks would say the same thing today and
  // quietly stop saying it the moment the promise chain gains a link.
  await waitFor(() => expect(submit).toBeEnabled())
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

  /*
   * The deferral is narrow — only this status — and the reason is the unscoped
   * queryClient.clear() in applySession, whose safety rests on the tree
   * unmounting in the SAME commit. This pins the claim that lets the two
   * coexist: a challenge carries no user id, and neither does the state it
   * replaces, so the flip never trips the clear.
   */
  it('does not wipe the query cache on the way into the challenge', async () => {
    const user = userEvent.setup()
    const client = makeQueryClient()
    const clear = vi.spyOn(client, 'clear')
    signInScreen(client)
    await signIn(user)

    expect(clear).not.toHaveBeenCalled()
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

describe('the sign-in to password-recovery transition', () => {
  it('asks for the recovery screen while the sign-in form is still on screen', async () => {
    signInScreen()
    expect(await screen.findByRole('heading', { name: /welcome back/i })).toBeInTheDocument()
    await waitFor(() => expect(forgotChunk.state.requested).toBe(true))
  })

  // Reached by a click on the form, not by a URL token: nothing has navigated,
  // so blanking the viewport is the same defect the code screen had.
  it('keeps the sign-in screen on the glass while the recovery screen loads', async () => {
    const user = userEvent.setup()
    signInScreen()
    await screen.findByRole('heading', { name: /welcome back/i })

    await user.click(screen.getByRole('button', { name: /forgot your password/i }))

    expect(screen.queryByText('Loading…')).not.toBeInTheDocument()
    expect(screen.getByRole('heading', { name: /welcome back/i })).toBeInTheDocument()

    act(() => forgotChunk.land())
    expect(await screen.findByRole('heading', { name: /recover your account/i })).toBeInTheDocument()
  })
})
