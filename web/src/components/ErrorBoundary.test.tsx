import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { I18nextProvider } from 'react-i18next'
import i18n from '../i18n'
import { ErrorBoundary } from './ErrorBoundary'

function Boom(): never {
  throw new Error('intentional test failure')
}

describe('ErrorBoundary', () => {
  it('renders children when nothing throws', () => {
    render(
      <I18nextProvider i18n={i18n}>
        <ErrorBoundary>
          <span data-testid="ok">happy path</span>
        </ErrorBoundary>
      </I18nextProvider>,
    )
    expect(screen.getByTestId('ok')).toBeInTheDocument()
  })

  it('renders the fallback when a child throws', () => {
    // Silence React's console.error noise from the intentional throw.
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    render(
      <I18nextProvider i18n={i18n}>
        <ErrorBoundary>
          <Boom />
        </ErrorBoundary>
      </I18nextProvider>,
    )
    // Fallback uses role="alert" — the only stable selector that survives
    // i18n key churn.
    expect(screen.getByRole('alert')).toBeInTheDocument()
    spy.mockRestore()
  })
})
