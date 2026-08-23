import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AuthGate } from './AuthGate'
import { renderWithProviders, testAdminSession } from '../test/renderWithProviders'
import { http } from '../api/client'

// authUrl reads the query string at MODULE scope (see its doc comment), so the
// value is fixed before any test runs. Mocking the module is how a test varies
// it — re-importing under vi.resetModules() would instead build a second copy
// of the whole module graph, giving AuthGate a different React context object
// than the provider the harness renders, and useAuth would not find it.
const mockedTokens: {
  invite?: string
  reset?: string
  verify?: string
  emailChange?: string
} = {}
vi.mock('./authUrl', () => ({
  get urlTokens() {
    return mockedTokens
  },
}))

function mockMe(response: unknown, status = 200) {
  return vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
    if (url === '/api/auth/me') {
      if (status >= 400) throw { response: { status, data: response } }
      return { data: response } as never
    }
    return { data: {} } as never
  })
}

const features = { google_oauth: false, two_factor: false, email_delivery: false }

afterEach(() => vi.restoreAllMocks())

describe('AuthGate', () => {
  it('renders the app when authenticated', () => {
    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: testAdminSession },
    )
    expect(screen.getByText('the app')).toBeInTheDocument()
  })

  it('shows the login screen when anonymous', async () => {
    mockMe({ status: 'anonymous', features })
    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: null },
    )

    expect(await screen.findByRole('heading', { name: /sign in to foldex/i })).toBeInTheDocument()
    // The gate must UNMOUNT the app, not merely hide it. App calls four
    // authenticated queries unconditionally on mount; leaving it rendered would
    // fire all four and 401 each one.
    expect(screen.queryByText('the app')).not.toBeInTheDocument()
  })

  it('shows the setup screen on a fresh instance', async () => {
    mockMe({ status: 'setup_required', features })
    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: null },
    )

    expect(
      await screen.findByRole('heading', { name: /create the administrator account/i }),
    ).toBeInTheDocument()
  })

  // /api/auth/me is contractually always 200, so a throw means the backend is
  // unreachable. Showing the login screen beats an infinite spinner: it retries
  // on submit and tells the user something concrete.
  it('falls back to the login screen when /me is unreachable', async () => {
    mockMe({}, 500)
    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: null },
    )

    expect(await screen.findByRole('heading', { name: /sign in to foldex/i })).toBeInTheDocument()
  })

  it('shows a boot spinner before /me resolves', () => {
    vi.spyOn(http, 'get').mockImplementation(() => new Promise(() => {}) as never)
    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: null },
    )
    expect(screen.getByRole('status')).toBeInTheDocument()
    expect(screen.queryByText('the app')).not.toBeInTheDocument()
  })
})

describe('AuthGate second-factor routing', () => {
  const pending = {
    email: 'ad•••••@foldex.test',
    methods: ['totp', 'recovery_code'],
    maxAttempts: 5,
  }

  // The branch every administrator hits on their first sign-in once
  // AUTH_REQUIRE_2FA_FOR_ADMINS is on — which is the default. Routing it to the
  // ordinary code screen would show a six-digit field for an authenticator that
  // does not exist yet.
  it('shows the enrollment screen for an enroll_2fa challenge', async () => {
    vi.spyOn(http, 'post').mockResolvedValue({
      data: { secret: 'JBSWY3DPEHPK3PXP', qr_url: '/api/auth/2fa/totp/qr.png' },
    } as never)

    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      {
        session: {
          status: 'two_factor_required',
          pending: { ...pending, purpose: 'enroll_2fa' },
          features,
        },
      },
    )

    expect(
      await screen.findByRole('heading', { name: /set up two-step verification/i }),
    ).toBeInTheDocument()
    expect(screen.queryByText('the app')).not.toBeInTheDocument()
  })

  it('shows the code screen for a totp challenge', async () => {
    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      {
        session: {
          status: 'two_factor_required',
          pending: { ...pending, purpose: 'totp' },
          features,
        },
      },
    )

    expect(await screen.findByRole('heading', { name: /enter your code/i })).toBeInTheDocument()
    expect(screen.queryByText('the app')).not.toBeInTheDocument()
  })

  // A half-finished login outranks a reset token: the user is mid-flow with a
  // live pre-auth challenge, and dropping them on the reset form would strand
  // it.
  it('prefers the second factor over a reset token in the URL', async () => {
    mockedTokens.reset = 'RESETTOKEN'
    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      {
        session: {
          status: 'two_factor_required',
          pending: { ...pending, purpose: 'totp' },
          features,
        },
      },
    )

    expect(await screen.findByRole('heading', { name: /enter your code/i })).toBeInTheDocument()
    mockedTokens.reset = undefined
  })
})

describe('AuthGate Google conversion', () => {
  // The conversion challenge is a half-finished sign-in with a live pre-auth
  // cookie behind it, so it outranks everything except the app itself. It is
  // NOT a second factor: routing it to the six-digit screen would put the user
  // in front of a field they can never satisfy.
  it('shows the convert screen for a convert challenge', async () => {
    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: { status: 'convert_password_account', email: 'a••@b.test', features } },
    )

    expect(
      await screen.findByRole('heading', { name: /confirm your password/i }),
    ).toBeInTheDocument()
    expect(screen.queryByText('the app')).not.toBeInTheDocument()
  })

  it('prefers the conversion over an invite token in the URL', async () => {
    mockedTokens.invite = 'INVITETOKEN'
    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: { status: 'convert_password_account', email: 'a••@b.test', features } },
    )

    expect(
      await screen.findByRole('heading', { name: /confirm your password/i }),
    ).toBeInTheDocument()
    mockedTokens.invite = undefined
  })
})

