import type { Folder, MergeSource } from './api/types'

export function wouldCreateFolderCycle(
  folders: Folder[],
  sourceId: number,
  targetId: number,
): boolean {
  if (sourceId === targetId) return true
  const childrenByParent = new Map<number, number[]>()
  for (const folder of folders) {
    if (folder.parent_id == null) continue
    const children = childrenByParent.get(folder.parent_id) ?? []
    children.push(folder.id)
    childrenByParent.set(folder.parent_id, children)
  }

  const stack = [...(childrenByParent.get(sourceId) ?? [])]
  const seen = new Set<number>()
  while (stack.length > 0) {
    const id = stack.pop() as number
    if (id === targetId) return true
    if (seen.has(id)) continue
    seen.add(id)
    stack.push(...(childrenByParent.get(id) ?? []))
  }
  return false
}

export function isSameEntry(a: MergeSource, b: MergeSource): boolean {
  return a.kind === b.kind && a.id === b.id
}
