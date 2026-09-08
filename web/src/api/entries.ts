import { useEffect, useRef, useState } from 'react'
import { useInfiniteQuery, useQuery, useQueryClient, type InfiniteData, type QueryClient, type QueryKey } from '@tanstack/react-query'
import { http } from './client'
import { FOLDER_UNLOCK_HEADER } from './folders'
import type { Entry, Link } from './types'

export type EntryListParams = {
  q?: string
  tagIds?: number[]
  sort?: 'created' | 'clicks' | 'recent' | 'alpha' | 'alpha_desc'
  folderId?: number | null
  ungrouped?: boolean
  // Required to read a protected folder's contents (ADR-28) — the backend
  // gates GET /api/entries?folder_id=X the same way it gates the folders
  // list. Ignored when folderId is unset.
  unlockToken?: string
  // Optional page size override (default ENTRY_PAGE_SIZE). Backend clamps to [1, 500].
  limit?: number
}

// The backend caps at 500; 100 keeps first paint bounded while preserving pagination.
export const ENTRY_PAGE_SIZE = 100

export type EntryCounts = { links: number; notes: number }

export const entryCountsKey = ['entry-counts'] as const

export async function fetchEntryCounts(): Promise<EntryCounts> {
  const { data } = await http.get<EntryCounts>('/api/entries/counts')
  return data
}

export function invalidateEntryCounts(qc: QueryClient) {
  return qc.invalidateQueries({ queryKey: entryCountsKey })
}

// The standard invalidation fan-out for library mutations, replacing the
// hand-rolled per-mutation bundles that had already drifted (folders.ts
// omitted ['tags'] and entry-counts; every new cache a mutation must
// refresh is a ~10-site edit to miss one of). Flags keep each call site's
// existing reach — behavior-preserving consolidation.
export function invalidateLibrary(
  qc: QueryClient,
  keys: { links?: boolean; entries?: boolean; tags?: boolean; folders?: boolean; counts?: boolean },
) {
  if (keys.links) void qc.invalidateQueries({ queryKey: ['links'] })
  if (keys.entries) void qc.invalidateQueries({ queryKey: ['entries'] })
  if (keys.tags) void qc.invalidateQueries({ queryKey: ['tags'] })
  if (keys.folders) void qc.invalidateQueries({ queryKey: ['folders'] })
  if (keys.counts) invalidateEntryCounts(qc)
}

const entriesKey = (p: EntryListParams) =>
  [
    'entries',
    p.q ?? '',
    [...(p.tagIds ?? [])].sort((a, b) => a - b).join(','),
    p.sort ?? 'created',
    p.folderId ?? (p.ungrouped ? 'ungrouped' : 'all'),
    // Same rationale as useFolders: presence-only, not the raw token, so a
    // fresh unlock of the same folder doesn't needlessly bust the cache.
    p.folderId != null && p.unlockToken ? 'unlocked' : 'locked',
    p.limit ?? ENTRY_PAGE_SIZE,
  ] as const

type EntriesCache = InfiniteData<Entry[]>

export type PreviewStatusResult = {
  id: number
  found: boolean
  preview_status?: Link['preview_status'] | null
  description?: string | null
  favicon_url?: string | null
  og_image_url?: string | null
  preview_error?: string | null
  updated_at?: string | null
}

const PREVIEW_STATUS_BATCH_SIZE = 100

export function flattenEntries(data: EntriesCache | undefined): Entry[] {
  if (!data?.pages) return []
  const out: Entry[] = []
  for (const page of data.pages) out.push(...page)
  return out
}

// Empty-open command palette suggestions reuse whatever Home (or a folder
// view) already fetched. A second GET /api/entries?limit=200 just to paint
// 12 rows is the N1-NEX-009 defect.
export function collectCachedEntries(qc: QueryClient): Entry[] {
  const seen = new Set<string>()
  const out: Entry[] = []
  for (const [, data] of qc.getQueriesData<EntriesCache>({ queryKey: ['entries'] })) {
    for (const entry of flattenEntries(data)) {
      const id = `${entry.kind}:${entry.id}`
      if (seen.has(id)) continue
      seen.add(id)
      out.push(entry)
    }
  }
  return out
}

