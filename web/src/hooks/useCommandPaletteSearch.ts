import { useEffect, useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { goHref } from '../api/links'
import { collectCachedEntries, flattenEntries, useEntries } from '../api/entries'
import { useTags } from '../api/tags'
import { useFolders } from '../api/folders'
import { searchFolderTree } from '../lib/folderTree'

// Search paints at most 12 links + 12 notes. Fetching 200 rows to fill that
// is pure overfetch; 24 mixed rows is enough to fill both lists.
const PALETTE_SEARCH_LIMIT = 24

export const PALETTE_DEBOUNCE_MS = 200

// One keyboard-navigable action per activatable row, flattened in render
// order. Notes open in-app (INV-130) and tag rows filter, so both are real
// actions now.
export type PaletteAction =
  | { kind: 'link'; href: string }
  | { kind: 'note'; id: number }
  | { kind: 'folder'; id: number }
  | { kind: 'tag'; id: number; name: string }

export function useCommandPaletteSearch(open: boolean) {
  const [q, setQ] = useState('')
  const [debounced, setDebounced] = useState('')

  useEffect(() => {
    if (!open) {
      setQ('')
      setDebounced('')
      return
    }
    const id = setTimeout(() => setDebounced(q), PALETTE_DEBOUNCE_MS)
    return () => clearTimeout(id)
  }, [open, q])

  const qc = useQueryClient()
  const searching = debounced.length > 0
  // Empty-open reuses Home's ['entries'] cache (client-side top-clicks).
  // Search is a slim page, still with qSettled so the palette's 200ms
  // debounce is not stacked on useEntries' 500ms Home debounce.
  const searchQuery = useEntries(
    { q: debounced, limit: PALETTE_SEARCH_LIMIT },
    { enabled: open && searching, qSettled: true },
  )
  const cached = !open || searching ? [] : collectCachedEntries(qc)
  const entries = searching ? flattenEntries(searchQuery.data) : cached
  const links = useMemo(() => entries.filter((e) => e.kind === 'link'), [entries])
  const notes = useMemo(() => entries.filter((e) => e.kind === 'note'), [entries])
  const { data: tags = [] } = useTags()
  const { data: folders = [] } = useFolders({ fields: 'minimal' })

  const suggested = useMemo(() => [...links].sort((a, b) => b.click_count - a.click_count).slice(0, 3), [links])
  const matches = useMemo(() => links.slice(0, 12), [links])
  const noteMatches = useMemo(() => notes.slice(0, 12), [notes])
  const tagMatches = useMemo(() => {
    if (!debounced) return []
    const f = debounced.toLowerCase()
    return tags.filter((tag) => tag.name.toLowerCase().includes(f)).slice(0, 4)
  }, [tags, debounced])
  // Hierarchical folder rendering — depth-aware so the user sees the tree
  // shape, with the full ancestor path attached to each match for context
  // when the search query lands deep in the tree.
  const folderMatches = useMemo(() => {
    if (!folders.length) return []
    return searchFolderTree(folders, debounced).slice(0, 8)
  }, [folders, debounced])

  const folderNameById = useMemo(() => {
    const map = new Map<number, string>()
    for (const folder of folders) map.set(folder.id, folder.name)
    return map
  }, [folders])

  const actions = useMemo<PaletteAction[]>(() => [
    ...(!debounced ? suggested.map((l) => ({ kind: 'link' as const, href: goHref(l) })) : []),
    ...matches.map((l) => ({ kind: 'link' as const, href: goHref(l) })),
    ...noteMatches.map((n) => ({ kind: 'note' as const, id: n.id })),
    ...folderMatches.map((f) => ({ kind: 'folder' as const, id: f.id })),
    ...tagMatches.map((tag) => ({ kind: 'tag' as const, id: tag.id, name: tag.name })),
  ], [debounced, suggested, matches, noteMatches, folderMatches, tagMatches])

  // Action offsets in render order, so rows and the keyboard cursor share
  // one numbering (links, then notes, then folders; suggested replaces the
  // links block while the query is empty).
  const linksOffset = debounced ? 0 : suggested.length
  const notesOffset = linksOffset + matches.length
  const foldersOffset = notesOffset + noteMatches.length
  const tagsOffset = foldersOffset + folderMatches.length

  // Derived once so the empty state cannot fall out of sync with the group
  // list — a new group only extends `hasResults`, never a hand-written
  // conjunction at the render site.
  const hasResults =
    matches.length + noteMatches.length + tagMatches.length + folderMatches.length > 0

  return {
    q,
    setQ,
    debounced,
    entries,
    suggested,
    matches,
    noteMatches,
    tagMatches,
    folderMatches,
    folderNameById,
    actions,
    linksOffset,
    notesOffset,
    foldersOffset,
    tagsOffset,
    hasResults,
  }
}
