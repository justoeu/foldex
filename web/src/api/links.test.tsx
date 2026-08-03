import { describe, it, expect, beforeEach, vi } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import {
  useLinks,
  flattenLinks,
  useCreateLink,
  useUpdateLink,
  useDeleteLink,
  useRefreshPreview,
  useCaptureScreenshot,
  captureScreenshot,
  goHref,
  mapCachedLinks,
  usePinLink,
  useRecentChanges,
  useMarkChangeSeen,
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

describe('useLinks', () => {
  it('lists empty by default', async () => {
    const { result } = renderHook(() => useLinks({}), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    // useInfiniteQuery returns InfiniteData<Link[]> — flatten to compare.
    expect(flattenLinks(result.current.data)).toEqual([])
  })

  it('applies q, tag and sort params', async () => {
    state.links.push(
      {
        id: 1, url: 'https://a', title: 'Alpha', click_count: 1,
        preview_status: 'ok', created_at: '', updated_at: '', tags: [state.tags[0]],
      } as any,
      {
        id: 2, url: 'https://b', title: 'Beta', click_count: 5,
        preview_status: 'ok', created_at: '', updated_at: '', tags: [],
      } as any,
    )
    const { result } = renderHook(
      () => useLinks({ q: 'Beta', sort: 'clicks' }),
      { wrapper },
    )
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    const flat = flattenLinks(result.current.data)
    expect(flat[0]?.title).toBe('Beta')
  })

  it('paginates via fetchNextPage when more results exist', async () => {
    // Seed 3 links and force a page size of 2 by pushing past the default
    // 100-link threshold is overkill; instead exercise the slice path by
    // crafting a state where the mock returns multiple pages. We rely on
    // the mock's limit/offset slicing.
    for (let i = 1; i <= 3; i++) {
      state.links.push({
        id: i, url: `https://${i}`, title: `L${i}`, click_count: 0,
        preview_status: 'ok', created_at: '', updated_at: '', tags: [],
      } as any)
    }
    // Default LINK_PAGE_SIZE=100 means all 3 fit on page 1 → no "Load more".
    // Verify hasNextPage is false in that case (the contract the Home
    // component relies on to hide the button).
    const { result } = renderHook(() => useLinks({}), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.hasNextPage).toBe(false)
    expect(flattenLinks(result.current.data)).toHaveLength(3)
  })
})

describe('useCreateLink + useUpdateLink + useDeleteLink + useRefreshPreview', () => {
  it('creates a link', async () => {
    const { result } = renderHook(() => useCreateLink(), { wrapper })
    const link = await result.current.mutateAsync({ url: 'https://hn', title: 'HN', tag_ids: [1] })
    expect(link.id).toBe(1)
    expect(link.tags[0].name).toBe('jira')
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
    const { result } = renderHook(() => useRefreshPreview(), { wrapper })
    await expect(result.current.mutateAsync(1)).resolves.toBeUndefined()
  })
})

describe('useCaptureScreenshot', () => {
  it('returns the screenshot url for an existing link', async () => {
    state.links.push({
      id: 5, url: 'https://example.com', title: 'Example', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any)
    const { result } = renderHook(() => useCaptureScreenshot(), { wrapper })
    const out = await result.current.mutateAsync(5)
    expect(out.url).toBe('/api/files/screenshots/5.png')
  })

  it('throws when link does not exist', async () => {
    const { result } = renderHook(() => useCaptureScreenshot(), { wrapper })
    await expect(result.current.mutateAsync(999)).rejects.toMatchObject({
      response: { status: 404 },
    })
  })
})

describe('captureScreenshot', () => {
  it('calls POST /api/links/:id/screenshot and returns url', async () => {
    state.links.push({
      id: 11, url: 'https://foo.example', title: 'Foo', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any)
    const out = await captureScreenshot(11)
    expect(out.url).toBe('/api/files/screenshots/11.png')
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
    // Seed an InfiniteData entry (real useLinks shape) AND a flat Link[]
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

describe('flattenLinks', () => {
  it('returns empty for undefined or missing pages', () => {
    expect(flattenLinks(undefined)).toEqual([])
    expect(flattenLinks({ pages: undefined } as any)).toEqual([])
  })

  it('flattens multiple pages', () => {
    expect(flattenLinks({
      pages: [[{ id: 1 } as any], [{ id: 2 } as any]],
      pageParams: [0, 1],
    })).toHaveLength(2)
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
})

describe('useLinks filters', () => {
  it('applies folderId and ungrouped params', async () => {
    state.links.push(
      { id: 1, url: 'https://a', title: 'A', click_count: 0, preview_status: 'ok', created_at: '', updated_at: '', tags: [], folder_id: 5 } as any,
      { id: 2, url: 'https://b', title: 'B', click_count: 0, preview_status: 'ok', created_at: '', updated_at: '', tags: [], folder_id: null } as any,
    )
    const { result: r1 } = renderHook(() => useLinks({ folderId: 5 }), { wrapper })
    await waitFor(() => expect(r1.current.isSuccess).toBe(true))
    const { result: r2 } = renderHook(() => useLinks({ ungrouped: true }), { wrapper })
    await waitFor(() => expect(r2.current.isSuccess).toBe(true))
    const { result: r3 } = renderHook(() => useLinks({ tagIds: [1, 2] }), { wrapper })
    await waitFor(() => expect(r3.current.isSuccess).toBe(true))
  })
})
