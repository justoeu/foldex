import { createContext, useContext, useMemo, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import type { MeResponse } from '../api/auth'
import { useSessionLifecycle } from './AuthProvider.lifecycle'
import type { Permission, SessionState } from './types'

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

/** Live matrix for this session. Missing /me.permissions (tests) fail closed. */
export function useHasPermission(permission: Permission): boolean {
  const { session } = useAuth()
  if (session.status !== 'authenticated') return false
  return session.permissions?.includes(permission) === true
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
  const queryClient = useQueryClient()
  const { session, adopt, signOut, reload } = useSessionLifecycle({ queryClient, initialState })
  const value = useMemo<AuthContextValue>(
    () => ({ session, adopt, signOut, reload }),
    [session, adopt, signOut, reload],
  )
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
