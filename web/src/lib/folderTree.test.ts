import { describe, it, expect } from 'vitest'
import { flattenFolderTree, searchFolderTree } from './folderTree'
import type { Folder } from '../api/types'

function f(id: number, name: string, parent_id: number | null = null): Folder {
  return {
    id,
    name,
    color: '#000',
    parent_id,
    link_count: 0,
    folder_count: 0,
    preview_links: [],
    preview_folders: [],
    has_password: false,
  }
}

describe('flattenFolderTree', () => {
  it('returns empty array for empty input', () => {
    expect(flattenFolderTree([])).toEqual([])
  })

  it('returns root folders sorted alphabetically with depth=0', () => {
    const out = flattenFolderTree([f(2, 'Bravo'), f(1, 'Alpha')])
    expect(out.map((n) => n.name)).toEqual(['Alpha', 'Bravo'])
    expect(out.every((n) => n.depth === 0)).toBe(true)
  })

  it('walks children depth-first under each parent', () => {
    const folders = [
      f(1, 'Root'),
      f(2, 'Child', 1),
      f(3, 'Grand', 2),
    ]
    const out = flattenFolderTree(folders)
    expect(out.map((n) => `${n.depth}:${n.name}`)).toEqual([
      '0:Root',
      '1:Child',
      '2:Grand',
    ])
  })

  it('builds the breadcrumb path from root to self', () => {
    const folders = [
      f(1, 'Trabalho'),
      f(2, 'Projetos', 1),
      f(3, 'Backend', 2),
    ]
    const out = flattenFolderTree(folders)
    const grand = out.find((n) => n.name === 'Backend')!
    expect(grand.path).toEqual(['Trabalho', 'Projetos', 'Backend'])
  })

  it('drops a child whose parent_id points at a non-existent folder', () => {
    const out = flattenFolderTree([f(1, 'Orphan', 999)])
    expect(out).toEqual([])
  })
})

describe('searchFolderTree', () => {
  it('returns the full tree when query is empty', () => {
    const out = searchFolderTree([f(1, 'A'), f(2, 'B', 1)], '')
    expect(out.map((n) => n.name)).toEqual(['A', 'B'])
  })

  it('filters by case-insensitive substring', () => {
    const folders = [
      f(1, 'Trabalho'),
      f(2, 'Projetos', 1),
      f(3, 'Pessoal'),
    ]
    const out = searchFolderTree(folders, 'pro')
    expect(out.map((n) => n.name)).toEqual(['Projetos'])
  })

  it('preserves the full ancestor path on a deep match', () => {
    const folders = [
      f(1, 'Trabalho'),
      f(2, 'Engenharia', 1),
      f(3, 'Backend', 2),
    ]
    const out = searchFolderTree(folders, 'backend')
    expect(out).toHaveLength(1)
    expect(out[0].path).toEqual(['Trabalho', 'Engenharia', 'Backend'])
    expect(out[0].depth).toBe(2)
  })
})
