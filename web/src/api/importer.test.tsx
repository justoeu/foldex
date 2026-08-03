import { describe, it, expect, beforeEach, vi } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useApplyImport, applyImport, validateImport } from './importer'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

describe('importer api', () => {
  it('validateImport posts the file', async () => {
    const file = new File(['<DL></DL>'], 'b.html', { type: 'text/html' })
    const data = await validateImport(file, 'netscape')
    expect(data.counts.links).toBeGreaterThanOrEqual(0)
  })

  it('applyImport posts mode + exclusions', async () => {
    const file = new File(['<DL></DL>'], 'b.html', { type: 'text/html' })
    await applyImport(file, 'netscape', 'skip', ['Work'])
    expect(state.lastImportMode).toBe('skip')
    expect(state.lastImportExcluded).toEqual(['Work'])
  })

  it('useApplyImport invalidates caches on success', async () => {
    const invalidate = vi.fn()
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    })
    client.invalidateQueries = invalidate
    const wrap = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(() => useApplyImport(), { wrapper: wrap })
    await act(async () => {
      await result.current.mutateAsync({
        file: new File(['x'], 'b.html'),
        format: 'netscape',
        mode: 'skip',
        excludeFolders: [],
      })
    })
    await waitFor(() => expect(invalidate).toHaveBeenCalled())
    expect(invalidate.mock.calls.some((c) => c[0]?.queryKey?.[0] === 'links')).toBe(true)
    expect(invalidate.mock.calls.some((c) => c[0]?.queryKey?.[0] === 'stats')).toBe(true)
  })
})
