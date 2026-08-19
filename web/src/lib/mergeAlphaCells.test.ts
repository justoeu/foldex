import { describe, it, expect } from 'vitest'
import { mergeAlphaCells } from './mergeAlphaCells'
import type { Entry, Folder } from '../api/types'

function folder(id: number, name: string): Folder {
  return { id, name, color: '#000', link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: false }
}

function linkEntry(id: number, title: string, pinned = false): Entry {
  return {
    kind: 'link', id, url: 'https://x', title, slug: 'x', click_count: 0,
    preview_status: 'ok', pinned, created_at: '', updated_at: '', tags: [],
  }
}

function noteEntry(id: number, title: string, pinned = false): Entry {
  return {
    kind: 'note', id, title, slug: 'x', pinned, created_at: '', updated_at: '',
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

  // The fixture has to be one where a codepoint comparator DISAGREES. With
  // ('banana', 'Apple') it does not: 'A' (0x41) already sorts before 'b'
  // (0x62), so `a.name < b.name` produces the same answer and the test named
  // its property without being able to fail on it. ('Zebra', 'apple') splits
  // them — codepoint puts every uppercase title above every lowercase one.
  it('is case-insensitive', () => {
    const cells = mergeAlphaCells([], [linkEntry(1, 'Zebra'), noteEntry(2, 'apple')], 1)
    expect(cells.map((c) => c.name)).toEqual(['apple', 'Zebra'])
  })

  it('returns an empty array for no folders/entries', () => {
    expect(mergeAlphaCells([], [], 1)).toEqual([])
  })

  // CLAUDE.md §5: "Pinned links always come first — applies in every sort
  // mode." Alpha was the one mode where it did not: the helper flattened every
  // cell into a single name sort, discarding the `pinned DESC` the backend had
  // already applied. Every fixture in this file used to be unpinned, so nothing
  // noticed.
  it('keeps pinned entries ahead of everything else (A→Z)', () => {
    const cells = mergeAlphaCells(
      [folder(1, 'Alpha folder')],
      [linkEntry(2, 'Apple link'), linkEntry(3, 'Zebra link', true), noteEntry(4, 'Beta note')],
      1,
    )
    expect(cells.map((c) => c.name)).toEqual(['Zebra link', 'Alpha folder', 'Apple link', 'Beta note'])
  })

  it('keeps pinned entries first when the direction is reversed too', () => {
    const cells = mergeAlphaCells(
      [folder(1, 'Zulu folder')],
      [linkEntry(2, 'Yankee link'), noteEntry(3, 'Alpha note', true)],
      -1,
    )
    expect(cells.map((c) => c.name)).toEqual(['Alpha note', 'Zulu folder', 'Yankee link'])
  })

  // Green before the fix too — the old flat sort ordered everything, so the
  // pinned entries happened to come out sorted. That says nothing about its
  // value NOW: it is the only test that fails if the pinned block loses its
  // sort, and the Z→A twin below is the only one that fails if that block
  // ignores the direction. Neither is redundant; do not delete them for
  // "passing before the fix".
  it('sorts the pinned block alphabetically among itself', () => {
    const cells = mergeAlphaCells(
      [],
      [linkEntry(1, 'Charlie', true), noteEntry(2, 'Alpha', true), linkEntry(3, 'Bravo', true)],
      1,
    )
    expect(cells.map((c) => c.name)).toEqual(['Alpha', 'Bravo', 'Charlie'])
  })

  it('sorts the pinned block in Z→A when the direction is reversed', () => {
    const cells = mergeAlphaCells(
      [],
      [linkEntry(1, 'Alpha', true), noteEntry(2, 'Bravo', true), linkEntry(3, 'Charlie', true)],
      -1,
    )
    expect(cells.map((c) => c.name)).toEqual(['Charlie', 'Bravo', 'Alpha'])
  })

  // The block boundary is a rule, not a tiebreak: a pinned entry outranks an
  // unpinned one of the SAME name, where a single sort would leave the two in
  // whatever order the input happened to have.
  it('puts a pinned entry ahead of an unpinned one with the same name', () => {
    const cells = mergeAlphaCells([], [linkEntry(1, 'Same'), noteEntry(2, 'Same', true)], 1)
    expect(cells.map((c) => c.kind)).toEqual(['note', 'link'])
  })

  // A folder has no `pinned` column, so it can never join the first block —
  // which is why the pinned entries are allowed to jump ahead of it here even
  // though alpha otherwise interleaves the two kinds.
  it('never promotes a folder into the pinned block', () => {
    const cells = mergeAlphaCells([folder(1, 'Aaa folder')], [linkEntry(2, 'Zzz link', true)], 1)
    expect(cells.map((c) => c.kind)).toEqual(['link', 'folder'])
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
