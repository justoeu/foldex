import { useMutation, useQuery, useQueryClient, type InfiniteData, type QueryClient } from '@tanstack/react-query'
import { http } from './client'
import {
  cachedEntryFolderId,
  invalidateLibrary,
  mapCachedLinkEntries,
  optimisticEntryPatch,
  removeCachedEntry,
} from './entries'
import type { Link, LinkCreate, LinkUpdate } from './types'

type LinksCache = InfiniteData<Link[]>

export function mapCachedLinks(qc: QueryClient, fn: (l: Link) => Link) {
  qc.setQueriesData<LinksCache>({ queryKey: ['links'] }, (old) => {
    // Prefix matching also reaches the flat recent-changes cache.
    if (!old || !Array.isArray(old.pages)) return old
    return {
      ...old,
      pages: old.pages.map((page) => (page ? page.map(fn) : page)),
    }
  })
}

const updateSequences = new WeakMap<QueryClient, Map<number, number>>()

function nextUpdateSequence(qc: QueryClient, id: number): number {
  let sequences = updateSequences.get(qc)
  if (!sequences) {
    sequences = new Map()
    updateSequences.set(qc, sequences)
  }
  const next = (sequences.get(id) ?? 0) + 1
  sequences.set(id, next)
  return next
}

function currentUpdateSequence(qc: QueryClient, id: number): number | undefined {
  return updateSequences.get(qc)?.get(id)
}

export function useCreateLink() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: LinkCreate) => {
      const { data } = await http.post<Link>('/api/links', body)
      return data
    },
    onSuccess: () => invalidateLibrary(qc, { links: true, entries: true, tags: true, folders: true, counts: true }),
  })
}

export function useUpdateLink() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, body }: { id: number; body: LinkUpdate }) => {
      const { data } = await http.patch<Link>(`/api/links/${id}`, body)
      return data
    },
    onMutate: ({ id, body }) => ({
      sequence: nextUpdateSequence(qc, id),
      previousFolderId: 'folder_id' in body ? cachedEntryFolderId(qc, 'link', id) : undefined,
    }),
    onSuccess: (data, vars, context) => {
      qc.invalidateQueries({ queryKey: ['links', 'by-url'] })
      // Association caches are refreshed only when their inputs changed.
      if (vars.body.tag_ids !== undefined || vars.body.pending_tags !== undefined) {
        qc.invalidateQueries({ queryKey: ['tags'] })
      }
      const folderMoved = 'folder_id' in vars.body && data.folder_id !== context.previousFolderId
      if (folderMoved) {
        qc.invalidateQueries({ queryKey: ['folders'] })
      }
      if (context.sequence !== currentUpdateSequence(qc, vars.id)) {
        if (folderMoved) removeCachedEntry(qc, 'link', data.id)
        qc.invalidateQueries({ queryKey: ['links'] })
        qc.invalidateQueries({ queryKey: ['entries'] })
        return
      }
      mapCachedLinks(qc, (l) => (l.id === data.id ? data : l))
      if (folderMoved) {
        removeCachedEntry(qc, 'link', data.id)
        qc.invalidateQueries({ queryKey: ['entries'] })
        return
      }
      mapCachedLinkEntries(qc, (l) => (l.id === data.id ? data : l))
    },
  })
}

export function useDeleteLink() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      await http.delete(`/api/links/${id}`)
    },
    onSuccess: () => invalidateLibrary(qc, { links: true, entries: true, tags: true, folders: true, counts: true }),
  })
}

export function usePinLink() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, pinned }: { id: number; pinned: boolean }) => {
      const { data } = await http.patch<Link>(`/api/links/${id}`, { pinned })
      return data
    },
    onMutate: async ({ id, pinned }) => optimisticEntryPatch(qc, 'link', id, { pinned }),
    onError: (_err, _vars, ctx) => ctx?.rollback(),
    onSettled: () => invalidateLibrary(qc, { links: true, entries: true, folders: true }),
  })
}

