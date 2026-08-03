import { describe, it, expect, beforeEach, vi } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useTags, useCreateTag, useUpdateTag, useDeleteTag } from './tags'
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

function keysFrom(invalidate: ReturnType<typeof vi.fn>): string[] {
  return invalidate.mock.calls.map((c) => c[0]?.queryKey?.[0]).filter(Boolean)
}

describe('tags hooks', () => {
  it('lists empty by default', async () => {
    const { result } = renderHook(() => useTags(), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.data).toEqual([])
  })

  it('creates a tag', async () => {
    const { result } = renderHook(() => useCreateTag(), { wrapper })
    const t = await result.current.mutateAsync({ name: 'docs', color: '#a78bfa' })
    expect(t.id).toBe(1)
    expect(t.name).toBe('docs')
  })

  it('updates a tag', async () => {
    state.tags.push({ id: 5, name: 'old', color: '#000', icon: null })
    const { result } = renderHook(() => useUpdateTag(), { wrapper })
    const updated = await result.current.mutateAsync({ id: 5, body: { name: 'new' } })
    expect(updated.name).toBe('new')
  })

  it('deletes a tag', async () => {
    state.tags.push({ id: 9, name: 'gone', color: '#000', icon: null })
    const { result } = renderHook(() => useDeleteTag(), { wrapper })
    await result.current.mutateAsync(9)
    expect(state.tags).toHaveLength(0)
  })

  it('useUpdateTag invalidates tags, links, and entries', async () => {
    state.tags.push({ id: 5, name: 'old', color: '#000', icon: null })
    const invalidate = vi.fn()
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    })
    client.invalidateQueries = invalidate
    const wrap = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(() => useUpdateTag(), { wrapper: wrap })
    await act(async () => {
      await result.current.mutateAsync({ id: 5, body: { name: 'new' } })
    })
    await waitFor(() => expect(invalidate).toHaveBeenCalled())
    const keys = keysFrom(invalidate)
    expect(keys).toEqual(expect.arrayContaining(['tags', 'links', 'entries']))
  })

  it('useDeleteTag invalidates tags, links, and entries', async () => {
    state.tags.push({ id: 9, name: 'gone', color: '#000', icon: null })
    const invalidate = vi.fn()
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    })
    client.invalidateQueries = invalidate
    const wrap = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(() => useDeleteTag(), { wrapper: wrap })
    await act(async () => {
      await result.current.mutateAsync(9)
    })
    await waitFor(() => expect(invalidate).toHaveBeenCalled())
    const keys = keysFrom(invalidate)
    expect(keys).toEqual(expect.arrayContaining(['tags', 'links', 'entries']))
  })
})
