import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { getStoredSecret, setStoredSecret, http } from './client'

describe('SHARED_SECRET client helpers', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    localStorage.clear()
    vi.restoreAllMocks()
  })

  it('getStoredSecret returns empty when unset', () => {
    expect(getStoredSecret()).toBe('')
  })

  it('setStoredSecret round-trips and clears', () => {
    setStoredSecret('s3cret')
    expect(getStoredSecret()).toBe('s3cret')
    setStoredSecret('')
    expect(getStoredSecret()).toBe('')
    expect(localStorage.getItem('foldex.secret')).toBeNull()
  })

  it('request interceptor attaches X-Foldex-Secret when stored', async () => {
    setStoredSecret('my-secret')
    const handlers = (http.interceptors.request as unknown as { handlers: Array<{ fulfilled?: (c: any) => any }> }).handlers
    const fulfilled = handlers.find((h) => h?.fulfilled)?.fulfilled
    expect(fulfilled).toBeTypeOf('function')
    const cfg = await fulfilled!({ headers: {} as Record<string, string> })
    expect((cfg.headers as Record<string, string>)['X-Foldex-Secret']).toBe('my-secret')
  })

  it('request interceptor omits header when secret empty', async () => {
    setStoredSecret('')
    const handlers = (http.interceptors.request as unknown as { handlers: Array<{ fulfilled?: (c: any) => any }> }).handlers
    const fulfilled = handlers.find((h) => h?.fulfilled)?.fulfilled
    const cfg = await fulfilled!({ headers: {} as Record<string, string> })
    expect((cfg.headers as Record<string, string>)['X-Foldex-Secret']).toBeUndefined()
  })
})