export function useRefreshPreview() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      await http.post(`/api/links/${id}/refresh-preview`)
    },
    onMutate: async (id) => optimisticEntryPatch(qc, 'link', id, { preview_status: 'pending' }),
    onError: (_err, _id, ctx) => ctx?.rollback(),
    onSuccess: () => invalidateLibrary(qc, { links: true, entries: true, folders: true }),
  })
}

export type UrlMetadata = {
  title: string
  description: string
  favicon_url: string
  og_image_url: string
}

const URL_METADATA_CACHE_TTL_MS = 5 * 60 * 1000
const urlMetadataCache = new Map<string, { data: UrlMetadata; expiresAt: number }>()

export function _clearUrlMetadataCacheForTests() {
  urlMetadataCache.clear()
}

export function useFetchUrlMetadata() {
  return useMutation({
    mutationFn: async ({ url, signal }: { url: string; signal?: AbortSignal }): Promise<UrlMetadata> => {
      const now = Date.now()
      if (signal?.aborted) {
        const canceled = new Error('canceled')
        canceled.name = 'CanceledError'
        throw canceled
      }
      const hit = urlMetadataCache.get(url)
      if (hit && hit.expiresAt > now) {
        return hit.data
      }
      const { data } = await http.post<UrlMetadata>('/api/links/url-metadata', { url }, {
        signal,
      })
      urlMetadataCache.set(url, { data, expiresAt: now + URL_METADATA_CACHE_TTL_MS })
      return data
    },
  })
}

export async function uploadLinkImage(id: number, file: File): Promise<{ url: string }> {
  const fd = new FormData()
  fd.append('image', file)
  const { data } = await http.post<{ url: string }>(`/api/links/${id}/image`, fd)
  return data
}

export async function captureLinkScreenshot(id: number): Promise<{ url: string }> {
  const { data } = await http.post<{ url: string }>(`/api/links/${id}/screenshot`, undefined, {
    timeout: 80_000,
  })
  return data
}

export async function removeLinkImage(id: number): Promise<void> {
  await http.delete(`/api/links/${id}/image`)
}

export function useRecentChanges(days = 7, limit = 20, enabled = true) {
  return useQuery<Link[]>({
    queryKey: ['links', 'recent-changes', days, limit],
    queryFn: async () => {
      const { data } = await http.get<Link[]>(`/api/links/recent-changes?days=${days}&limit=${limit}`)
      return data
    },
    // Stand down while there is nothing to report — the sidebar hides the
    // section on an empty answer, so an unconditional minute tick was pure
    // background traffic (~1,440 GETs/day per tab). MarkChangeSeen's
    // invalidation re-evaluates the interval after real activity.
    refetchInterval: (query) => ((query.state.data?.length ?? 0) > 0 ? 60_000 : false),
    enabled,
  })
}

export function useMarkChangeSeen() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      await http.post(`/api/links/${id}/seen-change`)
    },
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: ['links'] })
      await qc.cancelQueries({ queryKey: ['entries'] })
      const now = new Date().toISOString()
      mapCachedLinks(qc, (l) => (l.id === id ? { ...l, change_seen_at: now } : l))
      mapCachedLinkEntries(qc, (l) => (l.id === id ? { ...l, change_seen_at: now } : l))
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['links', 'recent-changes'] })
      qc.invalidateQueries({ queryKey: ['entries'] })
    },
    onError: () => {
      qc.invalidateQueries({ queryKey: ['links'] })
      qc.invalidateQueries({ queryKey: ['entries'] })
    },
  })
}

export async function lookupLinkByURL(url: string): Promise<Link | null> {
  try {
    const { data } = await http.get<Link>('/api/links/by-url', { params: { url } })
    return data
  } catch (error) {
    const status = (error as { response?: { status?: number } })?.response?.status
    if (status === 404) return null
    throw error
  }
}

export function goHref(linkOrId: { id: number; slug: string } | number): string {
  if (typeof linkOrId === 'number') return `/go/${linkOrId}`
  return `/go/${linkOrId.slug || linkOrId.id}`
}
