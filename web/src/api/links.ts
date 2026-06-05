import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { http } from './client'
import type { Link, LinkCreate, LinkUpdate } from './types'

export type LinkListParams = {
  q?: string
  tagIds?: number[]
  sort?: 'created' | 'clicks' | 'recent' | 'alpha' | 'alpha_desc'
  folderId?: number | null  // null = ungrouped, number = inside folder, undefined = all
  ungrouped?: boolean
}

const linksKey = (p: LinkListParams) =>
  [
    'links',
    p.q ?? '',
    // Sort the tag IDs before joining so toggling A→B vs B→A produces the
    // same cache key for the same logical filter. Without the sort, the same
    // result set would be fetched twice on different orderings.
    [...(p.tagIds ?? [])].sort((a, b) => a - b).join(','),
    p.sort ?? 'created',
    p.folderId ?? (p.ungrouped ? 'ungrouped' : 'all'),
  ] as const

export function useLinks(params: LinkListParams, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: linksKey(params),
    queryFn: async () => {
      const search = new URLSearchParams()
      if (params.q) search.set('q', params.q)
      for (const id of params.tagIds ?? []) search.append('tag', String(id))
      if (params.sort) search.set('sort', params.sort)
      if (typeof params.folderId === 'number') {
        search.set('folder_id', String(params.folderId))
      } else if (params.ungrouped) {
        search.set('ungrouped', '1')
      }
      const { data } = await http.get<Link[]>(`/api/links?${search.toString()}`)
      return data
    },
    enabled: options?.enabled ?? true,
    // Auto-poll while ANY link in the current page is still pending so the
    // user doesn't have to refresh to see "capturando…" flip to a real
    // preview. Backend worker takes a few seconds per link; we re-poll every
    // 3s and stop the moment nothing is pending. Stale links are unaffected.
    refetchInterval: (query) => {
      const data = query.state.data as Link[] | undefined
      if (data?.some((l) => l.preview_status === 'pending')) return 3000
      return false
    },
  })
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
      qc.invalidateQueries({ queryKey: ['tags'] })
      // Folder cards on the home grid carry `link_count` and `preview_links`
      // (the 2x2 mini-thumbs). Any link mutation can shift those — invalidate
      // so the UI re-fetches and the card updates in real time.
      qc.invalidateQueries({ queryKey: ['folders'] })
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
    // Narrow invalidation: patch the changed link in every cached list via
    // setQueryData; only invalidate folders/tags when the mutation actually
    // touched those associations. The previous "invalidate everything"
    // recipe triggered 3 full refetches per drag-and-drop or edit.
    onSuccess: (data, vars) => {
      qc.setQueriesData<Link[] | undefined>({ queryKey: ['links'] }, (old) => {
        if (!old) return old
        return old.map((l) => (l.id === data.id ? data : l))
      })
      if (vars.body.tag_ids !== undefined) {
        qc.invalidateQueries({ queryKey: ['tags'] })
      }
      // folder_id touched OR cross-folder move → folder cards' link_count
      // and preview_links may have shifted; invalidate so they reconcile.
      if ('folder_id' in vars.body) {
        qc.invalidateQueries({ queryKey: ['folders'] })
      }
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
      qc.invalidateQueries({ queryKey: ['tags'] })
      // Folder cards on the home grid carry `link_count` and `preview_links`
      // (the 2x2 mini-thumbs). Any link mutation can shift those — invalidate
      // so the UI re-fetches and the card updates in real time.
      qc.invalidateQueries({ queryKey: ['folders'] })
    },
  })
}

// usePinLink optimistically flips link.pinned in every cached list so the
// badge animates instantly instead of waiting for the PATCH + refetch. Pin
// is the most-clicked stateful action in the UI; a 300-500 ms round-trip
// before the badge moves felt sluggish.
//
// onMutate: snapshot every ['links', ...] query and patch the target link in
// place. onError: rollback to the snapshot. onSettled: invalidate to reconcile
// (server is the source of truth — the sort order may need to shift).
export function usePinLink() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, pinned }: { id: number; pinned: boolean }) => {
      const { data } = await http.patch<Link>(`/api/links/${id}`, { pinned })
      return data
    },
    onMutate: async ({ id, pinned }) => {
      await qc.cancelQueries({ queryKey: ['links'] })
      const snapshots = qc.getQueriesData<Link[]>({ queryKey: ['links'] })
      for (const [key, list] of snapshots) {
        if (!list) continue
        qc.setQueryData<Link[]>(
          key,
          list.map((l) => (l.id === id ? { ...l, pinned } : l)),
        )
      }
      return { snapshots }
    },
    onError: (_err, _vars, ctx) => {
      if (!ctx) return
      for (const [key, list] of ctx.snapshots) {
        qc.setQueryData(key, list)
      }
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ['links'] })
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
      qc.invalidateQueries({ queryKey: ['folders'] })
    },
  })
}

