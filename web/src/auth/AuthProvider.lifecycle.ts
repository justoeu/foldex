import {
  startTransition,
  useCallback,
  useEffect,
  useRef,
  useState,
} from 'react'
import type { QueryClient } from '@tanstack/react-query'
import { fetchMe, logout as apiLogout, type MeResponse } from '../api/auth'
import { advanceAuthEpoch, setSessionLostHandler } from '../api/client'
import { defaultFeatures, type SessionState, type TwoFactorPending } from './types'

export const LAST_OWNER_KEY = 'foldex.auth.lastOwnerId'

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

export function toState(me: MeResponse): SessionState {
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
        permissions: me.permissions ?? [],
      }
    case 'two_factor_required': {
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

export function ownerIdOf(session: SessionState): number | null {
  return session.status === 'authenticated' ? session.user.id : null
}

export type ApplySessionPlan = {
  nextId: number | null
  clearCache: boolean
  clearTenant: boolean
  persistOwner: number | null | undefined
  defer: boolean
}

export function applySessionPlan({
  next,
  previousId,
  storedOwnerId,
  writeOwnerMarker,
}: {
  next: SessionState
  previousId: number | null
  storedOwnerId: number | null
  writeOwnerMarker: boolean
}): ApplySessionPlan {
  const nextId = ownerIdOf(next)
  const clearCache = previousId !== nextId
  if (nextId === null) {
    return {
      nextId,
      clearCache,
      clearTenant: true,
      persistOwner: null,
      defer: next.status === 'two_factor_required',
    }
  }
  const priorOwner = previousId ?? storedOwnerId
  return {
    nextId,
    clearCache,
    clearTenant: priorOwner !== nextId,
    persistOwner: writeOwnerMarker ? nextId : undefined,
    defer: next.status === 'two_factor_required',
  }
}

export function unreachableFallback(current: SessionState): SessionState | 'keep' {
  return current.status === 'authenticated'
    ? 'keep'
    : { status: 'anonymous', features: defaultFeatures }
}

export type StorageSyncAction = 'ignore' | 'logout' | 'probe'

export function parseOwnerId(raw: string | null): number | null {
  if (raw === null) return null
  const id = Number(raw)
  return Number.isSafeInteger(id) && id > 0 ? id : null
}

export function storageSyncDecision({
  key,
  newValue,
  currentOwnerId,
}: {
  key: string | null
  newValue: string | null
  currentOwnerId: number | null
}): { action: StorageSyncAction } {
  if (key !== LAST_OWNER_KEY) return { action: 'ignore' }
  const nextOwner = parseOwnerId(newValue)
  if (newValue !== null && nextOwner === null) return { action: 'ignore' }
  if (nextOwner === currentOwnerId) return { action: 'ignore' }
  if (nextOwner === null) return { action: 'logout' }
  return { action: 'probe' }
}

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

function persistLastOwnerId(storage: Storage | null, id: number | null): void {
  if (!storage) return
  try {
    if (id === null) storage.removeItem(LAST_OWNER_KEY)
    else storage.setItem(LAST_OWNER_KEY, String(id))
  } catch {
    // Authentication must still resolve when browser storage is unavailable.
  }
}

export function useSessionLifecycle({
  queryClient,
  initialState,
}: {
  queryClient: QueryClient
  initialState?: SessionState
}): {
  session: SessionState
  adopt: (me: MeResponse) => void
  signOut: () => Promise<void>
  reload: () => Promise<void>
} {
  const [session, setSession] = useState<SessionState>(initialState ?? { status: 'loading' })
  // Tracks which account the cached queries belong to, so a switch can be
  // detected without re-running the effect on every unrelated state change.
  const lastUserId = useRef<number | null>(
    initialState?.status === 'authenticated' ? initialState.user.id : null,
  )
  // Kept next to apply so a stale probe cannot resurrect a signed-out session.
  const reloadRequest = useRef(0)

  const applySession = useCallback(
    (next: SessionState, writeOwnerMarker = true) => {
      const storage = getLocalStorage()
      const plan = applySessionPlan({
        next,
        previousId: lastUserId.current,
        storedOwnerId: readLastOwnerId(storage),
        writeOwnerMarker,
      })
      if (plan.clearCache) {
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
      if (plan.clearTenant) clearTenantScopedState(storage)
      if (plan.persistOwner !== undefined) persistLastOwnerId(storage, plan.persistOwner)
      lastUserId.current = plan.nextId
      // A transition ONLY for the challenge, so a screen that suspends on the
      // way in does not blank the one already on the glass. AuthGate lazy-loads
      // the second-factor screens, and sign-in → code is a swap between two
      // painted states of one flow: as an urgent update React commits the
      // Suspense fallback, which is a full-viewport spinner.
      //
      // Narrow on purpose, and the clear() above is the reason. Its safety
      // argument is that the tree unmounts in the SAME commit, leaving no
      // observer alive to refetch into the emptied cache — an argument that
      // holds only while this update is urgent. The challenge is the one status
      // that can never coincide with a clear: it carries no user id, and the
      // state it replaces carries none either, so `lastUserId` does not move
      // and the branch above never runs. Nothing else here suspends — the login
      // and app trees are eager — so nothing else has anything to gain from
      // being deferred.
      if (plan.defer) startTransition(() => setSession(next))
      else setSession(next)
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
        setSession((current) => {
          const fallback = unreachableFallback(current)
          return fallback === 'keep' ? current : fallback
        })
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
      const decision = storageSyncDecision({
        key: event.key,
        newValue: event.newValue,
        currentOwnerId: lastUserId.current,
      })
      if (decision.action === 'ignore') return
      if (decision.action === 'logout') {
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

  return { session, adopt, signOut, reload }
}
