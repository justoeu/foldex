import { describe, it, expect } from 'vitest'
import { screen } from '@testing-library/react'
import { AuthShell } from './AuthShell'
import { renderWithProviders, testAdminSession } from '../../test/renderWithProviders'
import { VERSION } from '../../version'

const features = (testAdminSession as { features: object }).features
const anonymous = { status: 'anonymous', features } as never

function render(extra?: { footer?: React.ReactNode }) {
  return renderWithProviders(
    <AuthShell kicker="access your library" title="Welcome back" subtitle="Sign in to reach your links." {...extra}>
      <form>form</form>
    </AuthShell>,
    { session: anonymous },
  )
}

describe('AuthShell', () => {
  it('puts the brand, the heading and the build stamp on the signed-out screen', () => {
    render()

    expect(screen.getByRole('heading', { name: /welcome back/i })).toBeInTheDocument()
    expect(screen.getByText('foldex')).toBeInTheDocument()
    expect(screen.getByText(/personal · self-hosted/i)).toBeInTheDocument()
    // The one surface reachable without an account has to answer "which
    // version is this instance running?".
    expect(screen.getByText(new RegExp(`foldex v${VERSION.replace(/\./g, '\\.')}`))).toBeInTheDocument()
  })

  /*
   * The promo pane is decoration: three sentences and a mock window between the
   * heading and the e-mail field is exactly what a screen-reader user does not
   * need while trying to sign in. `aria-hidden` on the aside is the contract,
   * so none of its copy may be reachable by an accessible query.
   */
  it('keeps the promo pane out of the accessibility tree', () => {
    render()

    // Role queries are the accessibility tree: `aria-hidden` removes the pane
    // from them while `queryByText` would still walk the raw DOM and find it.
    expect(screen.queryByRole('heading', { name: /finally in one place/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('list')).not.toBeInTheDocument()
    expect(document.querySelector('.fx-auth-marketing')).toHaveAttribute('aria-hidden', 'true')
    // Still rendered — the pane is hidden from assistive tech, not dropped.
    expect(document.querySelectorAll('.fx-auth-preview-card')).toHaveLength(4)
  })

  it('renders a footer only when one is given', () => {
    const { unmount } = render()
    expect(document.querySelector('.fx-auth-footer')).toBeNull()
    unmount()

    render({ footer: <span>need an invite?</span> })
    expect(screen.getByText('need an invite?')).toBeInTheDocument()
  })
})
