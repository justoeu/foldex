import { describe, it, expect, beforeEach } from 'vitest'
import { syncFolderUnlocks, folderUnlockTokenFor, clearFolderUnlocks } from './folderUnlockRegistry'

beforeEach(() => clearFolderUnlocks())

describe('folderUnlockRegistry', () => {
  it('returns the token for an unlocked folder and nothing for others', () => {
    syncFolderUnlocks({ 7: { token: 'tok-7', expiresAt: Date.now() + 60_000 } })
    expect(folderUnlockTokenFor(7)).toBe('tok-7')
    expect(folderUnlockTokenFor(8)).toBeUndefined()
    expect(folderUnlockTokenFor(null)).toBeUndefined()
    expect(folderUnlockTokenFor(undefined)).toBeUndefined()
  })

  it('does not return an expired token', () => {
    syncFolderUnlocks({ 7: { token: 'tok-7', expiresAt: 1_000 } })
    expect(folderUnlockTokenFor(7, 2_000)).toBeUndefined()
  })

  it('sync replaces the whole map and clear empties it', () => {
    syncFolderUnlocks({ 7: { token: 'tok-7', expiresAt: Date.now() + 60_000 } })
    syncFolderUnlocks({ 9: { token: 'tok-9', expiresAt: Date.now() + 60_000 } })
    expect(folderUnlockTokenFor(7)).toBeUndefined()
    expect(folderUnlockTokenFor(9)).toBe('tok-9')
    clearFolderUnlocks()
    expect(folderUnlockTokenFor(9)).toBeUndefined()
  })
})
