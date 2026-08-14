import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { GoogleButton } from './GoogleButton'
import { renderWithProviders } from '../../test/renderWithProviders'
import * as auth from '../../api/auth'
import { http } from '../../api/client'

/**
 * Starting the OAuth flow is a full-page navigation, not a fetch: the flow
 * leaves this origin for Google's consent screen and comes back carrying
 * cookies the server sets, so an XHR could neither follow the cross-origin hop
 * nor let the user see what they are agreeing to.
 *
 * The navigation itself is not stubbed — jsdom refuses to let `window.location`
 * be redefined. What IS asserted is the URL that would be navigated to, which
 * is where the only real logic lives.
 */

afterEach(() => vi.restoreAllMocks())

describe('googleStartUrl', () => {
  it('carries the purpose', () => {
    expect(auth.googleStartUrl('login')).toBe('/api/auth/oauth/google/start?purpose=login')
  })

  it('never puts an invitation token in the start URL', () => {
    expect(auth.googleStartUrl('accept_invite', 'tok en/+')).toBe(
      '/api/auth/oauth/google/invite/start',
    )
  })

  it('omits the invite parameter when there is none', () => {
    expect(auth.googleStartUrl('login')).not.toContain('invite')
  })
})

describe('GoogleButton', () => {
  it('starts the flow for its purpose', async () => {
    const start = vi.spyOn(auth, 'startGoogleOAuth').mockResolvedValue()
    renderWithProviders(<GoogleButton purpose="login" />)

    await userEvent.setup().click(screen.getByRole('button', { name: /continue with google/i }))
    expect(start).toHaveBeenCalledWith('login', undefined)
  })

  it('passes the invitation token through', async () => {
    const start = vi.spyOn(auth, 'startGoogleOAuth').mockResolvedValue()
    renderWithProviders(<GoogleButton purpose="accept_invite" invite="tok" />)

    await userEvent.setup().click(screen.getByRole('button'))
    expect(start).toHaveBeenCalledWith('accept_invite', 'tok')
  })

  it('starts invitation OAuth with a body POST', async () => {
    const post = vi.spyOn(http, 'post').mockResolvedValue({
      data: { redirect_url: 'https://accounts.google.test/auth' },
    } as never)

    await auth.startGoogleOAuth('accept_invite', 'tok en/+')

    expect(post).toHaveBeenCalledWith('/api/auth/oauth/google/invite/start', {
      invite: 'tok en/+',
    })
  })

  it('reports a failed invitation start and allows retrying', async () => {
    vi.spyOn(auth, 'startGoogleOAuth').mockRejectedValue(new Error('network down'))
    renderWithProviders(<GoogleButton purpose="accept_invite" invite="tok" />)

    const button = screen.getByRole('button', { name: /continue with google/i })
    await userEvent.setup().click(button)

    expect(await screen.findByRole('alert')).toHaveTextContent(/google sign-in did not complete/i)
    expect(button).toBeEnabled()
  })

  it('accepts a caller-supplied label', () => {
    renderWithProviders(<GoogleButton purpose="link" label="Connect Google" onClick={() => {}} />)
    expect(screen.getByRole('button', { name: 'Connect Google' })).toBeInTheDocument()
  })

  it('never sends linking through the proofless GET navigation', async () => {
    const start = vi.spyOn(auth, 'startGoogleOAuth').mockResolvedValue()
    const stepUp = vi.fn()
    renderWithProviders(<GoogleButton purpose="link" onClick={stepUp} />)

    await userEvent.setup().click(screen.getByRole('button'))
    expect(stepUp).toHaveBeenCalledOnce()
    expect(start).not.toHaveBeenCalled()
  })

  // A strict CSP blocks external hosts outright, so a mark fetched from Google's
  // CDN would silently fail to load — on the one screen where a broken-looking
  // page costs the most trust.
  it('inlines the mark rather than fetching it', () => {
    const { container } = renderWithProviders(<GoogleButton purpose="login" />)
    // eslint-disable-next-line testing-library/no-container, testing-library/no-node-access
    expect(container.querySelector('svg.fx-auth-oauth-mark')).toBeInTheDocument()
    // eslint-disable-next-line testing-library/no-container, testing-library/no-node-access
    expect(container.querySelector('img')).toBeNull()
  })
})
