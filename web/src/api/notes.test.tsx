import { describe, it, expect, beforeEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { useCreateNote, useUpdateNote, useDeleteNote, usePinNote, useNote, uploadNoteImage, goNoteHref } from './notes'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { makeQueryClient } from '../test/renderWithProviders'

let state: MockState
let client: ReturnType<typeof makeQueryClient>

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

beforeEach(() => {
  state = freshState()
  client = makeQueryClient()
  installAxiosMock(state)
})

describe('goNoteHref', () => {
  it('prefers the slug', () => {
    expect(goNoteHref({ id: 5, slug: 'my-note' })).toBe('/n/my-note')
  })
  it('falls back to id when slug is empty', () => {
    expect(goNoteHref({ id: 5, slug: '' })).toBe('/n/5')
  })
  it('accepts a bare id', () => {
    expect(goNoteHref(9)).toBe('/n/9')
  })
})

describe('useCreateNote', () => {
  it('creates a note and invalidates entries/tags/folders', async () => {
    const { result } = renderHook(() => useCreateNote(), { wrapper })
    result.current.mutate({ title: 'Recipe', body_html: '<p>flour</p>' })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(state.notes).toHaveLength(1)
    expect(state.notes[0].title).toBe('Recipe')
    expect(state.notes[0].slug).toBe('recipe')
  })
})

describe('useUpdateNote', () => {
  it('patches an existing note', async () => {
    state.notes.push({
      id: 1, title: 'Old', slug: 'old', body_html: '<p>x</p>', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    const { result } = renderHook(() => useUpdateNote(), { wrapper })
    result.current.mutate({ id: 1, body: { pinned: true } })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(state.notes[0].pinned).toBe(true)
  })
})

describe('useDeleteNote', () => {
  it('removes the note', async () => {
    state.notes.push({
      id: 1, title: 'Gone', slug: 'gone', body_html: '', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    const { result } = renderHook(() => useDeleteNote(), { wrapper })
    result.current.mutate(1)
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(state.notes).toHaveLength(0)
  })
})

describe('usePinNote', () => {
  it('optimistically flips pinned in the entries cache', async () => {
    state.notes.push({
      id: 1, title: 'Pin me', slug: 'pin-me', body_html: '', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    const { result } = renderHook(() => usePinNote(), { wrapper })
    result.current.mutate({ id: 1, pinned: true })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(state.notes[0].pinned).toBe(true)
  })
})

describe('useNote', () => {
  it('fetches a single note by id', async () => {
    state.notes.push({
      id: 7, title: 'Fetched', slug: 'fetched', body_html: '<p>y</p>', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    const { result } = renderHook(() => useNote(7), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.data?.title).toBe('Fetched')
  })
  it('stays disabled when id is null', () => {
    const { result } = renderHook(() => useNote(null), { wrapper })
    expect(result.current.fetchStatus).toBe('idle')
  })
})

describe('uploadNoteImage', () => {
  it('posts multipart form data and returns the proxy url', async () => {
    const file = new File(['x'], 'a.png', { type: 'image/png' })
    const res = await uploadNoteImage(file)
    expect(res.url).toMatch(/^\/api\/files\/notes\//)
  })
})
