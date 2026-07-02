import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { http } from './client'
import type { Folder, FolderCreate, FolderUpdate } from './types'

// Header carrying a folder unlock token — matches folders.UnlockHeader /
// entries.FolderPasswordLookup on the backend (ADR-28).
export const FOLDER_UNLOCK_HEADER = 'X-Foldex-Folder-Unlock'

export type FolderListParams = {
  // null = no filtering (flat list — used by LinkDialog folder picker);
  // 'root' = only top-level folders (home view);
  // number = only direct children of that folder id (folder view).
  scope?: 'root' | number | null
  // Only meaningful when scope is a number AND that folder is protected —
  // the backend gates GET /api/folders?parent_id=X on this exact case
  // (root/flat listings are never gated, only "list what's inside X").
  unlockToken?: string
}

export function useFolders(params?: FolderListParams) {
  const scope = params?.scope ?? null
  const unlockToken = params?.unlockToken
  return useQuery({
    // The token itself isn't part of the key (a fresh unlock shouldn't
    // bust the cache) — only whether one is present, so the locked→
    // unlocked transition still triggers a refetch.
    queryKey: ['folders', scope, unlockToken ? 'unlocked' : 'locked'],
    queryFn: async () => {
      const search = new URLSearchParams()
      if (scope === 'root') search.set('root', '1')
      else if (typeof scope === 'number') search.set('parent_id', String(scope))
      const qs = search.toString()
      const { data } = await http.get<Folder[]>(`/api/folders${qs ? '?' + qs : ''}`, {
        headers: unlockToken ? { [FOLDER_UNLOCK_HEADER]: unlockToken } : undefined,
      })
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

export type FolderUnlockResult = { unlock_token: string; expires_at: string }

// Verifies a folder's password and returns a short-lived unlock token (see
// ADR-28). No cache invalidation — this mutation doesn't change folder/link
// data, it just proves the caller knows the password.
export function useUnlockFolder() {
  return useMutation({
    mutationFn: async ({ id, password }: { id: number; password: string }) => {
      const { data } = await http.post<FolderUnlockResult>(`/api/folders/${id}/unlock`, { password })
      return data
    },
  })
}

// Master-password RECOVERY (ADR-29): clears a folder's password + hint after
// the backend verifies the master password, so a new one can be set. Never
// unlocks the folder for viewing. Invalidates ['folders']/['entries'] since
// has_password/redaction flip.
export function useResetFolderPassword() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, masterPassword }: { id: number; masterPassword: string }) => {
      await http.post(`/api/folders/${id}/reset-password`, { master_password: masterPassword })
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['folders'] })
      qc.invalidateQueries({ queryKey: ['entries'] })
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
