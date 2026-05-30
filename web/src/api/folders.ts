import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { http } from './client'
import type { Folder, FolderCreate, FolderUpdate } from './types'

export type FolderListParams = {
  // null = no filtering (flat list — used by LinkDialog folder picker);
  // 'root' = only top-level folders (home view);
  // number = only direct children of that folder id (folder view).
  scope?: 'root' | number | null
}

export function useFolders(params?: FolderListParams) {
  const scope = params?.scope ?? null
  return useQuery({
    queryKey: ['folders', scope],
    queryFn: async () => {
      const search = new URLSearchParams()
      if (scope === 'root') search.set('root', '1')
      else if (typeof scope === 'number') search.set('parent_id', String(scope))
      const qs = search.toString()
      const { data } = await http.get<Folder[]>(`/api/folders${qs ? '?' + qs : ''}`)
      return data
    },
    // The flat-list (`scope: null`) query is used by the LinkDialog folder
    // picker and the breadcrumb/folder-tree builder. It changes only on
    // CRUD (create/update/delete) — those mutations already invalidate
    // ['folders'], so a long staleTime here just avoids the parallel
    // refetch alongside the scoped query on first paint of Home.
    staleTime: scope === null ? 5 * 60_000 : 30_000,
  })
}

export function useCreateFolder() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: FolderCreate) => {
      const { data } = await http.post<Folder>('/api/folders', body)
      return data
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['folders'] })
      qc.invalidateQueries({ queryKey: ['links'] })
    },
  })
}

export function useUpdateFolder() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, body }: { id: number; body: FolderUpdate }) => {
      const { data } = await http.patch<Folder>(`/api/folders/${id}`, body)
      return data
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['folders'] })
      qc.invalidateQueries({ queryKey: ['links'] })
    },
  })
}

// Deleting a folder defaults to `ON DELETE SET NULL` on `link.folder_id`
// server-side (links survive and unflag back to ungrouped). Passing
// `{ cascade: true }` flips to `?cascade=1`, which deletes every link inside
// the folder too (transactional). Both modes invalidate ['folders'] AND
// ['links'] since membership / counts shift.
export function useDeleteFolder() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (args: number | { id: number; cascade?: boolean }) => {
      const id = typeof args === 'number' ? args : args.id
      const cascade = typeof args === 'object' && args.cascade ? '?cascade=1' : ''
      await http.delete(`/api/folders/${id}${cascade}`)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['folders'] })
      qc.invalidateQueries({ queryKey: ['links'] })
    },
  })
}
