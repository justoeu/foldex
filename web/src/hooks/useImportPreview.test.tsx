import { describe, it, expect, beforeEach, vi } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useImportPreview } from './useImportPreview'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { makeQueryClient } from '../test/renderWithProviders'
import { http } from '../api/client'
import type { ImportMode, ImportValidation } from '../api/importer'

let state: MockState

const validationFixture = {
  format: 'netscape',
  counts: { links: 5, folders: 2, tags: 1 },
  conflicts: { links: 3, folders: 0, tags: 0 },
  folders: [
    { path: 'Bookmarks Bar', name: 'Bookmarks Bar', count: 2, conflicts: 1 },
    { path: 'Work', name: 'Work', count: 2, conflicts: 1 },
  ],
  ungrouped: { links: 1, conflicts: 1 },
  warnings: [],
} satisfies ImportValidation

beforeEach(() => {
  state = freshState()
  state.importValidation = validationFixture
  installAxiosMock(state)
})

function makeFile() {
  return new File(['<DL></DL>'], 'bookmarks.html', { type: 'text/html' })
}

function wrap() {
  const client = makeQueryClient()
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>
  }
}

describe('ImportPreviewPhaseCharter', () => {
  it.each(['skip', 'duplicate', 'wipe'] as const)(
    '%s × exclude folders posts that mode and the excluded path',
    async (mode: ImportMode) => {
      const file = makeFile()
      const { result } = renderHook(() => useImportPreview(file, 'netscape'), { wrapper: wrap() })
      await waitFor(() => expect(result.current.phase).toBe('ready'))
      expect(result.current.effectiveCounts).toEqual({ links: 5, folders: 2, conflicts: 3 })

      act(() => {
        result.current.setMode(mode)
        result.current.toggle('Work')
      })
      expect(result.current.effectiveCounts).toEqual({ links: 3, folders: 1, conflicts: 2 })
      expect(result.current.phase).toBe('ready')

      await act(async () => {
        await result.current.apply()
      })
      await waitFor(() => expect(result.current.phase).toBe('done'))
      expect(state.lastImportMode).toBe(mode)
      expect(state.lastImportExcluded).toEqual(['Work'])
      expect(result.current.canClose).toBe(false)
    },
  )

  it('abort during validate does not surface cancellation as an error', async () => {
    let signal: AbortSignal | undefined
    vi.mocked(http.post).mockImplementation((_url, _data, config) => {
      signal = config?.signal as AbortSignal | undefined
      return new Promise((_resolve, reject) => {
        signal?.addEventListener('abort', () => {
          reject(Object.assign(new Error('canceled'), { code: 'ERR_CANCELED' }))
        })
      })
    })

    const file = makeFile()
    const { result, unmount } = renderHook(() => useImportPreview(file, 'netscape'), { wrapper: wrap() })
    await waitFor(() => expect(signal).toBeDefined())
    expect(result.current.phase).toBe('loading')

    act(() => {
      result.current.abort()
    })
    expect(signal?.aborted).toBe(true)
    await waitFor(() => expect(result.current.phase).not.toBe('error'))
    expect(result.current.errMsg).toBeNull()
    unmount()
  })

  it('abort during apply cancels the in-flight upload', async () => {
    const file = makeFile()
    const { result, unmount } = renderHook(() => useImportPreview(file, 'netscape'), { wrapper: wrap() })
    await waitFor(() => expect(result.current.phase).toBe('ready'))

    let signal: AbortSignal | undefined
    vi.mocked(http.post).mockImplementation((_url, _data, config) => {
      signal = config?.signal as AbortSignal | undefined
      return new Promise(() => {})
    })

    act(() => {
      void result.current.apply()
    })
    await waitFor(() => expect(result.current.phase).toBe('applying'))
    act(() => {
      result.current.abort()
    })
    expect(signal?.aborted).toBe(true)
    unmount()
  })
})
