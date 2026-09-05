import { useQuery } from '@tanstack/react-query'
import { fetchPolicy, type InstancePolicy } from '../api/admin'
import { MIN_PASSWORD_LEN } from '../auth/types'

/** The one cache key for the instance policy document. */
export const INSTANCE_POLICY_KEY = ['admin', 'policy'] as const

/** The live floor, never below the compiled-in minimum. */
export function passwordFloor(live?: number | null): number {
  return Math.max(live ?? MIN_PASSWORD_LEN, MIN_PASSWORD_LEN)
}

/**
 * The owner-configurable instance rules (ADR-35), read.
 *
 * The key was literal in three places — the policy form, its own `setQueryData`
 * and the create-user drawer — which is how one of them ends up reading a
 * cache the others no longer write.
 *
 * `retry: false` because the default is three attempts with backoff, and every
 * caller here has a compiled-in fallback that is CORRECT: seven seconds of a
 * dead control with nothing on screen explaining it is worse than falling
 * through immediately. `staleTime` is generous because this document changes
 * approximately never.
 */
export function useInstancePolicy() {
  const query = useQuery({
    queryKey: INSTANCE_POLICY_KEY,
    queryFn: fetchPolicy,
    retry: false,
    staleTime: 5 * 60_000,
  })

  return {
    policy: query.data as InstancePolicy | undefined,
    isPending: query.isPending,
    isError: query.isError,
    /**
     * The password floor, never below the compiled-in minimum. `Math.max` is
     * what makes a policy document that predates a raised constant — or one
     * that failed to load — safe rather than weaker than the code.
     */
    minPasswordLen: passwordFloor(query.data?.password_min_length),
  }
}

/** The password floor every SPA writer of credentials must use. */
export function usePasswordFloor(): number {
  return useInstancePolicy().minPasswordLen
}
