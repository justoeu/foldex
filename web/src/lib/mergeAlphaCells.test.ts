import { describe, it, expect } from 'vitest'
import { mergeAlphaCells } from './mergeAlphaCells'
import type { Entry, Folder } from '../api/types'

function folder(id: number, name: string): Folder {
  return { id, name, color: '#000', link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: false }
}

function linkEntry(id: number, title: string): Entry {
  return {
    kind: 'link', id, url: 'https://x', title, slug: 'x', click_count: 0,
    preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
  }
}

function noteEntry(id: number, title: string): Entry {
  return {
    kind: 'note', id, title, slug: 'x', pinned: false, created_at: '', updated_at: '',
    click_count: 0, tags: [],
  }
}

describe('mergeAlphaCells', () => {
  it('interleaves folders, links, and notes by name (A→Z)', () => {
    const cells = mergeAlphaCells(
      [folder(1, 'Zebra folder')],
      [linkEntry(2, 'Apple link'), noteEntry(3, 'Mango note')],
      1,
    )
    expect(cells.map((c) => c.name)).toEqual(['Apple link', 'Mango note', 'Zebra folder'])
    expect(cells.map((c) => c.kind)).toEqual(['link', 'note', 'folder'])
  })

  it('reverses order for Z→A', () => {
    const cells = mergeAlphaCells(
      [folder(1, 'Beta folder')],
      [linkEntry(2, 'Alpha link')],
      -1,
    )
    expect(cells.map((c) => c.name)).toEqual(['Beta folder', 'Alpha link'])
  })

  it('is case-insensitive', () => {
    const cells = mergeAlphaCells([], [linkEntry(1, 'banana'), noteEntry(2, 'Apple')], 1)
    expect(cells.map((c) => c.name)).toEqual(['Apple', 'banana'])
  })

  it('returns an empty array for no folders/entries', () => {
    expect(mergeAlphaCells([], [], 1)).toEqual([])
  })

  it('carries the original entry/folder object on each cell', () => {
    const f = folder(1, 'F')
    const l = linkEntry(2, 'L')
    const cells = mergeAlphaCells([f], [l], 1)
    const folderCell = cells.find((c) => c.kind === 'folder')
    const linkCell = cells.find((c) => c.kind === 'link')
    expect(folderCell?.kind === 'folder' && folderCell.folder).toBe(f)
    expect(linkCell?.kind === 'link' && linkCell.entry).toBe(l)
  })
})
