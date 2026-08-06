/**
 * Tokens that arrive in the URL from an e-mail link.
 *
 * They are read and stripped at MODULE SCOPE — before React renders anything —
 * and that timing is the whole point. Doing it in an effect would break under
 * React 19's StrictMode, which invokes effects twice in development: the first
 * pass would clean the URL and the second would find nothing, so the invite
 * screen would flash and then vanish. Reading once at import time gives every
 * consumer the same answer for the lifetime of the page.
 *
 * The token is removed from the address bar immediately so it does not end up
 * in browser history, in a screenshot, or in the Referer header of the next
 * outbound request — an invite token is a credential.
 */
export type UrlTokens = {
  invite?: string
  reset?: string
  verify?: string
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
  const params = url.searchParams
  const tokens: UrlTokens = {
    invite: params.get('invite') ?? undefined,
    reset: params.get('reset') ?? undefined,
    verify: params.get('verify') ?? undefined,
    oauth: params.get('oauth') ?? undefined,
    oauthError: params.get('oauth_error') ?? undefined,
  }

  const consumed = ['invite', 'reset', 'verify', 'oauth', 'oauth_error']
  const hadAny = consumed.some((k) => params.has(k))
  if (hadAny) {
    consumed.forEach((k) => params.delete(k))
    // replaceState, not pushState: the URL carrying the token must not remain
    // reachable with the Back button.
    window.history.replaceState({}, '', url.pathname + (params.toString() ? `?${params}` : '') + url.hash)
  }
  return tokens
}

export const urlTokens: UrlTokens = readAndStrip()
