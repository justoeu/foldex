import { useInfiniteQuery, type InfiniteData, type QueryClient } from '@tanstack/react-query'
import { http } from './client'
import { FOLDER_UNLOCK_HEADER } from './folders'
import type { Entry } from './types'

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
}

// Same page size as links (ENTRY_PAGE_SIZE mirrors LINK_PAGE_SIZE) — the
// backend caps at 500, we request 100 so "Load more" surfaces naturally.
export const ENTRY_PAGE_SIZE = 100

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
  ] as const

type EntriesCache = InfiniteData<Entry[]>

export function flattenEntries(data: EntriesCache | undefined): Entry[] {
  if (!data?.pages) return []
  const out: Entry[] = []
  for (const page of data.pages) out.push(...page)
  return out
}

// mapCachedEntries applies fn to every Entry in every page of every
// ['entries'] query — the interleaved-grid sibling of mapCachedLinks.
export function mapCachedEntries(qc: QueryClient, fn: (e: Entry) => Entry) {
  qc.setQueriesData<EntriesCache>({ queryKey: ['entries'] }, (old) => {
    if (!old) return old
    return {
      ...old,
      pages: old.pages.map((page) => page.map(fn)),
    }
  })
}

// useEntries replaces useLinks as the Home/folder grid's data source — one
// paginated, sorted, searched query spanning both links and notes instead of
// merging two independently-paginated streams client-side. See ADR-27.
export function useEntries(params: EntryListParams, options?: { enabled?: boolean }) {
  return useInfiniteQuery({
    queryKey: entriesKey(params),
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
      search.set('limit', String(ENTRY_PAGE_SIZE))
      search.set('offset', String(pageParam))
      const { data } = await http.get<Entry[]>(`/api/entries?${search.toString()}`, {
        headers: params.unlockToken ? { [FOLDER_UNLOCK_HEADER]: params.unlockToken } : undefined,
      })
      return data
    },
    initialPageParam: 0,
    getNextPageParam: (lastPage, _allPages, lastPageParam) =>
      lastPage.length < ENTRY_PAGE_SIZE ? undefined : (lastPageParam as number) + lastPage.length,
    enabled: options?.enabled ?? true,
    // Same auto-poll rationale as useLinks — a link entry can still be
    // 'pending' while the preview worker runs; notes never carry that state,
    // so the predicate is a no-op for them.
    refetchInterval: (query) => {
      const data = query.state.data as EntriesCache | undefined
      const all = flattenEntries(data)
      if (all.some((e) => e.kind === 'link' && e.preview_status === 'pending')) return 3000
      return false
    },
  })
}
