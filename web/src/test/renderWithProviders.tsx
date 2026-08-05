import { ReactNode } from 'react'
import { render, RenderOptions } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ThemeProvider } from '@mui/material'
import { theme } from '../theme/theme'
import { ConfirmProvider } from '../components/ConfirmDialog'
import { PasswordPromptProvider } from '../components/PasswordPromptDialog'
import { AuthProvider } from '../auth/AuthProvider'
import type { SessionState } from '../auth/types'

export function makeQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: 0 },
      mutations: { retry: false },
    },
  })
}

/**
 * The session every test gets unless it asks for another.
 *
 * An authenticated admin, seeded synchronously. Two reasons it is a default
 * rather than something each test opts into: the existing component tests
 * render deep inside the app and know nothing about auth, so requiring them to
 * set it up would mean editing ~60 files for no behavioural gain; and a seeded
 * state means AuthProvider never fires its /api/auth/me probe, so no test has
 * to mock an endpoint it does not care about.
 *
 * A test that wants the anonymous or first-run path passes `session`
 * explicitly.
 */
export const testAdminUser = {
  id: 1,
  email: 'admin@foldex.test',
  name: 'Test Admin',
  role: 'admin',
  status: 'active',
  has_password: true,
  totp_enabled: false,
  created_at: '2026-01-01T00:00:00Z',
} as const

export const testAdminSession: SessionState = {
  status: 'authenticated',
  user: {
    id: 1,
    email: 'admin@foldex.test',
    name: 'Test Admin',
    role: 'admin',
    status: 'active',
    has_password: true,
    totp_enabled: false,
    created_at: '2026-01-01T00:00:00Z',
  },
  csrfToken: 'test-csrf-token',
  features: { google_oauth: false, two_factor: false, email_delivery: false },
}

export function renderWithProviders(
  ui: ReactNode,
  options: {
    client?: QueryClient
    /** Pass `null` to render the anonymous path (no seeded session). */
    session?: SessionState | null
  } & Omit<RenderOptions, 'wrapper'> = {},
) {
  const { client: clientOpt, session, ...renderOptions } = options
  const client = clientOpt ?? makeQueryClient()
  const initialState = session === null ? undefined : (session ?? testAdminSession)

  const wrapper = ({ children }: { children: ReactNode }) => (
    <ThemeProvider theme={theme}>
      <QueryClientProvider client={client}>
        <AuthProvider initialState={initialState}>
          <ConfirmProvider>
            <PasswordPromptProvider>{children}</PasswordPromptProvider>
          </ConfirmProvider>
        </AuthProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
  return { client, ...render(ui, { wrapper, ...renderOptions }) }
}
