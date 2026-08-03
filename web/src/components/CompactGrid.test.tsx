import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CompactGrid } from './CompactGrid'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import type { Entry, Folder, Link } from '../api/types'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

const tag = { id: 1, name: 'work', color: '#6366F1' }

const baseLink: Link = {
  id: 1, url: 'https://a.example', title: 'A link', slug: 'a-link', click_count: 2,
  preview_status: 'ok', pinned: false, created_at: '', updated_at: '',
  tags: [tag, { id: 2, name: 'x', color: '#000' }, { id: 3, name: 'y', color: '#111' }],
}

const baseNote: Entry = {
  kind: 'note', id: 2, title: 'A note', slug: 'a-note', pinned: false,
  created_at: '', updated_at: '', click_count: 5, tags: [tag], body_text_snippet: 'a snippet',
}

const baseFolder: Folder = {
  id: 3, name: 'A folder', color: '#000', link_count: 2, folder_count: 0,
  preview_links: [], preview_folders: [], has_password: false,
}

const locked: Folder = {
  ...baseFolder, id: 4, name: 'Secret', has_password: true,
  color: 'linear-gradient(135deg, #6366F1, #EC4899)',
}

describe('CompactGrid', () => {
  it('renders links, notes, and folders as compact tiles', () => {
    renderWithProviders(
      <CompactGrid
        folders={[baseFolder]}
        entries={[{ kind: 'link', ...baseLink }, baseNote]}
        sort="created"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    expect(screen.getByText('A link')).toBeInTheDocument()
    expect(screen.getByText('A note')).toBeInTheDocument()
    expect(screen.getByText('a snippet')).toBeInTheDocument()
    expect(screen.getByText('A folder')).toBeInTheDocument()
  })

  it('interleaves by name in alpha and alpha_desc', () => {
    const { unmount } = renderWithProviders(
      <CompactGrid
        folders={[{ ...baseFolder, name: 'Zebra folder' }]}
        entries={[{ kind: 'link', ...baseLink, title: 'Apple link' }, { ...baseNote, title: 'Mango note' }]}
        sort="alpha"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    let tiles = document.querySelectorAll('.fx-compact')
    let titles = Array.from(tiles).map((t) => t.textContent ?? '')
    expect(titles[0]).toMatch(/Apple link/)
    expect(titles[2]).toMatch(/Zebra folder/)
    unmount()

    renderWithProviders(
      <CompactGrid
        folders={[{ ...baseFolder, name: 'Zebra folder' }]}
        entries={[{ kind: 'link', ...baseLink, title: 'Apple link' }, { ...baseNote, title: 'Mango note' }]}
        sort="alpha_desc"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    tiles = document.querySelectorAll('.fx-compact')
    titles = Array.from(tiles).map((t) => t.textContent ?? '')
    expect(titles[0]).toMatch(/Zebra folder/)
  })

  it('calls onEdit / onEditNote / onOpenFolder / onEditFolder', async () => {
    const onEdit = vi.fn()
    const onEditNote = vi.fn()
    const onOpenFolder = vi.fn()
    const onEditFolder = vi.fn()
    renderWithProviders(
      <CompactGrid
        folders={[baseFolder, locked]}
        entries={[{ kind: 'link', ...baseLink }, baseNote]}
        sort="created"
        onEdit={onEdit}
        onEditNote={onEditNote}
        onOpenFolder={onOpenFolder}
        onEditFolder={onEditFolder}
      />,
    )
    const user = userEvent.setup()
    await user.click(screen.getByText('A link'))
    expect(onEdit).toHaveBeenCalledWith(expect.objectContaining({ id: 1 }))
    await user.click(screen.getByText('A note'))
    expect(onEditNote).toHaveBeenCalledWith(2)
    await user.click(screen.getByLabelText(/open folder.*a folder/i))
    expect(onOpenFolder).toHaveBeenCalledWith(3)
    await user.click(screen.getByLabelText(/edit folder.*secret/i))
    expect(onEditFolder).toHaveBeenCalledWith(expect.objectContaining({ id: 4 }))
    expect(document.querySelector('.fx-folder-lock-icon')).toBeTruthy()
  })

  it('double-clicks folder to open', async () => {
    const onOpenFolder = vi.fn()
    renderWithProviders(
      <CompactGrid
        folders={[baseFolder]}
        entries={[]}
        sort="created"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={onOpenFolder}
        onEditFolder={vi.fn()}
      />,
    )
    await userEvent.setup().dblClick(document.querySelector('.fx-compact-folder')!)
    expect(onOpenFolder).toHaveBeenCalledWith(3)
  })

  it('caps tags at 2 chips and exposes open hrefs', () => {
    renderWithProviders(
      <CompactGrid
        folders={[]}
        entries={[{ kind: 'link', ...baseLink }, baseNote]}
        sort="created"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    expect(screen.queryByText('y')).toBeNull()
    const anchors = screen.getAllByRole('link') as HTMLAnchorElement[]
    expect(anchors.some((a) => a.getAttribute('href')?.includes('/go/'))).toBe(true)
    expect(anchors.some((a) => a.getAttribute('href')?.includes('/n/'))).toBe(true)
  })
})
