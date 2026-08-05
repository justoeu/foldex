import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { fetchMe, logout as apiLogout, type MeResponse } from '../api/auth'
import { setSessionLostHandler } from '../api/client'
import { defaultFeatures, type SessionState } from './types'

type AuthContextValue = {
  session: SessionState
  /** Adopts the payload a login/bootstrap/invite response returned. */
  adopt: (me: MeResponse) => void
  signOut: () => Promise<void>
  /** Re-probes /api/auth/me. */
  reload: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
  return ctx
}

/** Convenience accessor for the common "who am I" case. */
export function useCurrentUser() {
  const { session } = useAuth()
  return session.status === 'authenticated' ? session.user : null
}

function toState(me: MeResponse): SessionState {
  if (me.status === 'authenticated' && me.user) {
    return {
      status: 'authenticated',
      user: me.user,
      csrfToken: me.csrf_token ?? '',
      features: me.features ?? defaultFeatures,
    }
  }
  if (me.status === 'setup_required') {
    return { status: 'setup_required', features: me.features ?? defaultFeatures }
  }
  return { status: 'anonymous', features: me.features ?? defaultFeatures }
}

/**
 * localStorage keys scoped to a TENANT's data, cleared whenever the identity
 * behind the tab changes.
 *
 * viewMode/foldersCompact are keyed by `folder.<id>`, and folder ids are dense
 * per-tenant BIGSERIALs — so after a user switch the previous tenant's
 * preferences would silently apply to entirely unrelated folders. The backup
 * history is a record of the previous account's exports and has no business
 * being visible to the next one.
 *
 * Device preferences (`foldex.dark`, `foldex.locale`, `foldex.grid.cols`,
 * `foldex.sidebar.collapsed`) are deliberately NOT in this list: they describe
 * the browser, not the account, and wiping them on every sign-out would be a
 * small daily annoyance for no security gain.
 */
const TENANT_SCOPED_KEYS = [
  'foldex.viewMode.map',
  'foldex.foldersCompact.map',
  'foldex.backups',
  'foldex.backup',
]

function clearTenantScopedState(): void {
  if (typeof localStorage === 'undefined') return
  TENANT_SCOPED_KEYS.forEach((k) => localStorage.removeItem(k))
}

export function AuthProvider({
  children,
  initialState,
}: {
  children: ReactNode
  /**
   * Pre-seeded session, used by tests.
   *
   * The existing ~60 component test files render deep inside the app and know
   * nothing about auth; renderWithProviders passes an authenticated admin by
   * default so none of them had to change. A test that wants the anonymous
   * path passes it explicitly.
   */
  initialState?: SessionState
}) {
  const [session, setSession] = useState<SessionState>(initialState ?? { status: 'loading' })
  const queryClient = useQueryClient()
  // Tracks which account the cached queries belong to, so a switch can be
  // detected without re-running the effect on every unrelated state change.
  const lastUserId = useRef<number | null>(
    initialState?.status === 'authenticated' ? initialState.user.id : null,
  )

  const applySession = useCallback(
    (next: SessionState) => {
      const nextId = next.status === 'authenticated' ? next.user.id : null
      if (lastUserId.current !== nextId) {
        // Wipe the whole cache rather than segmenting every query key by user.
        //
        // Segmenting would mean touching eight key factories, ~30
        // invalidateQueries calls and the setQueriesData prefix writes — and a
        // single missed one leaks another tenant's rows into the grid with no
        // visible symptom. Clearing is one line and cannot be partially
        // applied. AuthGate unmounts <App/> across the transition, so no
        // observer survives to refetch into the old cache.
        queryClient.clear()
        if (lastUserId.current !== null) clearTenantScopedState()
        lastUserId.current = nextId
      }
      setSession(next)
    },
    [queryClient],
  )

  const reload = useCallback(async () => {
    try {
      applySession(toState(await fetchMe()))
    } catch {
      // /api/auth/me is contractually always 200, so a throw means the backend
      // is unreachable rather than the caller being signed out. Report
      // anonymous — the login screen is a far better failure mode than an
      // infinite spinner, and it retries on submit.
      applySession({ status: 'anonymous', features: defaultFeatures })
    }
  }, [applySession])

  useEffect(() => {
    if (initialState) return
    void reload()
  }, [initialState, reload])

  // When a refresh definitively fails, drop to anonymous so the gate swaps in
  // the login screen instead of leaving a dead session rendering 401s.
  useEffect(() => {
    setSessionLostHandler(() => {
      applySession({ status: 'anonymous', features: defaultFeatures })
    })
    return () => setSessionLostHandler(null)
  }, [applySession])

  const adopt = useCallback((me: MeResponse) => applySession(toState(me)), [applySession])

  const signOut = useCallback(async () => {
    try {
      await apiLogout()
    } finally {
      // Always drop to anonymous, even if the request failed. The user asked to
      // be forgotten; leaving them apparently signed in because the network
      // blipped is the one outcome that is clearly wrong.
      applySession({ status: 'anonymous', features: defaultFeatures })
    }
  }, [applySession])

  const value = useMemo<AuthContextValue>(
    () => ({ session, adopt, signOut, reload }),
    [session, adopt, signOut, reload],
  )
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
