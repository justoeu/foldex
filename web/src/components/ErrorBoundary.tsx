import { Component, type ErrorInfo, type ReactNode } from 'react'

type Props = { children: ReactNode }
type State = { error: Error | null }

// ErrorBoundary catches uncaught render-time exceptions anywhere below it and
// renders a recoverable fallback instead of leaving the user with a blank
// page. Mounted at the root in main.tsx so any view crash stays contained.
//
// Reset is reload-the-page rather than try-again because most crashes here
// imply stale client state (e.g. a query cache pointing at a removed entity).
// A full reload is the cheapest correct recovery for a single-user app.
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // eslint-disable-next-line no-console
    console.error('foldex: uncaught render error', error, info.componentStack)
  }

  private handleReload = () => {
    window.location.reload()
  }

  render() {
    if (this.state.error) {
      return (
        <div
          role="alert"
          style={{
            minHeight: '100vh',
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'center',
            justifyContent: 'center',
            padding: 32,
            background: 'var(--fx-bg, #0b0b0f)',
            color: 'var(--fx-ink, #e6e6f0)',
            fontFamily: 'var(--fx-mono, ui-monospace, monospace)',
            gap: 18,
            textAlign: 'center',
          }}
        >
          <div style={{ fontSize: 14, color: 'var(--fx-ink-3, #9ca0b8)', textTransform: 'uppercase', letterSpacing: '0.18em' }}>
            ⚠ algo deu errado
          </div>
          <h1 style={{ fontFamily: 'var(--fx-display, system-ui, sans-serif)', fontSize: 28, margin: 0 }}>
            Foldex bateu num erro inesperado
          </h1>
          <p style={{ fontSize: 14, color: 'var(--fx-ink-4, #6b6e85)', maxWidth: 520 }}>
            A view atual quebrou ao renderizar. Recarregar a página resolve quase sempre — os dados estão salvos no servidor.
          </p>
          <pre
            style={{
              maxWidth: 720,
              overflow: 'auto',
              padding: 12,
              borderRadius: 8,
              background: 'rgba(255, 255, 255, 0.04)',
              fontSize: 12,
              color: 'var(--fx-ink-3, #9ca0b8)',
              textAlign: 'left',
            }}
          >
            {this.state.error.message}
          </pre>
          <button
            onClick={this.handleReload}
            style={{
              padding: '10px 18px',
              borderRadius: 10,
              border: 0,
              background: 'linear-gradient(180deg, #8B85FF, #6366F1)',
              color: 'white',
              fontWeight: 700,
              cursor: 'pointer',
            }}
          >
            Recarregar
          </button>
        </div>
      )
    }
    return this.props.children
  }
}
