import { useMutation, useQueryClient } from '@tanstack/react-query'
import { http } from './client'

export type ImportFormat = 'netscape' | 'json'
export type ImportMode = 'skip' | 'wipe' | 'duplicate'

export type ImportCounts = {
  links: number
  folders: number
  tags: number
}

export type ImportFolder = {
  path: string
  name: string
  count: number
}

export type ImportLink = {
  url: string
  title: string
  folder?: string
  tags?: string[]
  conflict: boolean
}

export type ImportValidation = {
  format: ImportFormat
  counts: ImportCounts
  conflicts: ImportCounts
  folders: ImportFolder[]
  links: ImportLink[]
  warnings: string[]
}

export type ImportResult = {
  format: ImportFormat
  mode: ImportMode
  imported: number
  skipped: number
  wiped: number
  warnings?: string[]
}

export async function validateImport(file: File, format: ImportFormat): Promise<ImportValidation> {
  const fd = new FormData()
  fd.append('file', file)
  fd.append('format', format)
  const { data } = await http.post<ImportValidation>('/api/import/validate', fd, {
    headers: { 'Content-Type': 'multipart/form-data' },
  })
  return data
}

export async function applyImport(
  file: File,
  format: ImportFormat,
  mode: ImportMode,
  excludeFolders: string[],
): Promise<ImportResult> {
  const fd = new FormData()
  fd.append('file', file)
  fd.append('format', format)
  fd.append('mode', mode)
  if (excludeFolders.length > 0) fd.append('exclude_folders', excludeFolders.join(','))
  const { data } = await http.post<ImportResult>('/api/import/apply', fd, {
    headers: { 'Content-Type': 'multipart/form-data' },
  })
  return data
}

// useApplyImport wraps the bare applyImport call with the mutation lifecycle
// + cache invalidation. Without this the user landed on Home after an import
// and saw stale link/folder/tag data for up to 30 s (the global staleTime).
export function useApplyImport() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (args: {
      file: File
      format: ImportFormat
      mode: ImportMode
      excludeFolders: string[]
    }) => applyImport(args.file, args.format, args.mode, args.excludeFolders),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['links'] })
      qc.invalidateQueries({ queryKey: ['folders'] })
      qc.invalidateQueries({ queryKey: ['tags'] })
      qc.invalidateQueries({ queryKey: ['stats'] })
    },
  })
}
