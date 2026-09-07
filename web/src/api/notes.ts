import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { http } from './client'
import { cachedEntryFolderId, invalidateEntryCounts, optimisticEntryPatch, removeCachedEntry } from './entries'
import type { Note, NoteCreate, NoteUpdate } from './types'

export function useNote(id: number | null) {
  return useQuery({
    queryKey: ['notes', id],
    queryFn: async ({ signal }) => {
      const { data } = await http.get<Note>(`/api/notes/${id}`, { signal })
      return data
    },
    enabled: id != null,
  })
}

export function useCreateNote() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: NoteCreate) => {
      const { data } = await http.post<Note>('/api/notes', body)
      return data
    },
    onSuccess: () => {
      // The home/folder grid reads from ['entries'], not ['notes'] — every
      // note mutation invalidates that key so the interleaved grid reflects
      // the change. See api/entries.ts.
      qc.invalidateQueries({ queryKey: ['entries'] })
      qc.invalidateQueries({ queryKey: ['tags'] })
      qc.invalidateQueries({ queryKey: ['folders'] })
      invalidateEntryCounts(qc)
    },
  })
}

export function useUpdateNote() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, body }: { id: number; body: NoteUpdate }) => {
      const { data } = await http.patch<Note>(`/api/notes/${id}`, body)
      return data
    },
    onMutate: ({ id, body }) => ({
      previousFolderId: 'folder_id' in body ? cachedEntryFolderId(qc, 'note', id) : undefined,
    }),
    onSuccess: (data, vars, context) => {
      if (vars.body.tag_ids !== undefined || vars.body.pending_tags !== undefined) {
        qc.invalidateQueries({ queryKey: ['tags'] })
      }
      const folderMoved = 'folder_id' in vars.body && data.folder_id !== context?.previousFolderId
      if (folderMoved) {
        removeCachedEntry(qc, 'note', data.id)
        qc.invalidateQueries({ queryKey: ['folders'] })
      }
      qc.invalidateQueries({ queryKey: ['entries'] })
    },
  })
}

export function useDeleteNote() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (id: number) => {
      await http.delete(`/api/notes/${id}`)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['entries'] })
      qc.invalidateQueries({ queryKey: ['tags'] })
      qc.invalidateQueries({ queryKey: ['folders'] })
      invalidateEntryCounts(qc)
    },
  })
}

// usePinNote shares optimisticEntryPatch with usePinLink/useRefreshPreview;
// notes only exist in the ['entries'] caches, which the helper keys on
// kind === 'note'.
export function usePinNote() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, pinned }: { id: number; pinned: boolean }) => {
      const { data } = await http.patch<Note>(`/api/notes/${id}`, { pinned })
      return data
    },
    onMutate: async ({ id, pinned }) => optimisticEntryPatch(qc, 'note', id, { pinned }),
    onError: (_err, _vars, ctx) => ctx?.rollback(),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: ['entries'] })
      qc.invalidateQueries({ queryKey: ['folders'] })
    },
  })
}

export async function uploadNoteImage(file: File): Promise<{ url: string }> {
  const fd = new FormData()
  fd.append('image', file)
  const { data } = await http.post<{ url: string }>('/api/notes/images', fd, {
    headers: { 'Content-Type': 'multipart/form-data' },
  })
  return data
}

// Builds the public note page URL. Prefers the slug — mirrors goHref's
// id-or-slug fallback shape for links.
export function goNoteHref(noteOrId: { id: number; slug: string } | number): string {
  if (typeof noteOrId === 'number') return `/n/${noteOrId}`
  return `/n/${noteOrId.slug || noteOrId.id}`
}
