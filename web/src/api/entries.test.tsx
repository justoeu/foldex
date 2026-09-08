import { describe, it, expect, beforeEach, vi } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
import { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  applyPreviewStatusResults,
  useEntries,
  flattenEntries,
  mapCachedLinkEntries,
  optimisticEntryPatch,
  pendingPreviewIDs,
  removeCachedEntry,
} from './entries'
import entriesSrc from './entries.ts?raw'
import { http } from './client'
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

  it('polls only pending ids, patches the cache, and never refetches entry pages', async () => {
    state.links.push({
      id: 1, url: 'https://pending.example', title: 'Pending', slug: 'pending', click_count: 0,
      preview_status: 'pending', pinned: false, created_at: '', updated_at: 'before', tags: [],
    } as any)
    state.links.push({
      id: 2, url: 'https://ready.example', title: 'Ready', slug: 'ready', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    } as any)
    const fallback = vi.mocked(http.get).getMockImplementation()!
    vi.mocked(http.get).mockImplementation((async (url: string, ...rest: any[]) => {
      if (url.startsWith('/api/entries/preview-status')) {
        const ids = new URL(url, 'https://foldex.test').searchParams.getAll('id')
        expect(ids).toEqual(['1'])
        return { data: [{
          id: 1, found: true, preview_status: 'ok', description: 'Fetched', favicon_url: '/favicon.ico',
          og_image_url: '/preview.jpg', preview_error: null, updated_at: 'after',
        }] }
      }
      return fallback(url, ...rest)
    }) as never)

    const { result } = renderHook(() => useEntries({}), { wrapper })
    await waitFor(() => {
      const entries = flattenEntries(result.current.data)
      const entry = entries.find((candidate) => candidate.kind === 'link' && candidate.id === 1)
      expect(entry?.kind === 'link' ? entry.preview_status : undefined).toBe('ok')
    })

    const paths = vi.mocked(http.get).mock.calls.map(([url]) => String(url).split('?')[0])
    expect(paths.filter((path) => path === '/api/entries')).toHaveLength(1)
    expect(paths.filter((path) => path === '/api/entries/preview-status')).toHaveLength(1)
    expect(pendingPreviewIDs(result.current.data)).toEqual([])
  })

  it('does not notify the component for internal polling fetch-state changes', async () => {
    state.links.push({
      id: 1, url: 'https://pending.example', title: 'Pending', slug: 'pending', click_count: 0,
      preview_status: 'pending', preview_error: null, description: null, favicon_url: null,
      og_image_url: null, pinned: false, created_at: '', updated_at: 'same', tags: [],
    } as any)
    const fallback = vi.mocked(http.get).getMockImplementation()!
    let statusCalls = 0
    vi.mocked(http.get).mockImplementation((async (url: string, ...rest: any[]) => {
      if (url.startsWith('/api/entries/preview-status')) {
        statusCalls++
        return { data: [{
          id: 1, found: true, preview_status: 'pending', preview_error: null,
          description: null, favicon_url: null, og_image_url: null, updated_at: 'same',
        }] }
      }
      return fallback(url, ...rest)
    }) as never)
    const client = makeQueryClient()
    const localWrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    let renders = 0
    renderHook(() => {
      renders++
      return useEntries({})
    }, { wrapper: localWrapper })
    await waitFor(() => expect(statusCalls).toBe(1))
    await waitFor(() => expect(client.isFetching({ queryKey: ['entries-preview-status'] })).toBe(0))
    const rendersBeforeRefetch = renders

    await act(async () => {
      await client.refetchQueries({ queryKey: ['entries-preview-status'] })
    })

    expect(statusCalls).toBe(2)
    expect(renders).toBe(rendersBeforeRefetch)
  })

  it('aborts the in-flight page when the search key changes', async () => {
    // N1-NEX-007: Topbar search is the query key, but queryFn dropped
    // TanStack's AbortSignal, so a stale GET /api/entries kept running and
    // could still settle after the user had moved on. The first page is
    // hung on purpose so the abort is observable; the replacement query
    // uses the mock so the grid can show only the latest rows.
    state.links.push(
      {
        id: 1, url: 'https://alpha.example', title: 'Alpha', slug: 'alpha', click_count: 0,
        preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
      } as any,
      {
        id: 2, url: 'https://beta.example', title: 'Beta', slug: 'beta', click_count: 0,
        preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
      } as any,
    )
    const fallback = vi.mocked(http.get).getMockImplementation()!
    const signals: Array<AbortSignal | undefined> = []
    let firstPage = true
    vi.mocked(http.get).mockImplementation((async (url: string, ...rest: any[]) => {
      const path = String(url).split('?')[0]
      if (path === '/api/entries') {
        const config = rest[0] as { signal?: AbortSignal } | undefined
        signals.push(config?.signal)
        if (firstPage) {
          firstPage = false
          return new Promise((_resolve, reject) => {
            const err = Object.assign(new Error('canceled'), { code: 'ERR_CANCELED', name: 'CanceledError' })
            const signal = config?.signal
            if (signal?.aborted) {
              reject(err)
              return
            }
            signal?.addEventListener('abort', () => reject(err))
          })
        }
      }
      return fallback(url, ...rest)
    }) as never)

    const client = makeQueryClient()
    const localWrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    const { result, rerender } = renderHook(({ q }: { q: string }) => useEntries({ q }), {
      wrapper: localWrapper,
      initialProps: { q: 'Alpha' },
    })
    await waitFor(() => expect(signals).toHaveLength(1))

    // One act per keystroke so React cannot collapse the burst into a single
    // props update — that is the Home search shape, and it is what used to
    // fan out one GET /api/entries per character.
    await act(async () => { rerender({ q: 'B' }) })
    await act(async () => { rerender({ q: 'Be' }) })
    await act(async () => { rerender({ q: 'Bet' }) })
    await act(async () => { rerender({ q: 'Beta' }) })
    // Debounce (LinkDialog-style 500ms): a burst must not issue one GET per
    // keystroke. Without it this is already 5 overlapping page fetches.
    expect(signals).toHaveLength(1)

    await waitFor(() => expect(signals).toHaveLength(2))
    expect(signals[0]).toBeInstanceOf(AbortSignal)
    expect(signals[0]?.aborted).toBe(true)
    expect(signals[1]?.aborted).toBe(false)

    await waitFor(() => {
      const titles = flattenEntries(result.current.data).map((entry) => entry.title)
      expect(titles).toEqual(['Beta'])
    })
  })

})

