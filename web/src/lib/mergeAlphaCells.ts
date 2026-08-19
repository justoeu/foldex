import type { Entry, Folder } from '../api/types'

export type AlphaCell =
  | { kind: 'folder'; name: string; folder: Folder }
  | { kind: 'link'; name: string; entry: Extract<Entry, { kind: 'link' }> }
  | { kind: 'note'; name: string; entry: Extract<Entry, { kind: 'note' }> }

// Alpha sort (A→Z / Z→A) interleaves folders and entries (links + notes) by
// name/title so the order is honest — CardsView/ListView/CompactGrid all
// share this instead of each re-implementing the same 3-way cell union.
//
// Pinned entries are sorted into their own block ahead of everything else,
// mirroring the backend, whose alpha plan is `pinned DESC, lower(title) ASC`
// (internal/pkg/listquery.Planner.AddPage). This helper used to claim the
// server's ordering carried through and then flatten every cell into a single
// name sort — which discarded it, and put a pinned "Zebra" below an unpinned
// "Apple" in the one sort mode where CLAUDE.md §5 says pinned still comes
// first. Re-deriving the rule here rather than trusting the incoming order is
// deliberate: folders arrive from a different query and have to be merged in,
// so the entries' relative order cannot survive the merge on its own.
//
// Folders sit in the second block because a folder cannot be pinned at all —
// there is no such column. Within each block the interleave is by name, so
// alphabetical order stays honest exactly where it can be.
//
// `sensitivity: 'base'` only decides ties between names differing solely by
// case or accent ("resume" vs "résumé"); it is NOT what makes the comparison
// case-insensitive — `localeCompare` already is. Left untested on purpose: a
// test for it would have to assert stable-tie order, which is more brittle
// than the option is dangerous.
export function mergeAlphaCells(folders: Folder[], entries: Entry[], dir: 1 | -1): AlphaCell[] {
  const toCell = (e: Entry): AlphaCell =>
    e.kind === 'link' ? { kind: 'link', name: e.title, entry: e } : { kind: 'note', name: e.title, entry: e }
  const byName = (a: AlphaCell, b: AlphaCell) =>
    dir * a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })

  const pinned = entries.filter((e) => e.pinned).map(toCell).sort(byName)
  const rest: AlphaCell[] = [
    ...folders.map<AlphaCell>((f) => ({ kind: 'folder', name: f.name, folder: f })),
    ...entries.filter((e) => !e.pinned).map(toCell),
  ].sort(byName)
  return [...pinned, ...rest]
}