export function pendingPreviewIDs(data: EntriesCache | undefined): number[] {
  const ids = new Set<number>()
  for (const entry of flattenEntries(data)) {
    if (entry.kind === 'link' && entry.preview_status === 'pending') ids.add(entry.id)
  }
  return [...ids]
}

export function applyPreviewStatusResults(qc: QueryClient, key: QueryKey, results: PreviewStatusResult[]) {
  const byID = new Map(results.map((result) => [result.id, result]))
  qc.setQueryData<EntriesCache>(key, (old) => {
    if (!old || !Array.isArray(old.pages)) return old
    let changed = false
    const pages = old.pages.map((page) => {
      let pageChanged = false
      const next: Entry[] = []
      for (const entry of page) {
        if (entry.kind !== 'link') {
          next.push(entry)
          continue
        }
        const result = byID.get(entry.id)
        if (!result) {
          next.push(entry)
          continue
        }
        if (!result.found) {
          changed = true
          pageChanged = true
          continue
        }
        if (!result.preview_status || !result.updated_at) {
          next.push(entry)
          continue
        }
        const resultTime = Date.parse(result.updated_at)
        const entryTime = Date.parse(entry.updated_at)
        if (!Number.isNaN(resultTime) && !Number.isNaN(entryTime) && resultTime < entryTime) {
          next.push(entry)
          continue
        }
        const description = result.description ?? null
        const faviconURL = result.favicon_url ?? null
        const ogImageURL = result.og_image_url ?? null
        const previewError = result.preview_error ?? null
        if (
          entry.preview_status === result.preview_status &&
          entry.description === description &&
          entry.favicon_url === faviconURL &&
          entry.og_image_url === ogImageURL &&
          entry.preview_error === previewError &&
          entry.updated_at === result.updated_at
        ) {
          next.push(entry)
          continue
        }
        changed = true
        pageChanged = true
        next.push({
          ...entry,
          preview_status: result.preview_status,
          description,
          favicon_url: faviconURL,
          og_image_url: ogImageURL,
          preview_error: previewError,
          updated_at: result.updated_at,
        })
      }
      return pageChanged ? next : page
    })
    if (!changed) return old
    return {
      ...old,
      pages,
    }
  })
}

// mapCachedEntries applies fn to every Entry in every page of every
// ['entries'] query — the interleaved-grid sibling of mapCachedLinks.
export function mapCachedEntries(qc: QueryClient, fn: (e: Entry) => Entry) {
  qc.setQueriesData<EntriesCache>({ queryKey: ['entries'] }, (old) => {
    if (!old || !Array.isArray(old.pages)) return old
    let changed = false
    const pages = old.pages.map((page) => {
      if (!page) return page
      let pageChanged = false
      const next = page.map((entry) => {
        const mapped = fn(entry)
        if (mapped !== entry) pageChanged = true
        return mapped
      })
      if (!pageChanged) return page
      changed = true
      return next
    })
    return changed ? { ...old, pages } : old
  })
}

// mapCachedLinkEntries is the link-only view over mapCachedEntries: fn is
// expressed in terms of Link (not the full Entry union), and notes are
// passed through untouched. Callers that already have a (Link) => Link
// transform (e.g. every link mutation's optimistic update) can reuse the
// same fn for BOTH ['links'] and ['entries'] caches without restating the
// discrimination at every call site. A PATCH payload is a Link, not an
// Entry — spreading onto `e` is what keeps `kind: 'link'` on a replace.
export function mapCachedLinkEntries(qc: QueryClient, fn: (l: Link) => Link) {
  mapCachedEntries(qc, (e) => {
    if (e.kind !== 'link') return e
    const next = fn(e)
    return next === e ? e : { ...e, ...next }
  })
}

