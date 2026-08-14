import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AuthProvider, useAuth, useCurrentUser } from './AuthProvider'
import { makeQueryClient, testAdminSession, testAdminUser } from '../test/renderWithProviders'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { http, resetRefreshState } from '../api/client'
import type { SessionState } from './types'

const features = { google_oauth: false, two_factor: false, email_delivery: false }

function Probe() {
  const { session, adopt, signOut } = useAuth()
  const user = useCurrentUser()
  return (
    <div>
      <span data-testid="status">{session.status}</span>
      <span data-testid="email">{user?.email ?? '—'}</span>
      <button
        onClick={() =>
          adopt({
            status: 'authenticated',
            user: { ...testAdminUser, id: 2, email: 'other@foldex.test' },
            csrf_token: 'x',
            features,
          } as never)
        }
      >
        switch user
      </button>
      <button
        onClick={() =>
          adopt({
            status: 'authenticated',
            user: { ...testAdminUser, name: 'Renamed' },
            csrf_token: 'rotated',
            features,
          } as never)
        }
      >
        re-adopt same user
      </button>
      <button onClick={() => void signOut()}>sign out</button>
    </div>
  )
}

/**
 * Builds the probe.
 *
 * `cacheable` swaps in a client with a real gcTime. The shared test client uses
 * gcTime: 0, which collects any setQueryData that has no observer the moment it
 * is written — so the "cache was cleared" assertion would hold whether or not
 * AuthProvider ever called clear(). That is a false green, and it is exactly
 * the kind that survives a rewrite of the code under test.
 */
function renderProbe(initialState?: SessionState, cacheable = false) {
  const client = cacheable
    ? new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 60_000 } } })
    : makeQueryClient()
  const result = render(
    <QueryClientProvider client={client}>
      <AuthProvider initialState={initialState}>
        <Probe />
      </AuthProvider>
    </QueryClientProvider>,
  )
  return { client, ...result }
}

async function runAuthErrorInterceptor(error: unknown) {
  const handlers = (http.interceptors.response as unknown as {
    handlers: Array<{ rejected?: (e: any) => any }>
  }).handlers
  const rejected = handlers.find((h) => h?.rejected)?.rejected
  return rejected!(error)
}

afterEach(() => {
  resetRefreshState()
  vi.restoreAllMocks()
})