describe('AuthGate password recovery', () => {
  it('opens the forgot screen from the login screen and comes back', async () => {
    mockMe({ status: 'anonymous', features })
    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: null },
    )

    await userEvent.click(await screen.findByRole('button', { name: /forgot your password/i }))
    expect(await screen.findByRole('heading', { name: /reset your password/i })).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /back to sign in/i }))
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /sign in to foldex/i })).toBeInTheDocument(),
    )
  })

  // A reset link is a credential that expires in 30 minutes; honouring it
  // before the ordinary login form is the whole point of reading the URL.
  it('shows the reset screen when the URL carries a token', async () => {
    mockedTokens.reset = 'RESETTOKEN'
    mockMe({ status: 'anonymous', features })
    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: null },
    )

    expect(
      await screen.findByRole('heading', { name: /set a new password/i }),
    ).toBeInTheDocument()
    mockedTokens.reset = undefined
  })
})

describe('AuthGate e-mail confirmation', () => {
  it('consumes a #verify= token from the URL', async () => {
    mockedTokens.verify = 'VERIFYTOKEN'
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    mockMe({ status: 'anonymous', features })

    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: null },
    )

    expect(await screen.findByRole('heading', { name: /e-mail confirmed/i })).toBeInTheDocument()
    expect(post).toHaveBeenCalledWith('/api/auth/email/verify', { token: 'VERIFYTOKEN' })
    mockedTokens.verify = undefined
  })

  // The branch sits ABOVE the authenticated short-circuit: a signed-in user who
  // follows the link from their inbox must still learn whether it worked,
  // rather than landing in the app with no feedback.
  it('shows the outcome even to an already signed-in user', async () => {
    mockedTokens.verify = 'VERIFYTOKEN'
    vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)

    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: testAdminSession },
    )

    expect(await screen.findByRole('heading', { name: /e-mail confirmed/i })).toBeInTheDocument()
    expect(screen.queryByText('the app')).not.toBeInTheDocument()
    mockedTokens.verify = undefined
  })

  it('falls through to the app once dismissed', async () => {
    mockedTokens.verify = 'VERIFYTOKEN'
    vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)

    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: testAdminSession },
    )

    await userEvent.click(await screen.findByRole('button', { name: /continue/i }))
    await waitFor(() => expect(screen.getByText('the app')).toBeInTheDocument())
    mockedTokens.verify = undefined
  })
})

describe('AuthGate invite handling', () => {
  beforeEach(() => {
    mockedTokens.invite = undefined
  })

  it('prefers the invite screen over login when a token is present', async () => {
    mockedTokens.invite = 'TESTTOKEN'
    vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
      if (url === '/api/auth/me') return { data: { status: 'anonymous', features } } as never
      return { data: {} } as never
    })
    vi.spyOn(http, 'post').mockResolvedValue({
      data: { email: 'new@example.com', role: 'user', expires_at: '' },
    } as never)

    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: null },
    )

    // Waiting on the FIELD, not the heading: InviteScreen shows the same title
    // while the token lookup is in flight, so a heading assertion is satisfied
    // by the loading state and the form may not exist yet.
    const email = (await screen.findByLabelText(/e-mail/i)) as HTMLInputElement
    expect(screen.getByRole('heading', { name: /activate your account/i })).toBeInTheDocument()
    // The address is the server's view of the token and must not be editable —
    // otherwise a `user` invite becomes "make me an account for any address".
    expect(email).toHaveValue('new@example.com')
    expect(email).toHaveAttribute('readonly')
  })

  it('falls back to login when the invite is no longer valid', async () => {
    mockedTokens.invite = 'DEAD'
    vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
      if (url === '/api/auth/me') return { data: { status: 'anonymous', features } } as never
      return { data: {} } as never
    })
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 404, data: { error: { code: 'not_found' } } },
    })

    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: null },
    )

    expect(
      await screen.findByRole('heading', { name: /no longer valid/i }),
    ).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: /go to sign in/i }))
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /sign in to foldex/i })).toBeInTheDocument(),
    )
  })
})

// The link that MOVES the account. It revokes every session, so whoever
// follows it is about to become anonymous either way — being dropped into a
// bare login form with no explanation is the outcome this screen exists to
// avoid, which is why it renders ABOVE the authenticated short-circuit.
describe('the e-mail change confirmation', () => {
  it('consumes an #email-change= token from the URL', async () => {
    mockedTokens.emailChange = 'CHANGETOKEN'
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: {} } as never)
    mockMe({ status: 'anonymous', features })

    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: null },
    )

    expect(await screen.findByRole('heading', { name: /your e-mail was changed/i })).toBeInTheDocument()
    expect(post).toHaveBeenCalledWith('/api/auth/email-change/confirm', { token: 'CHANGETOKEN' })
    expect(screen.getByText(/sign in again/i)).toBeInTheDocument()
  })

  it('explains a dead link instead of leaving a spinner', async () => {
    mockedTokens.emailChange = 'SPENT'
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 404, data: { error: { code: 'email_change_invalid' } } },
    })
    mockMe({ status: 'anonymous', features })

    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: null },
    )

    expect(await screen.findByText(/no longer valid/i)).toBeInTheDocument()
  })

  // Claimed between the request and the click. The user can fix this by
  // choosing another address, so it must not read as "your link is broken".
  it('tells the taken-address case apart from a dead link', async () => {
    mockedTokens.emailChange = 'RACED'
    vi.spyOn(http, 'post').mockRejectedValue({
      response: { status: 409, data: { error: { code: 'email_taken' } } },
    })
    mockMe({ status: 'anonymous', features })

    renderWithProviders(
      <AuthGate>
        <div>the app</div>
      </AuthGate>,
      { session: null },
    )

    expect(await screen.findByText(/another account already uses that address/i)).toBeInTheDocument()
  })
})
