import { describe, it, expect, beforeEach } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { useTagPicker } from './TagPicker'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { makeQueryClient } from '../test/renderWithProviders'

let state: MockState

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={makeQueryClient()}>{children}</QueryClientProvider>
}

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

function catalog(count: number) {
  return Array.from({ length: count }, (_, i) => ({
    id: i + 1,
    name: `tag-${i + 1}`,
    color: '#6366f1',
  }))
}

describe('useTagPicker page clamp', () => {
  it('renders the last valid page on the same paint as a shrink, without an effect', async () => {
    state.tags = catalog(15)
    const paints: Array<{ page: number; count: number }> = []
    const { result } = renderHook(() => {
      const picker = useTagPicker(true)
      paints.push({ page: picker.page, count: picker.pageTags.length })
      return picker
    }, { wrapper })

    await waitFor(() => expect(result.current.totalPages).toBe(3))

    await act(() => { result.current.setPage(2) })
    expect(result.current.pageTags.map((tag) => tag.name)).toEqual(['tag-15'])

    paints.length = 0
    await act(() => {
      result.current.setSelected(state.tags.slice(0, 8))
    })

    expect(paints.length).toBeGreaterThan(0)
    for (const paint of paints) {
      expect(paint.page).toBe(0)
      expect(paint.count).toBe(7)
    }
    expect(result.current.page).toBe(0)
    expect(result.current.pageTags).toHaveLength(7)
    expect(result.current.totalPages).toBe(1)
  })
})