// Drop one entry from every ['entries'] cache. Used when a link/note moves
// into a folder: mapCachedLinkEntries would leave it in the home
// (ungrouped) page with a new folder_id, so the card stayed on the grid
// until a later refetch (INV-068).
export function removeCachedEntry(qc: QueryClient, kind: Entry['kind'], id: number): void {
  qc.setQueriesData<EntriesCache>({ queryKey: ['entries'] }, (old) => {
    if (!old || !Array.isArray(old.pages)) return old
    let changed = false
    const pages = old.pages.map((page) => {
      if (!page) return page
      const next = page.filter((entry) => !(entry.kind === kind && entry.id === id))
      if (next.length !== page.length) changed = true
      return next
    })
    return changed ? { ...old, pages } : old
  })
}

export function cachedEntryFolderId(qc: QueryClient, kind: Entry['kind'], id: number): number | null | undefined {
  for (const [, data] of qc.getQueriesData<EntriesCache>({ queryKey: ['entries'] })) {
    for (const page of data?.pages ?? []) {
      for (const entry of page ?? []) {
        if (entry.kind === kind && entry.id === id) return entry.folder_id ?? null
      }
    }
  }
  return undefined
}

// The optimistic patch recipe shared by usePinLink / usePinNote /
// useRefreshPreview: cancel both cache families, record the matched
// entry's PREVIOUS object per query, apply the patch, and hand back a
// surgical rollback. Restoring only the touched entry — not a whole-cache
// snapshot — is what keeps a slow failed mutation from wiping a
// concurrent mutation's already-applied optimistic state (RACE-HER-004).
// Notes never touch ['links'] (Link caches hold no notes), so the helper
// keys that family on kind === 'link'.
export type OptimisticEntryPatch = {
  patched: boolean
  rollback: () => void
}

export async function optimisticEntryPatch(
  qc: QueryClient,
  kind: Entry['kind'],
  id: number,
  patch: Partial<Pick<Link, 'pinned' | 'preview_status'>>,
): Promise<OptimisticEntryPatch> {
  await qc.cancelQueries({ queryKey: ['links'] })
  await qc.cancelQueries({ queryKey: ['entries'] })

  const previousLinks = new Map<QueryKey, Link>()
  if (kind === 'link') {
    for (const [key, data] of qc.getQueriesData<LinksCache>({ queryKey: ['links'] })) {
      if (!data || !Array.isArray(data.pages)) continue
      for (const page of data.pages) {
        const found = (page ?? []).find((l) => l.id === id)
        if (found) {
          previousLinks.set(key, found)
          break
        }
      }
    }
  }
  const previousEntries = new Map<QueryKey, Entry>()
  for (const [key, data] of qc.getQueriesData<EntriesCache>({ queryKey: ['entries'] })) {
    if (!data || !Array.isArray(data.pages)) continue
    for (const page of data.pages) {
      const found = (page ?? []).find((e) => e.kind === kind && e.id === id)
      if (found) {
        previousEntries.set(key, found)
        break
      }
    }
  }

  if (kind === 'link') mapCachedLinksPatch(qc, id, patch)
  mapCachedEntries(qc, (e) => {
    if (e.kind !== kind || e.id !== id) return e
    if (e.kind === 'link') return { ...e, ...patch }
    if ('pinned' in patch) return { ...e, pinned: patch.pinned ?? e.pinned }
    return e
  })

  const rollback = () => {
    for (const [key, previous] of previousLinks) {
      qc.setQueryData<LinksCache>(key, (old) =>
        old && Array.isArray(old.pages)
          ? { ...old, pages: old.pages.map((page) => page?.map((l) => (l.id === id ? previous : l))) }
          : old,
      )
    }
    for (const [key, previous] of previousEntries) {
      qc.setQueryData<EntriesCache>(key, (old) =>
        old && Array.isArray(old.pages)
          ? { ...old, pages: old.pages.map((page) => page?.map((e) => (e.kind === kind && e.id === id ? previous : e))) }
          : old,
      )
    }
  }
  return { patched: previousLinks.size > 0 || previousEntries.size > 0, rollback }
}

type LinksCache = InfiniteData<Link[]>