describe('mapCachedLinkEntries', () => {
  // Regression for the "folder doesn't reload" bug: link mutations
  // (create/update/pin/delete) used to invalidate only ['links'] but the
  // grid renders from ['entries'] (ADR-27). mapCachedLinkEntries is the
  // optimistic-update bridge — it must patch link-kind entries and leave
  // notes untouched.
  it('patches link entries in place and passes notes through untouched', () => {
    const client = makeQueryClient()
    client.setQueryData(['entries', '', '', 'created', 'all', 'locked'], {
      pages: [[
        { kind: 'link', id: 1, url: 'https://a', title: 'A', click_count: 0,
          preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [] } as any,
        { kind: 'note', id: 2, title: 'Note', slug: 'n', pinned: false,
          click_count: 0, created_at: '', updated_at: '', tags: [] } as any,
      ]],
      pageParams: [0],
    })

    mapCachedLinkEntries(client, (l) => ({ ...l, pinned: true }))

    const cached = client.getQueryData<{ pages: any[] }>(['entries', '', '', 'created', 'all', 'locked'])
    const [linkEntry, noteEntry] = cached!.pages[0]
    expect(linkEntry.pinned).toBe(true)
    expect(linkEntry.kind).toBe('link')
    expect(noteEntry.pinned).toBe(false)
    expect(noteEntry.kind).toBe('note')
  })

  // Production callers pass identity-preserving fns (`l.id === id ? changed : l`).
  // Spreading every link allocates a new object for the whole page and
  // defeats React.memo on LinkCard. structuralSharing is off so this
  // asserts the mapper, not TQ replaceEqualDeep recovering cloned-but-equal rows.
  it('preserves identity of untouched links', () => {
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false, gcTime: 0, staleTime: 0, structuralSharing: false },
        mutations: { retry: false },
      },
    })
    const link = (id: number) => ({
      kind: 'link' as const,
      id,
      url: `https://${id}.example`,
      title: String(id),
      slug: String(id),
      click_count: 0,
      preview_status: 'ok' as const,
      pinned: false,
      created_at: '',
      updated_at: '',
      tags: [],
    })
    const target = link(1)
    const untouched = [link(2), link(3)]
    const note = {
      kind: 'note' as const,
      id: 99,
      title: 'Note',
      slug: 'n',
      pinned: false,
      click_count: 0,
      created_at: '',
      updated_at: '',
      tags: [],
    }
    const key = ['entries', '', '', 'created', 'all', 'locked']
    client.setQueryData(key, {
      pages: [[target, ...untouched, note]],
      pageParams: [0],
    })

    // Same shape as useUpdateLink: replace the hit with a Link payload
    // that has no `kind`, pass every other row through.
    const { kind: _kind, ...linkFields } = target
    const patched = { ...linkFields, pinned: true }
    mapCachedLinkEntries(client, (l) => (l.id === 1 ? patched : l))

    const cached = client.getQueryData<{ pages: typeof target[][] }>(key)
    const [nextTarget, nextTwo, nextThree, nextNote] = cached!.pages[0]
    expect(nextTarget).not.toBe(target)
    expect(nextTarget.pinned).toBe(true)
    expect(nextTarget.kind).toBe('link')
    expect(nextTwo).toBe(untouched[0])
    expect(nextThree).toBe(untouched[1])
    expect(nextNote).toBe(note)
  })

  it('removeCachedEntry drops the moved card from every entries page', () => {
    const client = makeQueryClient()
    const link = (id: number) => ({
      kind: 'link', id, url: `https://${id}.example`, title: String(id), slug: String(id),
      click_count: 0, preview_status: 'ok', pinned: false, folder_id: null,
      created_at: '', updated_at: '', tags: [],
    }) as any
    client.setQueryData(['entries', '', '', 'created', 'ungrouped', 'locked', 100], {
      pages: [[link(1), link(2)]],
      pageParams: [0],
    })

    removeCachedEntry(client, 'link', 1)

    const cached = client.getQueryData<{ pages: any[] }>(['entries', '', '', 'created', 'ungrouped', 'locked', 100])
    expect(cached!.pages[0]).toHaveLength(1)
    expect(cached!.pages[0][0].id).toBe(2)
  })
})

