import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AuthProvider, useAuth, useCurrentUser } from './AuthProvider'
import { makeQueryClient, testAdminSession, testAdminUser } from '../test/renderWithProviders'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { http, resetRefreshState } from '../api/client'
import type { SessionState } from './types'
import type { MeResponse } from '../api/auth'

const features = { google_oauth: false, two_factor: false, email_delivery: false }
const lastOwnerKey = 'foldex.auth.lastOwnerId'

function typecheckMeResponseContract() {
  // @ts-expect-error anonymous responses require feature flags
  void ({ status: 'anonymous' } satisfies MeResponse)
  // @ts-expect-error setup responses require feature flags
  void ({ status: 'setup_required' } satisfies MeResponse)
  // @ts-expect-error authenticated responses require a user
  void ({ status: 'authenticated', csrf_token: 'csrf', features } satisfies MeResponse)
  // @ts-expect-error authenticated responses require a CSRF token
  void ({ status: 'authenticated', user: testAdminUser, features } satisfies MeResponse)
  const unknownPurpose = {
    status: 'two_factor_required',
    purpose: 'convert_google' as const,
    email: 'a••@b.test',
    methods: [],
    expires_in: 300,
    max_attempts: 5,
    features,
  }
  // @ts-expect-error conversion is not a two-factor purpose
  void (unknownPurpose satisfies MeResponse)
  // @ts-expect-error anonymous responses cannot carry an authenticated user
  void ({ status: 'anonymous', user: testAdminUser, features } satisfies MeResponse)
  const conversionWithoutEmail = {
    status: 'convert_password_account',
    purpose: 'convert_google' as const,
    methods: [],
    expires_in: 300,
    max_attempts: 5,
    features,
  }
  // @ts-expect-error conversion responses require the server-masked email
  void (conversionWithoutEmail satisfies MeResponse)
  const twoFactorWithoutBudget = {
    status: 'two_factor_required',
    purpose: 'totp' as const,
    email: 'a••@b.test',
    methods: ['totp'],
    expires_in: 300,
    features,
  }
  // @ts-expect-error second-factor responses require the server attempt budget
  void (twoFactorWithoutBudget satisfies MeResponse)
  const twoFactorWithoutExpiry = {
    status: 'two_factor_required',
    purpose: 'totp' as const,
    email: 'a••@b.test',
    methods: ['totp'],
    max_attempts: 5,
    features,
  }
  // @ts-expect-error second-factor responses require the challenge expiry
  void (twoFactorWithoutExpiry satisfies MeResponse)
  const enrollmentWithoutReason = {
    status: 'two_factor_required',
    purpose: 'enroll_2fa' as const,
    email: 'a••@b.test',
    methods: [],
    expires_in: 300,
    max_attempts: 5,
    features,
  }
  // @ts-expect-error enrollment responses require the producer's reason
  void (enrollmentWithoutReason satisfies MeResponse)
  const conversionChallenge = {
    status: 'convert_password_account' as const,
    purpose: 'convert_google' as const,
    email: 'a••@b.test',
    methods: [],
    expires_in: 300,
    max_attempts: 5,
    features,
  }
  const { purpose: _purpose, ...conversionWithoutPurpose } = conversionChallenge
  // @ts-expect-error conversion responses require their challenge purpose
  void (conversionWithoutPurpose satisfies MeResponse)
  const { methods: _methods, ...conversionWithoutMethods } = conversionChallenge
  // @ts-expect-error conversion responses require their challenge methods
  void (conversionWithoutMethods satisfies MeResponse)
  const { expires_in: _expiresIn, ...conversionWithoutExpiry } = conversionChallenge
  // @ts-expect-error conversion responses require their challenge expiry
  void (conversionWithoutExpiry satisfies MeResponse)
  const { max_attempts: _maxAttempts, ...conversionWithoutBudget } = conversionChallenge
  // @ts-expect-error conversion responses require their challenge attempt budget
  void (conversionWithoutBudget satisfies MeResponse)
}

