import { describe, it, expect, beforeEach, vi } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  useCreateLink,
  useUpdateLink,
  useDeleteLink,
  useRefreshPreview,
  goHref,
  mapCachedLinks,
  usePinLink,
  useRecentChanges,
  useMarkChangeSeen,
  captureLinkScreenshot,
  uploadLinkImage,
  removeLinkImage,
  useFetchUrlMetadata,
  _clearUrlMetadataCacheForTests,
} from './links'
import { useTags } from './tags'
import { http } from './client'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { makeQueryClient } from '../test/renderWithProviders'

let state: MockState

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={makeQueryClient()}>{children}</QueryClientProvider>
}

beforeEach(() => {
  state = freshState()
  state.tags.push({ id: 1, name: 'jira', color: '#1f6feb', icon: null })
  installAxiosMock(state)
})

describe('goHref', () => {
  it('returns redirect path', () => {
    expect(goHref(42)).toBe('/go/42')
  })
})

describe('useCreateLink + useUpdateLink + useDeleteLink + useRefreshPreview', () => {
  it('creates a link', async () => {
    const { result } = renderHook(() => useCreateLink(), { wrapper })
    const link = await result.current.mutateAsync({
      url: 'https://hn',
      title: 'HN',
      tag_ids: [1],
      pending_tags: [{ name: 'news', color: '#22C55E' }],
    })
    expect(link.id).toBe(1)
    expect(link.tags.map((tag) => tag.name)).toEqual(['jira', 'news'])
    expect(state.tags.some((tag) => tag.name === 'news')).toBe(true)
  })

  it('updates a link', async () => {
    state.links.push({
      id: 7, url: 'https://x', title: 'x', click_count: 0,
      preview_status: 'pending', created_at: '', updated_at: '', tags: [],
    } as any)
    const { result } = renderHook(() => useUpdateLink(), { wrapper })
    const updated = await result.current.mutateAsync({ id: 7, body: { title: 'renamed' } })
    expect(updated.title).toBe('renamed')
  })

  it('deletes a link', async () => {
    state.links.push({
      id: 9, url: 'https://x', title: 'x', click_count: 0,
      preview_status: 'pending', created_at: '', updated_at: '', tags: [],
    } as any)
    const { result } = renderHook(() => useDeleteLink(), { wrapper })
    await result.current.mutateAsync(9)
    expect(state.links).toHaveLength(0)
  })

  it('refreshes preview without error', async () => {
    state.links.push({
      id: 1, url: 'https://r', title: 'R', click_count: 0,
      preview_status: 'failed', created_at: '', updated_at: '', tags: [],
    } as any)
    const { result } = renderHook(() => useRefreshPreview(), { wrapper })
    await expect(result.current.mutateAsync(1)).resolves.toBeUndefined()
    expect(state.links[0].preview_status).toBe('pending')
  })
})

describe('mapCachedLinks', () => {
  // Regression: setQueriesData({ queryKey: ['links'] }) is a PREFIX match in
  // TanStack v5, so it also hits useRecentChanges' entry whose key is
  // ['links','recent-changes',...] and whose value is a flat Link[], NOT
  // InfiniteData<Link[]>. Without the shape guard, old.pages.map throws
  // "Cannot read properties of undefined (reading 'map')" the moment a link
  // update/pin/seen-change fires while the sidebar's recent-changes query is
  // active.
  it('skips non-InfiniteData entries under the [links] prefix without throwing', async () => {
    const client = makeQueryClient()
    // Seed an InfiniteData entry and a flat Link[]
    // (useRecentChanges shape) under the shared ['links'] prefix.
    client.setQueryData(['links', '', '', 'created', 'all'], {
      pages: [[
        { id: 1, url: 'https://a', title: 'A', click_count: 0,
          preview_status: 'ok', created_at: '', updated_at: '', tags: [] } as any,
      ]],
      pageParams: [0],
    })
    const recentChanges = [
      { id: 9, url: 'https://rc', title: 'RC', click_count: 0,
        preview_status: 'ok', created_at: '', updated_at: '', tags: [] } as any,
    ]
    client.setQueryData(['links', 'recent-changes', 7, 10], recentChanges)

    expect(() =>
      mapCachedLinks(client, (l) => ({ ...l, title: `(${l.title})` })),
    ).not.toThrow()

    // InfiniteData page was patched in place.
    const inf = client.getQueryData<{ pages: any[] }>(['links', '', '', 'created', 'all'])
    expect(inf?.pages[0][0].title).toBe('(A)')
    // Flat Link[] was left untouched (not InfiniteData).
    expect(client.getQueryData(['links', 'recent-changes', 7, 10])).toBe(recentChanges)
  })
})

