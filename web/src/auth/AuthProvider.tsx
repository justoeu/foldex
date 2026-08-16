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
import { advanceAuthEpoch, setSessionLostHandler } from '../api/client'
import { defaultFeatures, type SessionState, type TwoFactorPending } from './types'

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
  switch (me.status) {
    case 'anonymous':
      return { status: 'anonymous', features: me.features }
    case 'setup_required':
      return { status: 'setup_required', features: me.features }
    case 'authenticated':
      return {
        status: 'authenticated',
        user: me.user,
        csrfToken: me.csrf_token,
        features: me.features,
      }
    case 'two_factor_required':
      // A half-finished login. It is a SESSION state rather than local state in
      // the login screen because three different screens can produce it — login,
      // invite acceptance and password reset — and all three funnel through
      // `adopt`. Keeping it here means none of them needs to know the flow exists.
      const pending: TwoFactorPending =
        me.purpose === 'totp'
          ? {
              purpose: 'totp',
              email: me.email,
              methods: me.methods,
              maxAttempts: me.max_attempts,
            }
          : {
              purpose: 'enroll_2fa',
              email: me.email,
              methods: me.methods,
              maxAttempts: me.max_attempts,
            }
      return {
        status: 'two_factor_required',
        pending,
        features: me.features,
      }
    case 'convert_password_account':
      return {
        status: 'convert_password_account',
        email: me.email,
        features: me.features,
      }
    default: {
      const exhaustive: never = me
      throw new Error(`Unsupported auth response: ${JSON.stringify(exhaustive)}`)
    }
  }
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

const LAST_OWNER_KEY = 'foldex.auth.lastOwnerId'

function getLocalStorage(): Storage | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  } catch {
    return null
  }
}

function clearTenantScopedState(storage = getLocalStorage()): void {
  if (!storage) return
  let removalFailed = false
  TENANT_SCOPED_KEYS.forEach((key) => {
    try {
      storage.removeItem(key)
    } catch {
      removalFailed = true
    }
  })
  if (!removalFailed) return
  try {
    // A partial cleanup could expose the previous tenant after storage recovers.
    storage.clear()
  } catch {
    // Fully unavailable storage cannot be read by the next session either.
  }
}

function readLastOwnerId(storage: Storage | null): number | null {
  if (!storage) return null
  try {
    return parseOwnerId(storage.getItem(LAST_OWNER_KEY))
  } catch {
    return null
  }
}

function parseOwnerId(raw: string | null): number | null {
  if (raw === null) return null
  const id = Number(raw)
  return Number.isSafeInteger(id) && id > 0 ? id : null
}

function persistLastOwnerId(storage: Storage | null, id: number | null): void {
  if (!storage) return
  try {
    if (id === null) storage.removeItem(LAST_OWNER_KEY)
    else storage.setItem(LAST_OWNER_KEY, String(id))
  } catch {
    // Authentication must still resolve when browser storage is unavailable.
  }
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
  const reloadRequest = useRef(0)

  const applySession = useCallback(
    (next: SessionState, writeOwnerMarker = true) => {
      const nextId = next.status === 'authenticated' ? next.user.id : null
      const previousId = lastUserId.current
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
      }
      const storage = getLocalStorage()
      if (nextId === null) {
        // A cold anonymous result may represent an expired or revoked session,
        // so no in-memory previous owner exists to trigger the normal switch.
        clearTenantScopedState(storage)
        persistLastOwnerId(storage, null)
      } else {
        const priorOwner = previousId ?? readLastOwnerId(storage)
        if (priorOwner !== nextId) clearTenantScopedState(storage)
        if (writeOwnerMarker) persistLastOwnerId(storage, nextId)
      }
      lastUserId.current = nextId
      setSession(next)
    },
    [queryClient],
  )

  const probeSession = useCallback(async (writeOwnerMarker: boolean) => {
    const request = ++reloadRequest.current
    try {
      const next = toState(await fetchMe())
      if (request === reloadRequest.current) applySession(next, writeOwnerMarker)
    } catch {
      // /api/auth/me is contractually always 200, so a throw means the backend
      // is unreachable rather than the caller being signed out. A cold load
      // may show the login screen, but only an authoritative response may
      // discard tenant data or replace a session that was already usable.
      if (request === reloadRequest.current) {
        setSession((current) =>
          current.status === 'authenticated'
            ? current
            : { status: 'anonymous', features: defaultFeatures },
        )
      }
    }
  }, [applySession])

  const reload = useCallback(() => probeSession(true), [probeSession])

  useEffect(() => {
    if (initialState) return
    void reload()
  }, [initialState, reload])

  useEffect(() => {
    const storage = getLocalStorage()
    if (!storage || typeof window === 'undefined') return
    const onStorage = (event: StorageEvent) => {
      if (event.key !== LAST_OWNER_KEY) return
      const nextOwner = parseOwnerId(event.newValue)
      if (event.newValue !== null && nextOwner === null) return
      if (nextOwner === lastUserId.current) return

      if (nextOwner === null) {
        reloadRequest.current++
        advanceAuthEpoch()
        applySession({ status: 'anonymous', features: defaultFeatures })
        return
      }
      advanceAuthEpoch()
      queryClient.clear()
      // The originating tab already wrote the new owner marker. Do not write
      // it again after probing or the tabs can bounce storage events.
      void probeSession(false)
    }
    window.addEventListener('storage', onStorage)
    return () => window.removeEventListener('storage', onStorage)
  }, [applySession, probeSession, queryClient])

  // When a refresh definitively fails, drop to anonymous so the gate swaps in
  // the login screen instead of leaving a dead session rendering 401s.
  useEffect(() => {
    setSessionLostHandler(() => {
      reloadRequest.current++
      advanceAuthEpoch()
      applySession({ status: 'anonymous', features: defaultFeatures })
    })
    return () => setSessionLostHandler(null)
  }, [applySession])

  const adopt = useCallback(
    (me: MeResponse) => {
      reloadRequest.current++
      advanceAuthEpoch()
      applySession(toState(me))
    },
    [applySession],
  )

  const signOut = useCallback(async () => {
    reloadRequest.current++
    advanceAuthEpoch()
    applySession({ status: 'anonymous', features: defaultFeatures })
    try {
      await apiLogout()
    } catch {
      // Swallowed, not rethrown. Signing out is best-effort by design: the
      // server call only revokes the session family, and the local state above is
      // what the user actually asked for. Rethrowing would make the natural
      // call site — `onClick={() => void signOut()}` — an unhandled rejection
      // in the console, and would give callers an error they can do nothing
      // useful with.
    }
  }, [applySession])

  const value = useMemo<AuthContextValue>(
    () => ({ session, adopt, signOut, reload }),
    [session, adopt, signOut, reload],
  )
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
