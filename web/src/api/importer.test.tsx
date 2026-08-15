import { describe, it, expect, beforeEach, vi } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useApplyImport, applyImport, validateImport } from './importer'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { http } from './client'

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

  it('passes an AbortSignal to the validation upload', async () => {
    const controller = new AbortController()
    const post = vi.spyOn((await import('./client')).http, 'post')
    const file = new File(['<DL></DL>'], 'b.html', { type: 'text/html' })

    await validateImport(file, 'netscape', controller.signal)

    expect(post).toHaveBeenCalledWith(
      '/api/import/validate',
      expect.any(FormData),
      expect.objectContaining({ signal: controller.signal }),
    )
  })

  it('applyImport posts mode + exclusions', async () => {
    const file = new File(['<DL></DL>'], 'b.html', { type: 'text/html' })
    await applyImport(file, 'netscape', 'skip', ['Work'])
    expect(state.lastImportMode).toBe('skip')
    expect(state.lastImportExcluded).toEqual(['Work'])
  })

  it('passes an AbortSignal to the apply upload', async () => {
    const controller = new AbortController()
    const post = vi.spyOn(http, 'post')
    const file = new File(['<DL></DL>'], 'b.html', { type: 'text/html' })

    await applyImport(file, 'netscape', 'skip', [], controller.signal)

    expect(post).toHaveBeenCalledWith(
      '/api/import/apply',
      expect.any(FormData),
      expect.objectContaining({ signal: controller.signal }),
    )
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
    expect(invalidate.mock.calls.map((call) => call[0]?.queryKey?.[0])).toEqual([
      'links', 'entries', 'folders', 'tags', 'stats',
    ])
  })

  it('useApplyImport invalidates caches after a failed mutation', async () => {
    const invalidate = vi.fn()
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    })
    client.invalidateQueries = invalidate
    const wrap = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const { http } = await import('./client')
    vi.mocked(http.post).mockRejectedValueOnce(new Error('apply failed'))
    const { result } = renderHook(() => useApplyImport(), { wrapper: wrap })

    await act(async () => {
      await expect(result.current.mutateAsync({
        file: new File(['x'], 'b.html'),
        format: 'netscape',
        mode: 'skip',
        excludeFolders: [],
      })).rejects.toThrow('apply failed')
    })

    expect(invalidate.mock.calls.map((call) => call[0]?.queryKey?.[0])).toEqual([
      'links', 'entries', 'folders', 'tags', 'stats',
    ])
  })
})
