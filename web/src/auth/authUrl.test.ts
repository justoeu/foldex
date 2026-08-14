import { describe, it, expect, beforeEach, vi } from 'vitest'

/**
 * authUrl reads and strips credentials from the fragment at MODULE scope, so every case has
 * to set the URL and then import a FRESH copy of the module. That is also the
 * behaviour under test: the read must happen exactly once, at import time, and
 * not in an effect — React 19's StrictMode invokes effects twice in
 * development, so an effect-based version would clean the URL on the first pass
 * and find nothing on the second, making the invite screen flash and vanish.
 */
async function importFresh(url: string) {
  window.history.replaceState({}, '', url)
  vi.resetModules()
  return import('./authUrl')
}

describe('authUrl', () => {
  beforeEach(() => {
    window.history.replaceState({}, '', '/')
  })

  it('reads every recognised token', async () => {
    const { urlTokens } = await importFresh('/?oauth_error=denied#invite=INV&reset=RST&verify=VER')
    expect(urlTokens).toEqual({
      invite: 'INV',
      reset: 'RST',
      verify: 'VER',
      oauth: undefined,
      oauthError: 'denied',
    })
  })

  // The token IS a credential. Leaving it in the address bar puts it in browser
  // history, in any screenshot, and in the Referer header of the next outbound
  // request.
  it('strips the token from the address bar', async () => {
    await importFresh('/#invite=SECRET')
    expect(window.location.search).not.toContain('SECRET')
    expect(window.location.search).toBe('')
    expect(window.location.hash).toBe('')
  })

  it('preserves unrelated query and fragment parameters', async () => {
    await importFresh('/?view=grid#invite=SECRET&keep=1&other=2')
    expect(window.location.search).not.toContain('SECRET')
    expect(window.location.search).toBe('?view=grid')
    expect(window.location.hash).toContain('keep=1')
    expect(window.location.hash).toContain('other=2')
  })

  it('leaves the URL alone when there is no token', async () => {
    await importFresh('/?view=grid')
    expect(window.location.search).toBe('?view=grid')
  })

  it('reports undefined for absent tokens', async () => {
    const { urlTokens } = await importFresh('/')
    expect(urlTokens.invite).toBeUndefined()
    expect(urlTokens.reset).toBeUndefined()
  })

  // replaceState, not pushState: the URL carrying the token must not stay
  // reachable with the Back button.
  it('replaces rather than pushes history', async () => {
    const push = vi.spyOn(window.history, 'pushState')
    const replace = vi.spyOn(window.history, 'replaceState')
    await importFresh('/#invite=SECRET')
    expect(push).not.toHaveBeenCalled()
    expect(replace).toHaveBeenCalled()
    push.mockRestore()
    replace.mockRestore()
  })

  it('does not accept credentials from the query string', async () => {
    const { urlTokens } = await importFresh('/?invite=SECRET#section')
    expect(urlTokens.invite).toBeUndefined()
    expect(window.location.search).toBe('')
    expect(window.location.hash).toBe('#section')
  })
})
