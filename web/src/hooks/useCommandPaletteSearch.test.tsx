import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import { useCommandPaletteSearch, PALETTE_DEBOUNCE_MS } from './useCommandPaletteSearch'
import { makeQueryClient } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import type { ReactNode } from 'react'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

afterEach(() => {
  vi.useRealTimers()
})

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={makeQueryClient()}>{children}</QueryClientProvider>
}

describe('useCommandPaletteSearch', () => {
  it('debounces the query by PALETTE_DEBOUNCE_MS', () => {
    vi.useFakeTimers()
    const { result } = renderHook(() => useCommandPaletteSearch(true), { wrapper })
    act(() => result.current.setQ('fol'))
    expect(result.current.debounced).toBe('')
    act(() => {
      vi.advanceTimersByTime(PALETTE_DEBOUNCE_MS - 1)
    })
    expect(result.current.debounced).toBe('')
    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(result.current.debounced).toBe('fol')
  })

  it('a newer keystroke supersedes the pending debounce', () => {
    vi.useFakeTimers()
    const { result } = renderHook(() => useCommandPaletteSearch(true), { wrapper })
    act(() => result.current.setQ('a'))
    act(() => {
      vi.advanceTimersByTime(150)
    })
    act(() => result.current.setQ('ab'))
    act(() => {
      vi.advanceTimersByTime(100)
    })
    expect(result.current.debounced).toBe('')
    act(() => {
      vi.advanceTimersByTime(100)
    })
    expect(result.current.debounced).toBe('ab')
  })

  it('resets q and debounced when the palette closes', async () => {
    const { result, rerender } = renderHook(
      ({ open }: { open: boolean }) => useCommandPaletteSearch(open),
      { wrapper, initialProps: { open: true } },
    )
    act(() => result.current.setQ('hello'))
    await waitFor(() => expect(result.current.debounced).toBe('hello'))
    rerender({ open: false })
    expect(result.current.q).toBe('')
    expect(result.current.debounced).toBe('')
  })
})
