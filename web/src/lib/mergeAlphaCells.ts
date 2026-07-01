import type { Entry, Folder } from '../api/types'

export type AlphaCell =
  | { kind: 'folder'; name: string; folder: Folder }
  | { kind: 'link'; name: string; entry: Extract<Entry, { kind: 'link' }> }
  | { kind: 'note'; name: string; entry: Extract<Entry, { kind: 'note' }> }

// Alpha sort (A→Z / Z→A) interleaves folders and entries (links + notes) by
// name/title so the order is honest — CardsView/ListView/CompactGrid all
// share this instead of each re-implementing the same 3-way cell union.
// Pinned entries still sort first within their own group via the backend's
// `pinned DESC` prefix (see internal/entries.Repository.List); this helper
// only handles the alpha ordering across kinds.
export function mergeAlphaCells(folders: Folder[], entries: Entry[], dir: 1 | -1): AlphaCell[] {
  const cells: AlphaCell[] = [
    ...folders.map<AlphaCell>((f) => ({ kind: 'folder', name: f.name, folder: f })),
    ...entries.map<AlphaCell>((e) =>
      e.kind === 'link' ? { kind: 'link', name: e.title, entry: e } : { kind: 'note', name: e.title, entry: e },
    ),
  ]
  cells.sort((a, b) => dir * a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }))
  return cells
}
