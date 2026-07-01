import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ListView } from './ListView'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import type { Entry, Folder, Link } from '../api/types'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

const baseLink: Link = {
  id: 1, url: 'https://a.example', title: 'A link', slug: 'a-link', click_count: 2,
  preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
}

const baseNote: Entry = {
  kind: 'note', id: 2, title: 'A note', slug: 'a-note', pinned: false,
  created_at: '', updated_at: '', click_count: 5, tags: [], body_text_snippet: 'a snippet',
}

const baseFolder: Folder = {
  id: 3, name: 'A folder', color: '#000', link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: false,
}

describe('ListView', () => {
  it('renders links, notes, and folders as rows', () => {
    renderWithProviders(
      <ListView
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

  it('interleaves by name in alpha sort', () => {
    renderWithProviders(
      <ListView
        folders={[{ ...baseFolder, name: 'Zebra folder' }]}
        entries={[{ kind: 'link', ...baseLink, title: 'Apple link' }, { ...baseNote, title: 'Mango note' }]}
        sort="alpha"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    const rows = document.querySelectorAll('.fx-list-row')
    const titles = Array.from(rows).map((r) => r.textContent ?? '')
    expect(titles[0]).toMatch(/Apple link/)
    expect(titles[1]).toMatch(/Mango note/)
    expect(titles[2]).toMatch(/Zebra folder/)
  })

  it('calls onEditNote when a note title is clicked', async () => {
    const onEditNote = vi.fn()
    renderWithProviders(
      <ListView
        folders={[]}
        entries={[baseNote]}
        sort="created"
        onEdit={vi.fn()}
        onEditNote={onEditNote}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    const user = userEvent.setup()
    await user.click(screen.getByText('A note'))
    expect(onEditNote).toHaveBeenCalledWith(2)
  })
})