describe('applyPreviewStatusResults', () => {
  it('patches and removes ids only in the originating entries scope', () => {
    const client = makeQueryClient()
    const pending = (id: number) => ({
      kind: 'link', id, url: `https://${id}.example`, title: String(id), slug: String(id), click_count: 0,
      preview_status: 'pending', pinned: false, created_at: '', updated_at: 'before', tags: [],
    }) as any
    const firstKey = ['entries', 'first'] as const
    const secondKey = ['entries', 'second'] as const
    client.setQueryData(firstKey, { pages: [[pending(1)], [pending(2)]], pageParams: [0, 1] })
    client.setQueryData(secondKey, { pages: [[pending(1), pending(2)]], pageParams: [0] })

    applyPreviewStatusResults(client, firstKey, [
      { id: 1, found: true, preview_status: 'ok', og_image_url: '/ready.jpg', updated_at: 'after' },
      { id: 2, found: false },
    ])

    const first = client.getQueryData<{ pages: any[][] }>(firstKey)!.pages.flat()
    expect(first.find((entry) => entry.id === 1)).toMatchObject({ preview_status: 'ok', og_image_url: '/ready.jpg', updated_at: 'after' })
    expect(first.some((entry) => entry.id === 2)).toBe(false)
    const second = client.getQueryData<{ pages: any[][] }>(secondKey)!.pages.flat()
    expect(second.find((entry) => entry.id === 1)).toMatchObject({ preview_status: 'pending', updated_at: 'before' })
    expect(second.some((entry) => entry.id === 2)).toBe(true)
  })

  it('ignores a payload older than the cached updated_at', () => {
    const client = makeQueryClient()
    const key = ['entries', 'preview-race'] as const
    const cached = {
      kind: 'link', id: 1, url: 'https://1.example', title: '1', slug: '1', click_count: 0,
      preview_status: 'ok', description: 'fresh', favicon_url: '/fresh.ico',
      og_image_url: '/fresh.jpg', preview_error: null, pinned: false,
      created_at: '', updated_at: '2026-09-05T12:00:00.000Z', tags: [],
    } as any
    client.setQueryData(key, { pages: [[cached]], pageParams: [0] })

    applyPreviewStatusResults(client, key, [{
      id: 1, found: true, preview_status: 'pending', description: 'stale',
      favicon_url: '/stale.ico', og_image_url: '/stale.jpg', preview_error: null,
      updated_at: '2026-09-05T11:00:00.000Z',
    }])

    expect(client.getQueryData<{ pages: any[][] }>(key)!.pages[0][0]).toMatchObject({
      preview_status: 'ok',
      description: 'fresh',
      og_image_url: '/fresh.jpg',
      updated_at: '2026-09-05T12:00:00.000Z',
    })

    applyPreviewStatusResults(client, key, [{
      id: 1, found: true, preview_status: 'ok', description: 'same-tick',
      favicon_url: '/fresh.ico', og_image_url: '/same.jpg', preview_error: null,
      updated_at: '2026-09-05T12:00:00.000Z',
    }])
    expect(client.getQueryData<{ pages: any[][] }>(key)!.pages[0][0]).toMatchObject({
      description: 'same-tick',
      og_image_url: '/same.jpg',
      updated_at: '2026-09-05T12:00:00.000Z',
    })

    applyPreviewStatusResults(client, key, [{
      id: 1, found: true, preview_status: 'ok', description: 'newer',
      favicon_url: '/fresh.ico', og_image_url: '/newer.jpg', preview_error: null,
      updated_at: '2026-09-05T13:00:00.000Z',
    }])
    expect(client.getQueryData<{ pages: any[][] }>(key)!.pages[0][0]).toMatchObject({
      description: 'newer',
      og_image_url: '/newer.jpg',
      updated_at: '2026-09-05T13:00:00.000Z',
    })
  })
})