describe('tag cache invalidation', () => {
  function sharedWrapper(client: ReturnType<typeof makeQueryClient>) {
    return ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
  }

  function tagGetCount() {
    return (http.get as ReturnType<typeof vi.spyOn>).mock.calls
      .filter(([u]: [string]) => u.startsWith('/api/tags')).length
  }

  it('createLink invalidates tags query', async () => {
    const client = makeQueryClient()
    const wrap = sharedWrapper(client)

    const tagsHook = renderHook(() => useTags(), { wrapper: wrap })
    await waitFor(() => expect(tagsHook.result.current.isSuccess).toBe(true))
    const before = tagGetCount()

    const createHook = renderHook(() => useCreateLink(), { wrapper: wrap })
    await createHook.result.current.mutateAsync({ url: 'https://new.example', title: 'New', tag_ids: [] })

    await waitFor(() => expect(tagGetCount()).toBeGreaterThan(before))
  })

  it('deleteLink invalidates tags query', async () => {
    state.links.push({
      id: 5, url: 'https://del.example', title: 'Del', click_count: 0,
      preview_status: 'pending', created_at: '', updated_at: '', tags: [],
    } as any)

    const client = makeQueryClient()
    const wrap = sharedWrapper(client)

    const tagsHook = renderHook(() => useTags(), { wrapper: wrap })
    await waitFor(() => expect(tagsHook.result.current.isSuccess).toBe(true))
    const before = tagGetCount()

    const deleteHook = renderHook(() => useDeleteLink(), { wrapper: wrap })
    await deleteHook.result.current.mutateAsync(5)

    await waitFor(() => expect(tagGetCount()).toBeGreaterThan(before))
  })
})

describe('goHref slug preference', () => {
  it('prefers slug over id when given a Link-like object', () => {
    expect(goHref({ id: 9, slug: 'hello' })).toBe('/go/hello')
    expect(goHref({ id: 9, slug: '' })).toBe('/go/9')
  })
})

