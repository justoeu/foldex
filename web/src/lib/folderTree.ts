import type { Folder } from '../api/types'

export type FolderNode = Folder & { depth: number; path: string[] }

// Flattens a list of folders into tree-traversal order (parents before
// children), enriched with depth (root=0) and the full ancestor path of
// names (last entry is the folder itself). Used by the CommandPalette to
// render hierarchical results with indentation + breadcrumbs.
export function flattenFolderTree(folders: Folder[]): FolderNode[] {
  const byParent = new Map<number | null, Folder[]>()
  for (const f of folders) {
    const key = f.parent_id ?? null
    const arr = byParent.get(key)
    if (arr) arr.push(f)
    else byParent.set(key, [f])
  }
  for (const arr of byParent.values()) {
    // Use the active locale (undefined = browser default, which matches
     // the user's i18n choice). Hardcoding 'pt-BR' here biased sort order
     // for English/Spanish users; the rest of the codebase already uses
     // locale-aware compare with undefined.
    arr.sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }))
  }
  const out: FolderNode[] = []
  function walk(parentId: number | null, depth: number, ancestors: string[]) {
    const children = byParent.get(parentId) ?? []
    for (const child of children) {
      const path = [...ancestors, child.name]
      out.push({ ...child, depth, path })
      walk(child.id, depth + 1, path)
    }
  }
  walk(null, 0, [])
  return out
}

// Filters a flat folder list by name match and returns the matches enriched
// with depth + path (computed from the original tree, not from the filtered
// subset, so the breadcrumb stays meaningful even when ancestors are not
// themselves matches).
export function searchFolderTree(folders: Folder[], query: string): FolderNode[] {
  const tree = flattenFolderTree(folders)
  if (!query) return tree
  const q = query.toLowerCase()
  return tree.filter((n) => n.name.toLowerCase().includes(q))
}
