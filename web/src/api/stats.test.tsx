import { QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { makeQueryClient } from '../test/renderWithProviders'
import { http } from './client'
import { useStatsDashboard } from './stats'

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={makeQueryClient()}>{children}</QueryClientProvider>
}

beforeEach(() => {
  vi.spyOn(http, 'get').mockResolvedValue({
    data: {
      summary: {
        total_links: 1,
        total_tags: 0,
        total_clicks: 2,
        clicks_last_30d: 2,
        clicks_prev_30d: 0,
        new_links_last_30d: 1,
        top_host: 'example.com',
        top_host_clicks: 2,
      },
      daily: [],
      top: [],
      tags: [],
    },
  } as never)
})

describe('useStatsDashboard', () => {
  it('loads all dashboard sections in one request', async () => {
    const { result } = renderHook(() => useStatsDashboard(60, 5), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))

    expect(http.get).toHaveBeenCalledTimes(1)
    expect(http.get).toHaveBeenCalledWith('/api/stats/dashboard?days=60&limit=5')
    expect(result.current.data?.summary.total_clicks).toBe(2)
  })
})