describe('usePinLink', () => {
  it('pins a link via PATCH', async () => {
    state.links.push({
      id: 3, url: 'https://p', title: 'P', click_count: 0, pinned: false,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any)
    const { result } = renderHook(() => usePinLink(), { wrapper })
    const out = await result.current.mutateAsync({ id: 3, pinned: true })
    expect(out.pinned).toBe(true)
    expect(state.links[0].pinned).toBe(true)
  })
})

describe('useRecentChanges', () => {
  it('lists recent-changes endpoint', async () => {
    state.links.push({
      id: 1, url: 'https://rc', title: 'RC', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
      last_change_detected_at: '2026-01-01T00:00:00Z',
    } as any)
    const { result } = renderHook(() => useRecentChanges(7, 10), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(Array.isArray(result.current.data)).toBe(true)
  })
})

describe('useMarkChangeSeen', () => {
  it('marks a change as seen', async () => {
    state.links.push({
      id: 4, url: 'https://s', title: 'S', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
      last_change_detected_at: '2026-05-30T10:00:00Z', change_seen_at: null,
    } as any)
    const { result } = renderHook(() => useMarkChangeSeen(), { wrapper })
    await result.current.mutateAsync(4)
    await waitFor(() => expect(state.links[0].change_seen_at).toBeTruthy())
  })
})

describe('uploadLinkImage / removeLinkImage', () => {
  it('uploads and removes an image', async () => {
    state.links.push({
      id: 6, url: 'https://img', title: 'Img', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
      og_image_url: null,
    } as any)
    const up = await uploadLinkImage(6, new File(['x'], 'a.png', { type: 'image/png' }))
    expect(up.url).toContain('/api/files/links/6')
    expect(state.links[0].og_image_url).toBeTruthy()
    await removeLinkImage(6)
    expect(state.links[0].og_image_url).toBeNull()
  })

  it('captures a screenshot for a link', async () => {
    state.links.push({
      id: 8, url: 'https://yt', title: 'YT', click_count: 0,
      preview_status: 'failed', created_at: '', updated_at: '', tags: [],
      og_image_url: null,
    } as any)
    const shot = await captureLinkScreenshot(8)
    expect(shot.url).toContain('/api/files/screenshots/8')
    expect(state.links[0].og_image_url).toBe(shot.url)
  })
})

describe('useFetchUrlMetadata', () => {
  beforeEach(() => {
    _clearUrlMetadataCacheForTests()
  })

  it('fetches and caches metadata', async () => {
    state.urlMetadata = { title: 'T', description: 'D' }
    const { result } = renderHook(() => useFetchUrlMetadata(), { wrapper })
    const a = await result.current.mutateAsync({ url: 'https://meta.example' })
    expect(a.title).toBe('T')
    const b = await result.current.mutateAsync({ url: 'https://meta.example' })
    expect(b.title).toBe('T')
    expect(state.urlMetadataCalls).toHaveLength(1)
  })
})

describe('useUpdateLink invalidation branches', () => {
  it('invalidates tags when tag_ids present and folders when folder_id present', async () => {
    state.links.push({
      id: 20, url: 'https://u', title: 'U', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [], folder_id: null,
    } as any)
    state.tags.push({ id: 1, name: 'jira', color: '#1f6feb', icon: null })
    const { result } = renderHook(() => useUpdateLink(), { wrapper })
    await result.current.mutateAsync({
      id: 20,
      body: { title: 'U2', tag_ids: [1], folder_id: null },
    })
    expect(state.links[0].title).toBe('U2')
  })

  it('removes a link from the ungrouped entries cache when it moves into a folder', async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    })
    const wrap = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const original = {
      id: 20, url: 'https://u', title: 'U', slug: 'u', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: 'v1', tags: [], folder_id: null,
    } as any
    state.links.push(original)
    client.setQueryData(['entries', '', '', 'created', 'ungrouped', 'locked', 100], {
      pages: [[{ ...original, kind: 'link' }]],
      pageParams: [0],
    })
    const { result } = renderHook(() => useUpdateLink(), { wrapper: wrap })
    await result.current.mutateAsync({ id: 20, body: { folder_id: 9 } })
    const cached = client.getQueryData<{ pages: any[] }>(['entries', '', '', 'created', 'ungrouped', 'locked', 100])
    expect(cached!.pages[0]).toEqual([])
  })

  it('does not let an older response overwrite a newer cached link', async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    })
    const wrap = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const original = {
      id: 20, url: 'https://u', title: 'Original', slug: 'u', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: 'v1', tags: [], folder_id: null,
    } as any
    client.setQueryData(['links', '', '', 'created', 'all'], {
      pages: [[original]],
      pageParams: [0],
    })
    client.setQueryData(['entries', '', '', 'created', 'all', 'locked', 100], {
      pages: [[{ ...original, kind: 'link' }]],
      pageParams: [0],
    })
    let resolveFirst!: (value: { data: typeof original }) => void
    let resolveSecond!: (value: { data: typeof original }) => void
    vi.mocked(http.patch)
      .mockImplementationOnce(() => new Promise((resolve) => { resolveFirst = resolve }) as ReturnType<typeof http.patch>)
      .mockImplementationOnce(() => new Promise((resolve) => { resolveSecond = resolve }) as ReturnType<typeof http.patch>)
    const { result } = renderHook(() => useUpdateLink(), { wrapper: wrap })

    const first = result.current.mutateAsync({ id: 20, body: { title: 'Older' } })
    const second = result.current.mutateAsync({ id: 20, body: { title: 'Newer' } })
    await waitFor(() => expect(http.patch).toHaveBeenCalledTimes(2))
    resolveSecond({ data: { ...original, title: 'Newer', updated_at: 'v3' } })
    await second
    resolveFirst({ data: { ...original, title: 'Older', updated_at: 'v2' } })
    await first

    const links = client.getQueryData<{ pages: any[][] }>(['links', '', '', 'created', 'all'])
    const entries = client.getQueryData<{ pages: any[][] }>(['entries', '', '', 'created', 'all', 'locked', 100])
    expect(links?.pages[0][0].title).toBe('Newer')
    expect(entries?.pages[0][0].title).toBe('Newer')
  })
})
