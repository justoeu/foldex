import { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { http } from '../api/client'
import { invalidateEntryCounts } from '../api/entries'
import { makeQueryClient } from '../test/renderWithProviders'
import { useEntryCounts } from './useEntryCounts'

beforeEach(() => {
  vi.restoreAllMocks()
})

describe('useEntryCounts', () => {
  it('loads authoritative link and note counts from the global endpoint', async () => {
    vi.spyOn(http, 'get').mockResolvedValue({ data: { links: 523, notes: 17 } } as never)
    const client = makeQueryClient()
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )

    const { result } = renderHook(() => useEntryCounts(), { wrapper })

    await waitFor(() => expect(result.current.data).toEqual({ links: 523, notes: 17 }))
    expect(http.get).toHaveBeenCalledWith('/api/entries/counts')
  })

  it('does not refetch for entry invalidations that cannot change cardinality', async () => {
    let counts = { links: 1, notes: 2 }
    vi.spyOn(http, 'get').mockImplementation(async () => ({ data: counts }) as never)
    const client = makeQueryClient()
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(() => useEntryCounts(), { wrapper })
    await waitFor(() => expect(result.current.data).toEqual({ links: 1, notes: 2 }))
    client.setQueryData(['entries', 'active-scope'], { pages: [[]], pageParams: [0] })

    counts = { links: 2, notes: 2 }
    await client.invalidateQueries({ queryKey: ['entries'] })

    expect(result.current.data).toEqual({ links: 1, notes: 2 })
    expect(http.get).toHaveBeenCalledTimes(1)

    await invalidateEntryCounts(client)
    await waitFor(() => expect(result.current.data).toEqual({ links: 2, notes: 2 }))
    expect(http.get).toHaveBeenCalledTimes(2)
  })
})
