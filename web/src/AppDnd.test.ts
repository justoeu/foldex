import { describe, expect, it } from 'vitest'
import { isSameEntry, wouldCreateFolderCycle } from './AppDnd'
import type { Folder } from './api/types'

const folders = [
  { id: 1, parent_id: null },
  { id: 2, parent_id: 1 },
  { id: 3, parent_id: 2 },
  { id: 4, parent_id: null },
] as Folder[]

describe('App drag-and-drop contracts', () => {
  it('rejects self moves and targets anywhere below the source folder', () => {
    expect(wouldCreateFolderCycle(folders, 1, 1)).toBe(true)
    expect(wouldCreateFolderCycle(folders, 1, 2)).toBe(true)
    expect(wouldCreateFolderCycle(folders, 1, 3)).toBe(true)
  })

  it('allows moves to ancestors and disconnected folders', () => {
    expect(wouldCreateFolderCycle(folders, 3, 1)).toBe(false)
    expect(wouldCreateFolderCycle(folders, 2, 4)).toBe(false)
  })

  it('treats only the same kind and id as a no-op merge', () => {
    expect(isSameEntry({ kind: 'link', id: 7 }, { kind: 'link', id: 7 })).toBe(true)
    expect(isSameEntry({ kind: 'link', id: 7 }, { kind: 'note', id: 7 })).toBe(false)
    expect(isSameEntry({ kind: 'link', id: 7 }, { kind: 'link', id: 8 })).toBe(false)
  })
})
