import { describe, expect, it } from 'vitest'
import {
  isFolderLockedError,
  pruneFolderContextMap,
  pruneFolderPath,
  pruneFolderUnlocks,
  validUnlockToken,
} from './AppNavigation'

describe('App navigation contracts', () => {
  it('accepts only unexpired unlock tokens', () => {
    expect(validUnlockToken({ token: 'live', expiresAt: 2_000 }, 1_000)).toBe('live')
    expect(validUnlockToken({ token: 'expired', expiresAt: 1_000 }, 1_000)).toBeUndefined()
    expect(validUnlockToken(undefined, 1_000)).toBeUndefined()
  })

  it('trims navigation at the first deleted ancestor', () => {
    const path = [1, 2, 3]
    expect(pruneFolderPath(path, new Set([1, 3]))).toEqual([1])
    expect(pruneFolderPath(path, new Set(path))).toBe(path)
  })

  it('prunes the same orphan folder keys from each contextual preference map', () => {
    const map = {
      home: 'cards',
      'folder.1': 'list',
      'folder.2': 'compact',
      future: 'cards',
    }

    expect(pruneFolderContextMap(map, new Set([2]))).toEqual({
      home: 'cards',
      'folder.2': 'compact',
      future: 'cards',
    })
    expect(pruneFolderContextMap(map, new Set([1, 2]))).toBe(map)
  })

  it('prunes unlock proofs for folders that no longer exist', () => {
    const unlocks = {
      1: { token: 'deleted', expiresAt: 2_000 },
      2: { token: 'kept', expiresAt: 2_000 },
    }

    expect(pruneFolderUnlocks(unlocks, new Set([2]))).toEqual({
      2: { token: 'kept', expiresAt: 2_000 },
    })
    expect(pruneFolderUnlocks(unlocks, new Set([1, 2]))).toBe(unlocks)
  })

  it('classifies only the exact folder_locked 403 contract as recoverable', () => {
    expect(isFolderLockedError({
      response: { status: 403, data: { error: { code: 'folder_locked' } } },
    })).toBe(true)
    expect(isFolderLockedError({
      response: { status: 401, data: { error: { code: 'folder_locked' } } },
    })).toBe(false)
    expect(isFolderLockedError({
      response: { status: 403, data: { error: { code: 'token_scope' } } },
    })).toBe(false)
  })
})