export function useCaptureScreenshot() {
  return useMutation({
    mutationFn: async (id: number) => {
      const { data } = await http.post<{ url: string }>(`/api/links/${id}/screenshot`)
      return data
    },
  })
}

// URL-metadata shape returned by GET /api/links/url-metadata. Mirrors what the
// preview worker eventually stamps on the link asynchronously — exposing it
// synchronously lets the LinkDialog pre-fill Title / Description before save.
export type UrlMetadata = {
  title: string
  description: string
  favicon_url: string
  og_image_url: string
}

// Module-level memo for url-metadata responses, keyed by the exact URL. Saves
// a backend round-trip for the "paste → close → re-paste same URL" loop
// (Cmd+V duplicates, dialog close-then-reopen). 5-min TTL is short enough
// that a stale og:title from a freshly-edited source page only lingers for
// a few minutes; OG tags rarely change minute-to-minute in practice.
const URL_METADATA_CACHE_TTL_MS = 5 * 60 * 1000
const urlMetadataCache = new Map<string, { data: UrlMetadata; expiresAt: number }>()

// Exposed for tests so each case starts with an empty cache. Production code
// never needs to call this — entries expire by TTL.
export function _clearUrlMetadataCacheForTests() {
  urlMetadataCache.clear()
}

// useFetchUrlMetadata wraps the metadata endpoint as a mutation because we
// trigger fetches imperatively from a debounce effect AND any failure is
// silent UX (the user can still type manually). The module-level Map gives
// us the cross-dialog-mount dedup that useQuery would give us, without
// paying the queryKey ceremony for fire-and-forget calls.
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

export async function captureScreenshot(id: number): Promise<{ url: string }> {
  const { data } = await http.post<{ url: string }>(`/api/links/${id}/screenshot`)
  return data
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

// useRecentChanges feeds the sidebar's "Recent updates" section. Server
// caps days ∈ [1,30] and limit ∈ [1,100] via clampInt so passing bigger
// values is harmless. Keyed on `[ 'links', 'recent-changes', days, limit ]`
// so the badge update path (useMarkChangeSeen below) can target this query
// for invalidation without touching the main `['links']` cache.
export function useRecentChanges(days = 7, limit = 20) {
  return useQuery<Link[]>({
    queryKey: ['links', 'recent-changes', days, limit],
    queryFn: async () => {
      const { data } = await http.get<Link[]>(`/api/links/recent-changes?days=${days}&limit=${limit}`)
      return data
    },
    // Background refetch every minute so the sidebar reflects fresh
    // changes without requiring a full page reload.
    refetchInterval: 60_000,
  })
}

// useMarkChangeSeen flips change_seen_at on the link, which hides the
// "unseen update" badge in the card. Optimistic update mirrors the
// usePinLink pattern so the UI responds before the network roundtrip.
export function useMarkChangeSeen() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      await http.post(`/api/links/${id}/seen-change`)
    },
    onMutate: async (id) => {
      // Optimistic: bump change_seen_at to now() across every cached page
      // that holds this link. The server will overwrite with its own
      // timestamp on success.
      await qc.cancelQueries({ queryKey: ['links'] })
      const now = new Date().toISOString()
      qc.setQueriesData<Link[]>({ queryKey: ['links'] }, (prev) => {
        if (!prev) return prev
        return prev.map((l) => (l.id === id ? { ...l, change_seen_at: now } : l))
      })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['links', 'recent-changes'] })
    },
    onError: () => {
      // The optimistic write may now be wrong — full refetch is cheaper
      // than reconciling the touched ids manually.
      qc.invalidateQueries({ queryKey: ['links'] })
    },
  })
}

// Builds the public short link. Prefers the slug when given a Link object —
// `/go/jira-board` is the share-friendly path. Falls back to `/go/{id}` for
// callers that only have the numeric id (legacy, optimistic UI updates that
// don't yet have the slug, etc.). Backend resolution is ID-first then slug-
// fallback so both forms always work.
export function goHref(linkOrId: { id: number; slug: string } | number): string {
  if (typeof linkOrId === 'number') return `/go/${linkOrId}`
  return `/go/${linkOrId.slug || linkOrId.id}`
}
