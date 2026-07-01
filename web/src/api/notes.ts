import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { http } from './client'
import type { Note, NoteCreate, NoteUpdate } from './types'

export function useNote(id: number | null) {
  return useQuery({
    queryKey: ['notes', id],
    queryFn: async () => {
      const { data } = await http.get<Note>(`/api/notes/${id}`)
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
    onSuccess: (_data, vars) => {
      qc.invalidateQueries({ queryKey: ['entries'] })
      if (vars.body.tag_ids !== undefined) {
        qc.invalidateQueries({ queryKey: ['tags'] })
      }
      if ('folder_id' in vars.body) {
        qc.invalidateQueries({ queryKey: ['folders'] })
      }
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
    },
  })
}

// usePinNote mirrors usePinLink's optimistic recipe, but since the grid reads
// from ['entries'] (a flat Entry[] cache, not InfiniteData<Note[]>), the
// patch/rollback walks every cached ['entries'] page directly rather than
// going through a Note-specific cache helper.
export function usePinNote() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, pinned }: { id: number; pinned: boolean }) => {
      const { data } = await http.patch<Note>(`/api/notes/${id}`, { pinned })
      return data
    },
    onMutate: async ({ id, pinned }) => {
      await qc.cancelQueries({ queryKey: ['entries'] })
      const snapshots = qc.getQueriesData({ queryKey: ['entries'] })
      qc.setQueriesData<{ pages: Array<Array<{ kind: string; id: number; pinned: boolean }>> } | undefined>(
        { queryKey: ['entries'] },
        (old) => {
          if (!old) return old
          return {
            ...old,
            pages: old.pages.map((page) =>
              page.map((e) => (e.kind === 'note' && e.id === id ? { ...e, pinned } : e)),
            ),
          }
        },
      )
      return { snapshots }
    },
    onError: (_err, _vars, ctx) => {
      if (!ctx) return
      for (const [key, snapshot] of ctx.snapshots) {
        qc.setQueryData(key, snapshot)
      }
    },
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
