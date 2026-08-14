import type { Folder } from '../api/types'

export type FolderPickerRow =
  | { kind: 'create'; label: string }
  | { kind: 'none'; label: string }
  | { kind: 'folder'; id: number; label: string; hasPassword: boolean }

type Labels = {
  create: (name: string) => string
  createEmpty: string
  none: string
}

export function buildFolderPickerRows(
  folders: Folder[],
  filter: string,
  labels: Labels,
): FolderPickerRow[] {
  const trimmed = filter.trim()
  const normalized = trimmed.toLowerCase()
  const exactMatch = folders.some((folder) => folder.name.toLowerCase() === normalized)
  const filtered = normalized
    ? folders.filter((folder) => folder.name.toLowerCase().includes(normalized))
    : folders
  const rows: FolderPickerRow[] = []
  if (!exactMatch) {
    rows.push({ kind: 'create', label: trimmed ? labels.create(trimmed) : labels.createEmpty })
  }
  rows.push({ kind: 'none', label: labels.none })
  for (const folder of filtered) {
    rows.push({
      kind: 'folder',
      id: folder.id,
      label: folder.name,
      hasPassword: folder.has_password,
    })
  }
  return rows
}

export function nextFolderHighlight(
  current: number,
  key: 'ArrowDown' | 'ArrowUp',
  rowCount: number,
): number {
  if (key === 'ArrowDown') return Math.min(rowCount - 1, current + 1)
  return Math.max(0, current - 1)
}
