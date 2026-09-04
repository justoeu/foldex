import { useRef } from 'react'
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
  // Optional page size override (default ENTRY_PAGE_SIZE). Command palette
  // uses a higher limit when searching so matches beyond the first page
  // are visible (N1-NEX-015). Backend clamps to [1, 500].
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
    return {
      ...old,
      pages: old.pages.map((page) => (page ? page.map(fn) : page)),
    }
  })
}

// mapCachedLinkEntries is the link-only view over mapCachedEntries: fn is
// expressed in terms of Link (not the full Entry union), and notes are
// passed through untouched. Callers that already have a (Link) => Link
// transform (e.g. every link mutation's optimistic update) can reuse the
// same fn for BOTH ['links'] and ['entries'] caches without restating the
// discrimination at every call site.
export function mapCachedLinkEntries(qc: QueryClient, fn: (l: Link) => Link) {
  mapCachedEntries(qc, (e) => (e.kind === 'link' ? { ...e, ...fn(e) } : e))
}

// Drop one entry from every ['entries'] cache. Used when a link/note moves
// into a folder: mapCachedLinkEntries would leave it in the home
// (ungrouped) page with a new folder_id, so the card stayed on the grid
// until a later refetch (INV-068).
export function removeCachedEntry(qc: QueryClient, kind: Entry['kind'], id: number): void {
  qc.setQueriesData<EntriesCache>({ queryKey: ['entries'] }, (old) => {
    if (!old || !Array.isArray(old.pages)) return old
    return {
      ...old,
      pages: old.pages.map((page) => page.filter((entry) => !(entry.kind === kind && entry.id === id))),
    }
  })
}

// A single backend query preserves ordering across links and notes (ADR-27).
export function useEntries(params: EntryListParams, options?: { enabled?: boolean }) {
  const pageSize = params.limit && params.limit > 0 ? Math.min(params.limit, 500) : ENTRY_PAGE_SIZE
  const queryClient = useQueryClient()
  const key = entriesKey(params)
  const batchCursor = useRef(0)
  const entries = useInfiniteQuery({
    queryKey: key,
    queryFn: async ({ pageParam }) => {
      const search = new URLSearchParams()
      if (params.q) search.set('q', params.q)
      for (const id of params.tagIds ?? []) search.append('tag', String(id))
      if (params.sort) search.set('sort', params.sort)
      if (typeof params.folderId === 'number') {
        search.set('folder_id', String(params.folderId))
      } else if (params.ungrouped) {
        search.set('ungrouped', '1')
      }
      search.set('limit', String(pageSize))
      search.set('offset', String(pageParam))
      const { data } = await http.get<Entry[]>(`/api/entries?${search.toString()}`, {
        headers: params.unlockToken ? { [FOLDER_UNLOCK_HEADER]: params.unlockToken } : undefined,
      })
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
    queryFn: async () => {
      const currentIDs = pendingPreviewIDs(queryClient.getQueryData<EntriesCache>(key))
      if (batchCursor.current >= currentIDs.length) batchCursor.current = 0
      const batch = currentIDs.slice(batchCursor.current, batchCursor.current + PREVIEW_STATUS_BATCH_SIZE)
      if (batch.length === 0) return []
      batchCursor.current += batch.length
      if (batchCursor.current >= currentIDs.length) batchCursor.current = 0

      const search = new URLSearchParams()
      for (const id of batch) search.append('id', String(id))
      if (typeof params.folderId === 'number') search.set('folder_id', String(params.folderId))
      const { data } = await http.get<PreviewStatusResult[]>(`/api/entries/preview-status?${search.toString()}`, {
        headers: params.unlockToken ? { [FOLDER_UNLOCK_HEADER]: params.unlockToken } : undefined,
      })
      applyPreviewStatusResults(queryClient, key, data)
      return data
    },
    refetchInterval: () => pendingPreviewIDs(queryClient.getQueryData<EntriesCache>(key)).length > 0 ? 3000 : false,
    notifyOnChangeProps: [],
  })
  return entries
}
