import { ReactNode } from 'react'
import { render, RenderOptions } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ThemeProvider } from '@mui/material'
import { theme } from '../theme/theme'
import { ConfirmProvider } from '../components/ConfirmDialog'
import { PasswordPromptProvider } from '../components/PasswordPromptDialog'

export function makeQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: 0 },
      mutations: { retry: false },
    },
  })
}

export function renderWithProviders(
  ui: ReactNode,
  options: { client?: QueryClient } & Omit<RenderOptions, 'wrapper'> = {},
) {
  const client = options.client ?? makeQueryClient()
  const wrapper = ({ children }: { children: ReactNode }) => (
    <ThemeProvider theme={theme}>
      <QueryClientProvider client={client}>
        <ConfirmProvider>
          <PasswordPromptProvider>{children}</PasswordPromptProvider>
        </ConfirmProvider>
      </QueryClientProvider>
    </ThemeProvider>
  )
  return { client, ...render(ui, { wrapper, ...options }) }
}
