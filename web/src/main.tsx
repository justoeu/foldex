import React from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import App from './App'
import { ConfirmProvider } from './components/ConfirmDialog'
import { PasswordPromptProvider } from './components/PasswordPromptDialog'
import { ErrorBoundary } from './components/ErrorBoundary'
import { AuthProvider } from './auth/AuthProvider'
import { AuthGate } from './auth/AuthGate'
import { useDarkMode } from './hooks/useDarkMode'
import './i18n' // initialises i18next BEFORE any component renders so t() works
import './styles/foldex.css'
import './styles/overrides.css'
// Last, so `.fx-auth` can rely on the tokens the two above declare.
import './styles/auth.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 30_000, refetchOnWindowFocus: false },
  },
})

/**
 * Runs the dark-mode effect ABOVE the gate.
 *
 * Left inside App it would only mount once a session existed, so a dark-mode
 * user would get a white login screen and then a flash on sign-in.
 */
function ThemedGate({ children }: { children: React.ReactNode }) {
  useDarkMode()
  return <AuthGate>{children}</AuthGate>
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ErrorBoundary>
      <QueryClientProvider client={queryClient}>
        {/* AuthProvider sits inside QueryClientProvider because it calls
            queryClient.clear() on an identity change, and outside the dialog
            providers because the auth screens use none of them. */}
        <AuthProvider>
          <ConfirmProvider>
            <PasswordPromptProvider>
              <ThemedGate>
                <App />
              </ThemedGate>
            </PasswordPromptProvider>
          </ConfirmProvider>
        </AuthProvider>
      </QueryClientProvider>
    </ErrorBoundary>
  </React.StrictMode>,
)
