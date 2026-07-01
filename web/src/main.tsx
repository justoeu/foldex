import React from 'react'
import ReactDOM from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import App from './App'
import { ConfirmProvider } from './components/ConfirmDialog'
import { PasswordPromptProvider } from './components/PasswordPromptDialog'
import { ErrorBoundary } from './components/ErrorBoundary'
import './i18n' // initialises i18next BEFORE any component renders so t() works
import './styles/foldex.css'
import './styles/overrides.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 30_000, refetchOnWindowFocus: false },
  },
})

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ErrorBoundary>
      <QueryClientProvider client={queryClient}>
        <ConfirmProvider>
          <PasswordPromptProvider>
            <App />
          </PasswordPromptProvider>
        </ConfirmProvider>
      </QueryClientProvider>
    </ErrorBoundary>
  </React.StrictMode>,
)
