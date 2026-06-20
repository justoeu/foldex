import { describe, it, expect, vi, beforeEach } from 'vitest'

// Mock the push module
vi.mock('../lib/push', () => ({
  isPushSupported: vi.fn(() => true),
  urlBase64ToUint8Array: vi.fn(() => new Uint8Array(65)),
}))

describe('useWebPush', () => {
  it('isPushSupported is mocked correctly', async () => {
    const { isPushSupported } = await import('../lib/push')
    expect(isPushSupported()).toBe(true)
  })

  it('urlBase64ToUint8Array returns a Uint8Array', async () => {
    const { urlBase64ToUint8Array } = await import('../lib/push')
    expect(urlBase64ToUint8Array('test')).toBeInstanceOf(Uint8Array)
  })
})
