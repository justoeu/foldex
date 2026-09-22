import { describe, it, expect, vi, afterEach } from 'vitest'
import { rotateToken } from './tokens'
import { http } from './client'
import type { ApiToken } from './tokens'

afterEach(() => vi.restoreAllMocks())

const active: ApiToken = {
  id: 7,
  name: 'extension',
  scope: 'content',
  created_at: '2026-01-01T00:00:00Z',
}

const rotated: ApiToken = {
  id: 8,
  name: 'extension',
  scope: 'content',
  created_at: '2026-09-22T00:00:00Z',
  token: 'fx_2_secret',
}

describe('rotateToken', () => {
  it('revokes the current token and creates one with the same name', async () => {
    const del = vi.spyOn(http, 'delete').mockResolvedValue({ data: {} } as never)
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: rotated } as never)

    await expect(rotateToken(active)).resolves.toEqual(rotated)
    expect(del).toHaveBeenCalledWith('/api/auth/tokens/7')
    expect(post).toHaveBeenCalledWith('/api/auth/tokens', { name: 'extension' })
  })

  // The pair is not a transaction: when the create half fails the old token
  // is already gone, so the rejection the caller sees must be the CREATE
  // error — that is the half the user can still act on.
  it('surfaces the create error when only the revoke half succeeds', async () => {
    vi.spyOn(http, 'delete').mockResolvedValue({ data: {} } as never)
    const failure = { response: { status: 500, data: { error: { code: 'internal' } } } }
    const post = vi.spyOn(http, 'post').mockRejectedValue(failure)

    await expect(rotateToken(active)).rejects.toBe(failure)
    expect(post).toHaveBeenCalledWith('/api/auth/tokens', { name: 'extension' })
  })

  it('fails fast without creating when the revoke half fails', async () => {
    const failure = { response: { status: 404, data: { error: { code: 'not_found' } } } }
    vi.spyOn(http, 'delete').mockRejectedValue(failure)
    const post = vi.spyOn(http, 'post').mockResolvedValue({ data: rotated } as never)

    await expect(rotateToken(active)).rejects.toBe(failure)
    expect(post).not.toHaveBeenCalled()
  })
})
