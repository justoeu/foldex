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
    (p.tagIds ?? []).join(','),
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

// Builds the public short link. Prefers the slug when given a Link object —
// `/go/jira-board` is the share-friendly path. Falls back to `/go/{id}` for
// callers that only have the numeric id (legacy, optimistic UI updates that
// don't yet have the slug, etc.). Backend resolution is ID-first then slug-
// fallback so both forms always work.
export function goHref(linkOrId: { id: number; slug: string } | number): string {
  if (typeof linkOrId === 'number') return `/go/${linkOrId}`
  return `/go/${linkOrId.slug || linkOrId.id}`
}
