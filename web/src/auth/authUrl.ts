/**
 * Tokens that arrive in the fragment of an e-mail link.
 *
 * They are read and stripped at MODULE SCOPE — before React renders anything —
 * and that timing is the whole point. Doing it in an effect would break under
 * React 19's StrictMode, which invokes effects twice in development: the first
 * pass would clean the URL and the second would find nothing, so the invite
 * screen would flash and then vanish. Reading once at import time gives every
 * consumer the same answer for the lifetime of the page.
 *
 * Fragments never reach the initial HTTP request or nginx access log. The token
 * is also removed from the address bar immediately so it does not remain in
 * browser history or screenshots.
 */
export type UrlTokens = {
  invite?: string
  reset?: string
  verify?: string
  /** The confirmation that MOVES the account to a new address. It arrives in
   *  the mailbox being moved to, so the device following it very often has no
   *  session at all. */
  emailChange?: string
  /**
   * The OAuth callback's outcome. It is a marker, not state: the actual result
   * — a session, or a pending challenge — lives in cookies, and the SPA
   * discovers it by calling /me on boot. This only decides what to SAY, e.g.
   * confirming that a Google account was linked.
   */
  oauth?: string
  oauthError?: string
}

function readAndStrip(): UrlTokens {
  if (typeof window === 'undefined') return {}

  const url = new URL(window.location.href)
  const query = url.searchParams
  const fragment = new URLSearchParams(url.hash.startsWith('#') ? url.hash.slice(1) : '')
  const tokens: UrlTokens = {
    invite: fragment.get('invite') ?? undefined,
    reset: fragment.get('reset') ?? undefined,
    verify: fragment.get('verify') ?? undefined,
    emailChange: fragment.get('email-change') ?? undefined,
    oauth: query.get('oauth') ?? undefined,
    oauthError: query.get('oauth_error') ?? undefined,
  }

  const credentialKeys = ['invite', 'reset', 'verify', 'email-change']
  const markerKeys = ['oauth', 'oauth_error']
  const hadFragmentCredential = credentialKeys.some((k) => fragment.has(k))
  const hadAny =
    hadFragmentCredential ||
    credentialKeys.some((k) => query.has(k)) ||
    markerKeys.some((k) => query.has(k))
  if (hadAny) {
    credentialKeys.forEach((k) => {
      fragment.delete(k)
      // Query credentials are never consumed, but remove stale links so they
      // do not leak again after the already-exposed initial request.
      query.delete(k)
    })
    markerKeys.forEach((k) => query.delete(k))
    if (hadFragmentCredential) {
      const remainingFragment = fragment.toString()
      url.hash = remainingFragment ? `#${remainingFragment}` : ''
    }
    // replaceState, not pushState: the URL carrying the token must not remain
    // reachable with the Back button.
    window.history.replaceState({}, '', url.pathname + (query.toString() ? `?${query}` : '') + url.hash)
  }
  return tokens
}

export const urlTokens: UrlTokens = readAndStrip()
