// Convert a base64url-encoded VAPID public key to the Uint8Array that
// PushManager.subscribe expects. Adapted from the canonical MDN example —
// PushManager doesn't accept the string form, so this small helper is
// required regardless of how the key arrives from the backend.
export function urlBase64ToUint8Array(b64url: string): Uint8Array<ArrayBuffer> {
  // base64url uses `-` / `_` instead of `+` / `/` and drops `=` padding.
  // Restore both for atob().
  const pad = '='.repeat((4 - (b64url.length % 4)) % 4)
  const b64 = (b64url + pad).replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(b64)
  // Allocate an explicit ArrayBuffer (not SharedArrayBuffer) so the result
  // satisfies `BufferSource` on PushManager.subscribe — TS 6's narrowed
  // typing rejects Uint8Array<ArrayBufferLike>.
  const buf = new ArrayBuffer(raw.length)
  const view = new Uint8Array(buf)
  for (let i = 0; i < raw.length; i++) view[i] = raw.charCodeAt(i)
  return view
}

// Single source of truth for "is push usable in this browser?".
// Checks both the navigator and window — Safari's coverage gap means the
// SW exists but PushManager doesn't.
export function isPushSupported(): boolean {
  return typeof navigator !== 'undefined'
    && 'serviceWorker' in navigator
    && typeof window !== 'undefined'
    && 'PushManager' in window
}
