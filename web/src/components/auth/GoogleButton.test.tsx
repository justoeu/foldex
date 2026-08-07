import { describe, it, expect, vi, afterEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { GoogleButton } from './GoogleButton'
import { renderWithProviders } from '../../test/renderWithProviders'
import * as auth from '../../api/auth'

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

  // An invitation token is a credential and may contain characters that would
  // otherwise terminate the query string.
  it('encodes an invitation token', () => {
    expect(auth.googleStartUrl('accept_invite', 'tok en/+')).toBe(
      '/api/auth/oauth/google/start?purpose=accept_invite&invite=tok+en%2F%2B',
    )
  })

  it('omits the invite parameter when there is none', () => {
    expect(auth.googleStartUrl('link')).not.toContain('invite')
  })
})

describe('GoogleButton', () => {
  it('starts the flow for its purpose', async () => {
    const start = vi.spyOn(auth, 'startGoogleOAuth').mockImplementation(() => {})
    renderWithProviders(<GoogleButton purpose="login" />)

    await userEvent.setup().click(screen.getByRole('button', { name: /continue with google/i }))
    expect(start).toHaveBeenCalledWith('login', undefined)
  })

  it('passes the invitation token through', async () => {
    const start = vi.spyOn(auth, 'startGoogleOAuth').mockImplementation(() => {})
    renderWithProviders(<GoogleButton purpose="accept_invite" invite="tok" />)

    await userEvent.setup().click(screen.getByRole('button'))
    expect(start).toHaveBeenCalledWith('accept_invite', 'tok')
  })

  it('accepts a caller-supplied label', () => {
    renderWithProviders(<GoogleButton purpose="link" label="Connect Google" />)
    expect(screen.getByRole('button', { name: 'Connect Google' })).toBeInTheDocument()
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