const mappingCases: Array<{ response: MeResponse; expected: SessionState }> = [
  {
    response: { status: 'anonymous', features },
    expected: { status: 'anonymous', features },
  },
  {
    response: { status: 'setup_required', features },
    expected: { status: 'setup_required', features },
  },
  {
    response: {
      status: 'authenticated',
      user: testAdminUser,
      csrf_token: 'wire-csrf',
      features,
    },
    expected: {
      status: 'authenticated',
      user: testAdminUser,
      csrfToken: 'wire-csrf',
      features,
    },
  },
  {
    response: {
      status: 'two_factor_required',
      purpose: 'totp',
      email: 'a••@b.test',
      methods: ['totp', 'recovery_code'],
      expires_in: 300,
      max_attempts: 4,
      features,
    },
    expected: {
      status: 'two_factor_required',
      pending: {
        purpose: 'totp',
        email: 'a••@b.test',
        methods: ['totp', 'recovery_code'],
        maxAttempts: 4,
      },
      features,
    },
  },
  {
    response: {
      status: 'two_factor_required',
      purpose: 'enroll_2fa',
      email: 'a••@b.test',
      methods: [],
      expires_in: 300,
      max_attempts: 5,
      features,
      reason: 'admin_enrollment_required',
    },
    expected: {
      status: 'two_factor_required',
      pending: {
        purpose: 'enroll_2fa',
        email: 'a••@b.test',
        methods: [],
        maxAttempts: 5,
      },
      features,
    },
  },
  {
    response: {
      status: 'convert_password_account',
      purpose: 'convert_google',
      email: 'a••@b.test',
      methods: [],
      expires_in: 300,
      max_attempts: 5,
      features,
    },
    expected: { status: 'convert_password_account', email: 'a••@b.test', features },
  },
]

function Probe() {
  const { session, adopt, signOut, reload } = useAuth()
  const user = useCurrentUser()
  return (
    <div>
      <span data-testid="status">{session.status}</span>
      <span data-testid="email">{user?.email ?? '—'}</span>
      <span data-testid="session">{JSON.stringify(session)}</span>
      <button
        onClick={() =>
          adopt({
            status: 'authenticated',
            user: { ...testAdminUser, id: 2, email: 'other@foldex.test' },
            csrf_token: 'x',
            features,
          })
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
          })
        }
      >
        re-adopt same user
      </button>
      <button onClick={() => void signOut()}>sign out</button>
      <button onClick={() => void reload()}>reload</button>
    </div>
  )
}