describe('AuthProvider', () => {
  it('uses the seeded session without probing /me', () => {
    const get = vi.spyOn(http, 'get')
    renderProbe(testAdminSession)

    expect(screen.getByTestId('status')).toHaveTextContent('authenticated')
    expect(screen.getByTestId('email')).toHaveTextContent('admin@foldex.test')
    // A seeded state means the ~60 existing component tests never have to mock
    // an endpoint they do not care about.
    expect(get).not.toHaveBeenCalled()
  })

  it('probes /me when no session is seeded', async () => {
    vi.spyOn(http, 'get').mockResolvedValue({
      data: { status: 'anonymous', features },
    } as never)
    renderProbe()

    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('anonymous'))
  })

  /**
   * Cache isolation between tenants.
   *
   * Segmenting every query key by user id would mean touching eight key
   * factories, ~30 invalidateQueries calls and the setQueriesData prefix
   * writes — and a single missed one leaks another tenant's rows into the grid
   * with no visible symptom. Clearing is one line and cannot be partially
   * applied.
   */
  it('clears the query cache when the identity changes', async () => {
    const { client } = renderProbe(testAdminSession, true)
    client.setQueryData(['links'], [{ id: 1, title: "tenant A's link" }])
    expect(client.getQueryData(['links'])).toBeTruthy()

    await userEvent.click(screen.getByRole('button', { name: /switch user/i }))

    await waitFor(() => expect(screen.getByTestId('email')).toHaveTextContent('other@foldex.test'))
    expect(client.getQueryData(['links'])).toBeUndefined()
  })

  it('keeps the cache when the SAME user is re-adopted', async () => {
    const { client } = renderProbe(testAdminSession, true)
    client.setQueryData(['links'], [{ id: 1, title: 'still mine' }])

    await userEvent.click(screen.getByRole('button', { name: /re-adopt same user/i }))

    // Re-adopting the same id is what every token refresh does. Clearing there
    // would throw the whole cache away every 15 minutes and refetch the grid.
    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('authenticated'))
    expect(client.getQueryData(['links'])).toBeTruthy()
  })

  it('drops tenant-scoped localStorage on a user switch', async () => {
    localStorage.setItem('foldex.viewMode.map', '{"folder.7":"list"}')
    localStorage.setItem('foldex.backups', '[{"id":"a"}]')
    localStorage.setItem('foldex.dark', 'true')
    localStorage.setItem('foldex.locale', 'pt')

    renderProbe(testAdminSession)
    await userEvent.click(screen.getByRole('button', { name: /switch user/i }))

    await waitFor(() => expect(screen.getByTestId('email')).toHaveTextContent('other@foldex.test'))
    // Keyed by the previous tenant's folder ids, which are dense per-user
    // BIGSERIALs — they would silently apply to unrelated folders.
    expect(localStorage.getItem('foldex.viewMode.map')).toBeNull()
    expect(localStorage.getItem('foldex.backups')).toBeNull()
    // Device preferences describe the browser, not the account.
    expect(localStorage.getItem('foldex.dark')).toBe('true')
    expect(localStorage.getItem('foldex.locale')).toBe('pt')
  })

  it('drops to anonymous on sign out', async () => {
    vi.spyOn(http, 'post').mockResolvedValue({ data: null } as never)
    renderProbe(testAdminSession)

    await userEvent.click(screen.getByRole('button', { name: /sign out/i }))
    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('anonymous'))
  })

  it('drops to anonymous before the logout request completes', async () => {
    let finishLogout!: () => void
    vi.spyOn(http, 'post').mockReturnValue(
      new Promise<void>((resolve) => {
        finishLogout = resolve
      }) as never,
    )
    renderProbe(testAdminSession)

    await userEvent.click(screen.getByRole('button', { name: /sign out/i }))
    expect(screen.getByTestId('status')).toHaveTextContent('anonymous')

    finishLogout()
  })

  it('ignores a successful refresh that completes after sign out', async () => {
    let finishRefresh!: () => void
    const refresh = new Promise<void>((resolve) => {
      finishRefresh = resolve
    })
    vi.spyOn(http, 'post').mockImplementation((url: string) => {
      if (url === '/api/auth/refresh') return refresh as never
      return Promise.resolve({ data: null }) as never
    })
    const retry = vi.spyOn(http, 'request').mockResolvedValue({ status: 200 } as never)
    const originalError = {
      response: { status: 401 },
      config: { url: '/api/links', method: 'get', headers: {} },
    }
    renderProbe(testAdminSession)

    const staleRequest = runAuthErrorInterceptor(originalError)
    await vi.waitFor(() => expect(http.post).toHaveBeenCalledWith('/api/auth/refresh', null, expect.anything()))
    await userEvent.click(screen.getByRole('button', { name: /sign out/i }))
    expect(screen.getByTestId('status')).toHaveTextContent('anonymous')

    finishRefresh()
    await expect(staleRequest).rejects.toBe(originalError)
    expect(retry).not.toHaveBeenCalled()
    expect(screen.getByTestId('status')).toHaveTextContent('anonymous')
  })

  // The user asked to be forgotten. Leaving them apparently signed in because
  // the network blipped is the one clearly wrong outcome.
  it('drops to anonymous even when the logout request fails', async () => {
    vi.spyOn(http, 'post').mockRejectedValue(new Error('offline'))
    renderProbe(testAdminSession)

    await userEvent.click(screen.getByRole('button', { name: /sign out/i }))
    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('anonymous'))
  })

  // /api/auth/me is contractually always 200, so a throw means the backend is
  // unreachable rather than the caller being signed out.
  it('reports anonymous when /me is unreachable', async () => {
    vi.spyOn(http, 'get').mockRejectedValue(new Error('down'))
    renderProbe()

    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('anonymous'))
  })

  it('throws a useful error when useAuth is used outside the provider', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    expect(() => render(<Probe />)).toThrow(/must be used inside <AuthProvider>/)
    spy.mockRestore()
  })
})
