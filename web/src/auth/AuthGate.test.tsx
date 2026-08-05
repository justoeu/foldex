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
const mockedTokens: { invite?: string } = {}
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

describe('AuthGate invite handling', () => {
  beforeEach(() => {
    mockedTokens.invite = undefined
  })

  it('prefers the invite screen over login when a token is present', async () => {
    mockedTokens.invite = 'TESTTOKEN'
    vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
      if (url === '/api/auth/me') return { data: { status: 'anonymous', features } } as never
      if (url.startsWith('/api/auth/invites/'))
        return { data: { email: 'new@example.com', role: 'user', expires_at: '' } } as never
      return { data: {} } as never
    })

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
      throw { response: { status: 404, data: { error: { code: 'not_found' } } } }
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
