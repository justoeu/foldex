import { describe, expect, it } from 'vitest'
import { defaultFeatures, type SessionState } from './types'
import {
  LAST_OWNER_KEY,
  applySessionPlan,
  ownerIdOf,
  storageSyncDecision,
  unreachableFallback,
} from './AuthProvider.lifecycle'
import lifecycleSrc from './AuthProvider.lifecycle.ts?raw'
import { testAdminUser } from '../test/renderWithProviders'

const features = defaultFeatures

const authenticated = (id: number): SessionState => ({
  status: 'authenticated',
  user: { ...testAdminUser, id },
  csrfToken: 'csrf',
  features,
})

const anonymous: SessionState = { status: 'anonymous', features }

const twoFactor: SessionState = {
  status: 'two_factor_required',
  pending: { purpose: 'totp', email: 'a••@b.test', methods: ['totp'], maxAttempts: 5 },
  features,
}

/**
 * CC-DAE-012 — apply/probe/storage-sync live next to the generation counter
 * so a stale /me cannot resurrect a signed-out session (INV-120 / tenant
 * isolation). INV-174: only the 2FA challenge is a deferred update.
 */
describe('AuthProviderLifecycleCharter', () => {
  it('switch: a different owner clears cache and tenant-scoped storage', () => {
    const plan = applySessionPlan({
      next: authenticated(2),
      previousId: 1,
      storedOwnerId: 1,
      writeOwnerMarker: true,
    })
    expect(plan.nextId).toBe(2)
    expect(plan.clearCache).toBe(true)
    expect(plan.clearTenant).toBe(true)
    expect(plan.persistOwner).toBe(2)
    expect(plan.defer).toBe(false)
  })

  it('anonymous: a signed-out result always wipes tenant state and the owner marker', () => {
    const plan = applySessionPlan({
      next: anonymous,
      previousId: 1,
      storedOwnerId: 1,
      writeOwnerMarker: true,
    })
    expect(ownerIdOf(anonymous)).toBeNull()
    expect(plan.clearCache).toBe(true)
    expect(plan.clearTenant).toBe(true)
    expect(plan.persistOwner).toBeNull()
  })

  it('unreachable: a live session is kept; a cold load falls back to anonymous without wiping', () => {
    expect(unreachableFallback(authenticated(1))).toBe('keep')
    expect(unreachableFallback({ status: 'loading' })).toEqual(anonymous)
    expect(unreachableFallback(anonymous)).toEqual(anonymous)
  })

  it('tab-sync: a foreign owner probes; a removed marker logs out; same owner is ignored', () => {
    expect(
      storageSyncDecision({ key: LAST_OWNER_KEY, newValue: '2', currentOwnerId: 1 }),
    ).toEqual({ action: 'probe' })
    expect(
      storageSyncDecision({ key: LAST_OWNER_KEY, newValue: null, currentOwnerId: 1 }),
    ).toEqual({ action: 'logout' })
    expect(
      storageSyncDecision({ key: LAST_OWNER_KEY, newValue: '1', currentOwnerId: 1 }),
    ).toEqual({ action: 'ignore' })
    expect(
      storageSyncDecision({ key: 'foldex.dark', newValue: 'true', currentOwnerId: 1 }),
    ).toEqual({ action: 'ignore' })
    expect(
      storageSyncDecision({ key: LAST_OWNER_KEY, newValue: 'not-an-id', currentOwnerId: 1 }),
    ).toEqual({ action: 'ignore' })
  })

  it('2FA transition: only the challenge is deferred so the glass is not blanked', () => {
    const plan = applySessionPlan({
      next: twoFactor,
      previousId: null,
      storedOwnerId: null,
      writeOwnerMarker: true,
    })
    expect(plan.defer).toBe(true)
    expect(plan.clearCache).toBe(false)
    expect(
      applySessionPlan({
        next: authenticated(1),
        previousId: null,
        storedOwnerId: null,
        writeOwnerMarker: true,
      }).defer,
    ).toBe(false)
  })

  it('does not wrap request === current in isCurrentGeneration', () => {
    expect(lifecycleSrc).not.toMatch(/function isCurrentGeneration/)
  })
})
