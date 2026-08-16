import { useMutation, useQuery, useQueryClient, type InfiniteData, type QueryClient } from '@tanstack/react-query'
import { http } from './client'
import { invalidateEntryCounts, mapCachedLinkEntries } from './entries'
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
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['links'] })
      qc.invalidateQueries({ queryKey: ['entries'] })
      qc.invalidateQueries({ queryKey: ['tags'] })
      qc.invalidateQueries({ queryKey: ['folders'] })
      invalidateEntryCounts(qc)
    },
  })
}

export function useUpdateLink() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, body }: { id: number; body: LinkUpdate }) => {
      const { data } = await http.patch<Link>(`/api/links/${id}`, body)
      return data
    },
    onMutate: ({ id }) => ({ sequence: nextUpdateSequence(qc, id) }),
    onSuccess: (data, vars, context) => {
      // Association caches are refreshed only when their inputs changed.
      if (vars.body.tag_ids !== undefined || vars.body.pending_tags !== undefined) {
        qc.invalidateQueries({ queryKey: ['tags'] })
      }
      if ('folder_id' in vars.body) {
        qc.invalidateQueries({ queryKey: ['folders'] })
      }
      if (context.sequence !== currentUpdateSequence(qc, vars.id)) {
        qc.invalidateQueries({ queryKey: ['links'] })
        qc.invalidateQueries({ queryKey: ['entries'] })
        return
      }
      mapCachedLinks(qc, (l) => (l.id === data.id ? data : l))
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
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['links'] })
      qc.invalidateQueries({ queryKey: ['entries'] })
      qc.invalidateQueries({ queryKey: ['tags'] })
      qc.invalidateQueries({ queryKey: ['folders'] })
      invalidateEntryCounts(qc)
    },
  })
}

export function usePinLink() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, pinned }: { id: number; pinned: boolean }) => {
      const { data } = await http.patch<Link>(`/api/links/${id}`, { pinned })
      return data
    },
    onMutate: async ({ id, pinned }) => {
      await qc.cancelQueries({ queryKey: ['links'] })
      await qc.cancelQueries({ queryKey: ['entries'] })
      // The grid and sidebar keep separate caches for the same link.
      const linkSnapshots = qc.getQueriesData<LinksCache>({ queryKey: ['links'] })
      const entrySnapshots = qc.getQueriesData<LinksCache>({ queryKey: ['entries'] })
      mapCachedLinks(qc, (l) => (l.id === id ? { ...l, pinned } : l))
      mapCachedLinkEntries(qc, (l) => (l.id === id ? { ...l, pinned } : l))
      return { linkSnapshots, entrySnapshots }
    },
    onError: (_err, _vars, ctx) => {
      if (!ctx) return
      for (const [key, snapshot] of [...ctx.linkSnapshots, ...ctx.entrySnapshots]) {
        qc.setQueryData(key, snapshot)
      }
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ['links'] })
      qc.invalidateQueries({ queryKey: ['entries'] })
      qc.invalidateQueries({ queryKey: ['folders'] })
    },
  })
}

export function useRefreshPreview() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      await http.post(`/api/links/${id}/refresh-preview`)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['links'] })
      qc.invalidateQueries({ queryKey: ['entries'] })
      qc.invalidateQueries({ queryKey: ['folders'] })
    },
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
      const hit = urlMetadataCache.get(url)
      if (hit && hit.expiresAt > now) {
        return hit.data
      }
      const { data } = await http.get<UrlMetadata>('/api/links/url-metadata', {
        params: { url },
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
  const { data } = await http.post<{ url: string }>(`/api/links/${id}/image`, fd, {
    headers: { 'Content-Type': 'multipart/form-data' },
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
    refetchInterval: 60_000,
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

export function goHref(linkOrId: { id: number; slug: string } | number): string {
  if (typeof linkOrId === 'number') return `/go/${linkOrId}`
  return `/go/${linkOrId.slug || linkOrId.id}`
}
