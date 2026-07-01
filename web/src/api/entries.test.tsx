import { describe, it, expect, beforeEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { useEntries, flattenEntries } from './entries'
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

describe('useEntries', () => {
  it('lists empty by default', async () => {
    const { result } = renderHook(() => useEntries({}), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(flattenEntries(result.current.data)).toEqual([])
  })

  it('interleaves links and notes into one result, each carrying its kind', async () => {
    state.links.push({
      id: 1, url: 'https://a.example', title: 'A link', slug: 'a-link', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '2026-01-01T00:00:00Z', updated_at: '', tags: [],
    } as any)
    state.notes.push({
      id: 2, title: 'A note', slug: 'a-note', body_html: '<p>hi</p>', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '2026-01-01T00:00:00Z', updated_at: '', tags: [],
    })
    const { result } = renderHook(() => useEntries({}), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    const entries = flattenEntries(result.current.data)
    expect(entries).toHaveLength(2)
    const kinds = entries.map((e) => e.kind).sort()
    expect(kinds).toEqual(['link', 'note'])
  })

  it('applies the q filter across both kinds', async () => {
    state.links.push({
      id: 1, url: 'https://a.example', title: 'Alpha link', slug: 'alpha-link', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    } as any)
    state.notes.push({
      id: 2, title: 'Beta note', slug: 'beta-note', body_html: '<p>x</p>', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    const { result } = renderHook(() => useEntries({ q: 'Beta' }), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    const entries = flattenEntries(result.current.data)
    expect(entries).toHaveLength(1)
    expect(entries[0].kind).toBe('note')
  })

  it('scopes by folder_id across both kinds', async () => {
    state.links.push({
      id: 1, url: 'https://a.example', title: 'In folder link', slug: 'l', click_count: 0,
      preview_status: 'ok', pinned: false, folder_id: 9, created_at: '', updated_at: '', tags: [],
    } as any)
    state.notes.push({
      id: 2, title: 'Root note', slug: 'root-note', body_html: '', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    const { result } = renderHook(() => useEntries({ folderId: 9 }), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    const entries = flattenEntries(result.current.data)
    expect(entries).toHaveLength(1)
    expect(entries[0].kind).toBe('link')
  })
})
