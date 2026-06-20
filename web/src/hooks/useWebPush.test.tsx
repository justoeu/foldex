import { describe, it, expect } from 'vitest'
import { isPushSupported, urlBase64ToUint8Array } from '../lib/push'

describe('push utilities', () => {
  describe('urlBase64ToUint8Array', () => {
    it('returns a Uint8Array', () => {
      // A valid unpadded base64url string (43 chars → 32 bytes)
      const key = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
      const result = urlBase64ToUint8Array(key)
      expect(result).toBeInstanceOf(Uint8Array)
      expect(result.length).toBe(32)
    })

    it('handles padding correctly', () => {
      // Shorter key that needs padding
      const key = 'AQIDBAUG'
      const result = urlBase64ToUint8Array(key)
      expect(result).toBeInstanceOf(Uint8Array)
    })

    it('returns empty Uint8Array for empty string', () => {
      const result = urlBase64ToUint8Array('')
      expect(result).toBeInstanceOf(Uint8Array)
      expect(result.length).toBe(0)
    })
  })

  describe('isPushSupported', () => {
    it('returns a boolean', () => {
      // In jsdom, serviceWorker and PushManager are usually absent
      const result = isPushSupported()
      expect(typeof result).toBe('boolean')
    })
  })
})
