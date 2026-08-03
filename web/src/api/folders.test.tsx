import { describe, it, expect, beforeEach, vi } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  FOLDER_UNLOCK_HEADER,
  useCreateFolder,
  useDeleteFolder,
  useFolders,
  useUnlockFolder,
  useUpdateFolder,
} from './folders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { makeQueryClient } from '../test/renderWithProviders'
import { http } from './client'

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

describe('useFolders', () => {
  it('lists folders and supports parent_id scope', async () => {
    state.folders.push(
      { id: 1, name: 'Root', color: '#abc', parent_id: null, has_password: false, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '' },
      { id: 2, name: 'Child', color: '#def', parent_id: 1, has_password: false, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '' },
    )
    const { result } = renderHook(() => useFolders({ scope: 1 }), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.data?.map((f) => f.name)).toEqual(['Child'])
  })

  it('sends unlock header when token provided', async () => {
    const spy = vi.spyOn(http, 'get')
    state.folders.push(
      { id: 5, name: 'Locked-child', color: '#abc', parent_id: 4, has_password: false, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '' },
    )
    const { result } = renderHook(() => useFolders({ scope: 4, unlockToken: 'tok-xyz' }), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(spy).toHaveBeenCalled()
    const call = spy.mock.calls.find((c) => String(c[0]).includes('/api/folders'))
    expect(call?.[1]?.headers?.[FOLDER_UNLOCK_HEADER]).toBe('tok-xyz')
  })
})

describe('folder CRUD mutations', () => {
  it('create posts body', async () => {
    const { result } = renderHook(() => useCreateFolder(), { wrapper })
    await act(async () => {
      const f = await result.current.mutateAsync({ name: 'New', color: '#6366F1' })
      expect(f.name).toBe('New')
    })
    expect(state.folders.some((f) => f.name === 'New')).toBe(true)
  })

  it('update patches by id', async () => {
    state.folders.push(
      { id: 9, name: 'Old', color: '#abc', parent_id: null, has_password: false, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '' },
    )
    const { result } = renderHook(() => useUpdateFolder(), { wrapper })
    await act(async () => {
      const f = await result.current.mutateAsync({ id: 9, body: { name: 'Renamed' } })
      expect(f.name).toBe('Renamed')
    })
  })

  it('delete hits cascade query when requested', async () => {
    state.folders.push(
      { id: 3, name: 'Gone', color: '#abc', parent_id: null, has_password: false, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '' },
    )
    const delSpy = vi.spyOn(http, 'delete')
    const { result } = renderHook(() => useDeleteFolder(), { wrapper })
    await act(async () => {
      await result.current.mutateAsync({ id: 3, cascade: true })
    })
    expect(delSpy).toHaveBeenCalledWith(expect.stringContaining('/api/folders/3?cascade=1'))
  })

  it('useCreateFolder invalidates folders, links, and entries', async () => {
    const invalidate = vi.fn()
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    })
    client.invalidateQueries = invalidate
    const wrap = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(() => useCreateFolder(), { wrapper: wrap })
    await act(async () => {
      await result.current.mutateAsync({ name: 'Work', color: '#6366F1' })
    })
    await waitFor(() => expect(invalidate).toHaveBeenCalled())
    expect(keysFrom(invalidate)).toEqual(expect.arrayContaining(['folders', 'links', 'entries']))
  })

  it('useUpdateFolder invalidates folders, links, and entries', async () => {
    state.folders.push({
      id: 1,
      name: 'Old',
      color: '#000',
      parent_id: null,
      link_count: 0,
      folder_count: 0,
      has_password: false,
      password_hint: null,
      preview_links: [],
      preview_folders: [],
      created_at: '',
    })
    const invalidate = vi.fn()
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    })
    client.invalidateQueries = invalidate
    const wrap = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const { result } = renderHook(() => useUpdateFolder(), { wrapper: wrap })
    await act(async () => {
      await result.current.mutateAsync({ id: 1, body: { name: 'New' } })
    })
    await waitFor(() => expect(invalidate).toHaveBeenCalled())
    expect(keysFrom(invalidate)).toEqual(expect.arrayContaining(['folders', 'links', 'entries']))
  })
})

describe('useUnlockFolder', () => {
  it('posts password to unlock endpoint', async () => {
    state.folders.push({
      id: 7,
      name: 'Secret',
      color: '#abc',
      parent_id: null,
      has_password: true,
      link_count: 0,
      folder_count: 0,
      preview_links: [],
      preview_folders: [],
      created_at: '',
    })
    state.folderPasswords[7] = 'pass'
    const { result } = renderHook(() => useUnlockFolder(), { wrapper })
    await act(async () => {
      const out = await result.current.mutateAsync({ id: 7, password: 'pass' })
      expect(out.unlock_token).toBeTruthy()
    })
  })
})