describe('optimisticEntryPatch', () => {
  it('does not copy link-only fields onto a note, and still patches a link', async () => {
    const client = makeQueryClient()
    const note = {
      kind: 'note' as const,
      id: 1,
      title: 'N',
      slug: 'n',
      pinned: false,
      tags: [],
      created_at: '',
      updated_at: '',
      click_count: 0,
    }
    const link = {
      kind: 'link' as const,
      id: 2,
      url: 'https://a.example',
      title: 'A',
      slug: 'a',
      click_count: 0,
      preview_status: 'ok' as const,
      pinned: false,
      created_at: '',
      updated_at: '',
      tags: [],
    }
    const key = ['entries', 'patch']
    client.setQueryData(key, { pages: [[note, link]], pageParams: [0] })

    await optimisticEntryPatch(client, 'note', 1, { pinned: true, preview_status: 'pending' })
    const afterNote = client.getQueryData<{ pages: typeof note[][] }>(key)!.pages[0]
    const nextNote = afterNote.find((entry) => entry.kind === 'note') as typeof note
    expect(nextNote.pinned).toBe(true)
    expect(nextNote).not.toHaveProperty('preview_status')
    expect(nextNote).not.toHaveProperty('url')

    await optimisticEntryPatch(client, 'link', 2, { pinned: true, preview_status: 'pending' })
    const afterLink = client.getQueryData<{ pages: Array<typeof note | typeof link>[] }>(key)!.pages[0]
    const nextLink = afterLink.find((entry) => entry.kind === 'link') as typeof link
    expect(nextLink.pinned).toBe(true)
    expect(nextLink.preview_status).toBe('pending')
  })

  it('does not assert the patched union with as Entry', () => {
    expect(entriesSrc).not.toMatch(/as Entry/)
  })
})
