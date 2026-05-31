import { describe, expect, it } from 'vitest'
import { urlBase64ToUint8Array, isPushSupported } from './push'

describe('urlBase64ToUint8Array', () => {
  it('decodes standard base64url (no padding)', () => {
    // "AQID" → bytes [1, 2, 3] (this is also valid plain base64)
    const out = urlBase64ToUint8Array('AQID')
    expect(Array.from(out)).toEqual([1, 2, 3])
  })

  it('decodes base64url-specific characters', () => {
    // VAPID-shaped keys use `-` / `_` instead of `+` / `/`.
    // Decode "_-_-" (4 chars → 3 bytes). Standard base64 equivalent: "/+/+".
    const out = urlBase64ToUint8Array('_-_-')
    expect(out.length).toBe(3)
  })

  it('restores padding when the input length is not a multiple of 4', () => {
    // "AQ" requires "==" padding → 1 byte
    const out = urlBase64ToUint8Array('AQ')
    expect(Array.from(out)).toEqual([1])
  })

  it('returns a Uint8Array backed by a real ArrayBuffer (TS 6 typing)', () => {
    const out = urlBase64ToUint8Array('AQID')
    // Without an explicit ArrayBuffer allocation the Uint8Array would be
    // typed as Uint8Array<ArrayBufferLike>, which PushManager.subscribe
    // rejects under TS 6.
    expect(out.buffer).toBeInstanceOf(ArrayBuffer)
  })
})

describe('isPushSupported', () => {
  it('returns false when navigator.serviceWorker is missing', () => {
    // jsdom doesn't ship `serviceWorker` on navigator, so this should
    // default to false in the test environment — covers the unsupported
    // branch end-to-end.
    expect(isPushSupported()).toBe(false)
  })
})
