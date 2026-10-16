import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { clearTenantScopedState } from './AuthProvider.lifecycle'
import { syncFolderUnlocks, folderUnlockTokenFor } from '../lib/folderUnlockRegistry'

describe('clearTenantScopedState', () => {
  const deleted: string[] = []

  beforeEach(() => {
    deleted.length = 0
    vi.stubGlobal('caches', {
      delete: vi.fn(async (name: string) => {
        deleted.push(name)
        return true
      }),
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('purges the Service Worker file cache and the unlock registry', () => {
    syncFolderUnlocks({ 3: { token: 'tok', expiresAt: Date.now() + 60_000 } })
    const storage = {
      getItem: () => null,
      setItem: () => undefined,
      removeItem: () => undefined,
      clear: () => undefined,
      key: () => null,
      length: 0,
    } as Storage

    clearTenantScopedState(storage)

    expect(deleted).toEqual(['foldex-files-v1'])
    expect(folderUnlockTokenFor(3)).toBeUndefined()
  })

  it('still purges when no storage is available', () => {
    clearTenantScopedState(null as unknown as Storage)
    expect(deleted).toEqual(['foldex-files-v1'])
  })
})