function dispatchOwnerChange(oldValue: string | null, newValue: string | null) {
  if (newValue === null) localStorage.removeItem(lastOwnerKey)
  else localStorage.setItem(lastOwnerKey, newValue)
  act(() => {
    window.dispatchEvent(new StorageEvent('storage', { key: lastOwnerKey, oldValue, newValue }))
  })
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

beforeEach(() => localStorage.clear())

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

  it('keeps tenant state on a cold load for the same owner', async () => {
    localStorage.setItem(lastOwnerKey, String(testAdminUser.id))
    localStorage.setItem('foldex.viewMode.map', '{"folder.7":"list"}')
    vi.spyOn(http, 'get').mockResolvedValue({
      data: {
        status: 'authenticated',
        user: testAdminUser,
        csrf_token: 'csrf',
        features,
      },
    } as never)

    renderProbe()

    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('authenticated'))
    expect(localStorage.getItem('foldex.viewMode.map')).toBe('{"folder.7":"list"}')
    expect(localStorage.getItem(lastOwnerKey)).toBe(String(testAdminUser.id))
  })

  it('drops tenant state on a cold load for a different owner', async () => {
    localStorage.setItem(lastOwnerKey, '99')
    localStorage.setItem('foldex.viewMode.map', '{"folder.7":"list"}')
    vi.spyOn(http, 'get').mockResolvedValue({
      data: {
        status: 'authenticated',
        user: testAdminUser,
        csrf_token: 'csrf',
        features,
      },
    } as never)

    renderProbe()

    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('authenticated'))
    expect(localStorage.getItem('foldex.viewMode.map')).toBeNull()
    expect(localStorage.getItem(lastOwnerKey)).toBe(String(testAdminUser.id))
    expect(localStorage.getItem(lastOwnerKey)).not.toContain(testAdminUser.email)
  })

  it('drops prior tenant state when a cold session resolves anonymous', async () => {
    localStorage.setItem(lastOwnerKey, String(testAdminUser.id))
    localStorage.setItem('foldex.backups', '[{"id":"previous-owner"}]')
    vi.spyOn(http, 'get').mockResolvedValue({ data: { status: 'anonymous', features } } as never)

    renderProbe()

    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('anonymous'))
    expect(localStorage.getItem('foldex.backups')).toBeNull()
    expect(localStorage.getItem(lastOwnerKey)).toBeNull()
  })

  it('resolves authentication when localStorage operations fail', async () => {
    vi.spyOn(localStorage, 'getItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError')
    })
    vi.spyOn(localStorage, 'setItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError')
    })
    vi.spyOn(localStorage, 'removeItem').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError')
    })
    vi.spyOn(localStorage, 'clear').mockImplementation(() => {
      throw new DOMException('blocked', 'SecurityError')
    })
    vi.spyOn(http, 'get').mockResolvedValue({
      data: {
        status: 'authenticated',
        user: testAdminUser,
        csrf_token: 'csrf',
        features,
      },
    } as never)

    renderProbe()

    await waitFor(() => expect(screen.getByTestId('email')).toHaveTextContent(testAdminUser.email))
  })

  it('falls back to full cleanup when one tenant key cannot be removed', async () => {
    localStorage.setItem(lastOwnerKey, String(testAdminUser.id))
    localStorage.setItem('foldex.viewMode.map', '{"folder.7":"list"}')
    localStorage.setItem('foldex.backups', '[{"id":"previous-owner"}]')
    const removeItem = localStorage.removeItem.bind(localStorage)
    vi.spyOn(localStorage, 'removeItem').mockImplementation((key) => {
      if (key === 'foldex.viewMode.map') throw new DOMException('failed', 'UnknownError')
      removeItem(key)
    })
    vi.spyOn(http, 'get').mockResolvedValue({ data: { status: 'anonymous', features } } as never)

    renderProbe()

    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('anonymous'))
    expect(localStorage.getItem('foldex.viewMode.map')).toBeNull()
    expect(localStorage.getItem('foldex.backups')).toBeNull()
    expect(localStorage.getItem(lastOwnerKey)).toBeNull()
  })

  it.each(mappingCases)(
    'maps the $response.status response without fallbacks',
    async ({ response, expected }) => {
      vi.spyOn(http, 'get').mockResolvedValue({ data: response } as never)
      renderProbe()

      await waitFor(() => {
        const rendered = JSON.parse(screen.getByTestId('session').textContent ?? 'null')
        expect(rendered).toEqual(expected)
      })
    },
  )

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
    expect(localStorage.getItem(lastOwnerKey)).toBe(String(testAdminUser.id))
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
    expect(localStorage.getItem(lastOwnerKey)).toBe('2')
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
  it('reports anonymous without erasing tenant state when cold /me is unreachable', async () => {
    localStorage.setItem(lastOwnerKey, String(testAdminUser.id))
    localStorage.setItem('foldex.viewMode.map', '{"folder.7":"list"}')
    vi.spyOn(http, 'get').mockRejectedValue(new Error('down'))
    renderProbe()

    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('anonymous'))
    expect(localStorage.getItem(lastOwnerKey)).toBe(String(testAdminUser.id))
    expect(localStorage.getItem('foldex.viewMode.map')).toBe('{"folder.7":"list"}')
  })

  it('preserves an authenticated session and tenant state when /me is unreachable', async () => {
    localStorage.setItem(lastOwnerKey, String(testAdminUser.id))
    localStorage.setItem('foldex.backups', '[{"id":"mine"}]')
    vi.spyOn(http, 'get').mockRejectedValue(new Error('down'))
    renderProbe(testAdminSession)

    await userEvent.click(screen.getByRole('button', { name: /reload/i }))

    await waitFor(() => expect(http.get).toHaveBeenCalledWith('/api/auth/me'))
    expect(screen.getByTestId('status')).toHaveTextContent('authenticated')
    expect(screen.getByTestId('email')).toHaveTextContent(testAdminUser.email)
    expect(localStorage.getItem(lastOwnerKey)).toBe(String(testAdminUser.id))
    expect(localStorage.getItem('foldex.backups')).toBe('[{"id":"mine"}]')
  })

  it('synchronizes logout from another tab and clears the previous query cache', async () => {
    localStorage.setItem(lastOwnerKey, String(testAdminUser.id))
    localStorage.setItem('foldex.backups', '[{"id":"mine"}]')
    vi.spyOn(http, 'get').mockResolvedValue({ data: { status: 'anonymous', features } } as never)
    const { client } = renderProbe(testAdminSession, true)
    client.setQueryData(['links'], [{ id: 1, title: 'private' }])

    dispatchOwnerChange(String(testAdminUser.id), null)

    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('anonymous'))
    expect(http.get).not.toHaveBeenCalled()
    expect(client.getQueryData(['links'])).toBeUndefined()
    expect(localStorage.getItem('foldex.backups')).toBeNull()
    expect(localStorage.getItem(lastOwnerKey)).toBeNull()
  })

  it('treats a removed owner marker as authoritative logout without probing', async () => {
    localStorage.setItem(lastOwnerKey, String(testAdminUser.id))
    vi.spyOn(http, 'get').mockResolvedValue({
      data: {
        status: 'authenticated',
        user: testAdminUser,
        csrf_token: 'still-live',
        features,
      },
    } as never)
    renderProbe(testAdminSession)

    dispatchOwnerChange(String(testAdminUser.id), null)

    await waitFor(() => expect(screen.getByTestId('status')).toHaveTextContent('anonymous'))
    expect(http.get).not.toHaveBeenCalled()
    expect(localStorage.getItem(lastOwnerKey)).toBeNull()
  })

  it('synchronizes an owner switch from another tab', async () => {
    const otherUser = { ...testAdminUser, id: 2, email: 'other@foldex.test' }
    localStorage.setItem(lastOwnerKey, String(testAdminUser.id))
    localStorage.setItem('foldex.viewMode.map', '{"folder.7":"list"}')
    vi.spyOn(http, 'get').mockResolvedValue({
      data: { status: 'authenticated', user: otherUser, csrf_token: 'next', features },
    } as never)
    const { client } = renderProbe(testAdminSession, true)
    client.setQueryData(['entries'], [{ id: 1, title: 'previous owner' }])

    dispatchOwnerChange(String(testAdminUser.id), String(otherUser.id))

    await waitFor(() => expect(screen.getByTestId('email')).toHaveTextContent(otherUser.email))
    expect(http.get).toHaveBeenCalledWith('/api/auth/me')
    expect(client.getQueryData(['entries'])).toBeUndefined()
    expect(localStorage.getItem('foldex.viewMode.map')).toBeNull()
    expect(localStorage.getItem(lastOwnerKey)).toBe(String(otherUser.id))
  })

  it('ignores same-owner storage events without clearing or reloading', () => {
    localStorage.setItem(lastOwnerKey, String(testAdminUser.id))
    const get = vi.spyOn(http, 'get')
    const { client } = renderProbe(testAdminSession, true)
    client.setQueryData(['links'], [{ id: 1, title: 'still mine' }])

    dispatchOwnerChange(String(testAdminUser.id), String(testAdminUser.id))

    expect(get).not.toHaveBeenCalled()
    expect(client.getQueryData(['links'])).toEqual([{ id: 1, title: 'still mine' }])
  })

  it('throws a useful error when useAuth is used outside the provider', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    expect(() => render(<Probe />)).toThrow(/must be used inside <AuthProvider>/)
    spy.mockRestore()
  })
})