function mapCachedLinksPatch(qc: QueryClient, id: number, patch: Partial<Pick<Link, 'pinned' | 'preview_status'>>) {
  qc.setQueriesData<LinksCache>({ queryKey: ['links'] }, (old) => {
    if (!old || !Array.isArray(old.pages)) return old
    return {
      ...old,
      pages: old.pages.map((page) => (page ? page.map((l) => (l.id === id ? { ...l, ...patch } : l)) : page)),
    }
  })
}

// Same idle window as LinkDialog URL autofill (INV-125): Home types into
// workspace.q on every keystroke, and without this the query key would
// fire one GET /api/entries per character.
const SEARCH_DEBOUNCE_MS = 500

function useDebouncedQ(q: string | undefined): string {
  const value = q ?? ''
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    if (value === settled) return
    const id = window.setTimeout(() => setSettled(value), SEARCH_DEBOUNCE_MS)
    return () => window.clearTimeout(id)
  }, [value, settled])
  return settled
}

function entriesRequestConfig(unlockToken: string | undefined, signal: AbortSignal) {
  return {
    signal,
    headers: unlockToken ? { [FOLDER_UNLOCK_HEADER]: unlockToken } : undefined,
  }
}

// A single backend query preserves ordering across links and notes (ADR-27).
export function useEntries(params: EntryListParams, options?: { enabled?: boolean; qSettled?: boolean }) {
  const pageSize = params.limit && params.limit > 0 ? Math.min(params.limit, 500) : ENTRY_PAGE_SIZE
  const queryClient = useQueryClient()
  const debouncedQ = useDebouncedQ(params.q)
  // CommandPalette already settles q (200ms). Debouncing again here made ⌘K
  // wait ~700ms. Home still types into workspace.q on every keystroke.
  const q = options?.qSettled ? (params.q ?? '') : debouncedQ
  const key = entriesKey({ ...params, q })
  const batchCursor = useRef(0)
  const entries = useInfiniteQuery({
    queryKey: key,
    queryFn: async ({ pageParam, signal }) => {
      const search = new URLSearchParams()
      if (q) search.set('q', q)
      for (const id of params.tagIds ?? []) search.append('tag', String(id))
      if (params.sort) search.set('sort', params.sort)
      if (typeof params.folderId === 'number') {
        search.set('folder_id', String(params.folderId))
      } else if (params.ungrouped) {
        search.set('ungrouped', '1')
      }
      search.set('limit', String(pageSize))
      search.set('offset', String(pageParam))
      const { data } = await http.get<Entry[]>(`/api/entries?${search.toString()}`, entriesRequestConfig(params.unlockToken, signal))
      return data
    },
    initialPageParam: 0,
    getNextPageParam: (lastPage, _allPages, lastPageParam) =>
      lastPage.length < pageSize ? undefined : (lastPageParam as number) + lastPage.length,
    enabled: options?.enabled ?? true,
  })
  const pendingIDs = pendingPreviewIDs(entries.data)
  useQuery({
    queryKey: ['entries-preview-status', key],
    enabled: (options?.enabled ?? true) && pendingIDs.length > 0,
    queryFn: async ({ signal }) => {
      const currentIDs = pendingPreviewIDs(queryClient.getQueryData<EntriesCache>(key))
      if (batchCursor.current >= currentIDs.length) batchCursor.current = 0
      const batch = currentIDs.slice(batchCursor.current, batchCursor.current + PREVIEW_STATUS_BATCH_SIZE)
      if (batch.length === 0) return []
      batchCursor.current += batch.length
      if (batchCursor.current >= currentIDs.length) batchCursor.current = 0

      const search = new URLSearchParams()
      for (const id of batch) search.append('id', String(id))
      if (typeof params.folderId === 'number') search.set('folder_id', String(params.folderId))
      const { data } = await http.get<PreviewStatusResult[]>(`/api/entries/preview-status?${search.toString()}`, entriesRequestConfig(params.unlockToken, signal))
      applyPreviewStatusResults(queryClient, key, data)
      return data
    },
    refetchInterval: () => pendingPreviewIDs(queryClient.getQueryData<EntriesCache>(key)).length > 0 ? 3000 : false,
    notifyOnChangeProps: [],
  })
  return entries
}
