import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
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

const tag = { id: 1, name: 'work', color: '#6366F1' }

const baseLink: Link = {
  id: 1, url: 'https://a.example', title: 'A link', slug: 'a-link', click_count: 2,
  preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [tag, { id: 2, name: 'x', color: '#000' }, { id: 3, name: 'y', color: '#111' }, { id: 4, name: 'z', color: '#222' }],
  last_clicked_at: new Date(Date.now() - 5 * 60_000).toISOString(),
}

const baseNote: Entry = {
  kind: 'note', id: 2, title: 'A note', slug: 'a-note', pinned: false,
  created_at: '', updated_at: '', click_count: 5, tags: [tag], body_text_snippet: 'a snippet',
  last_clicked_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
}

const oldNote: Entry = {
  kind: 'note', id: 9, title: 'Old note', slug: 'old-note', pinned: false,
  created_at: '', updated_at: '', click_count: 0, tags: [],
  last_clicked_at: new Date(Date.now() - 5 * 86400_000).toISOString(),
}

const neverClicked: Link = {
  ...baseLink, id: 8, title: 'Never', slug: 'never', last_clicked_at: null, tags: [],
}

const baseFolder: Folder = {
  id: 3, name: 'A folder', color: '#000', link_count: 2, folder_count: 0,
  preview_links: [], preview_folders: [], has_password: false,
}

const lockedFolder: Folder = {
  ...baseFolder, id: 4, name: 'Secret', has_password: true, color: 'linear-gradient(135deg, #6366F1, #EC4899)',
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
    expect(screen.getByText(/^\d+m$/)).toBeInTheDocument()
  })

  it('interleaves by name in alpha sort and reverses for alpha_desc', () => {
    const { unmount } = renderWithProviders(
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
    let rows = document.querySelectorAll('.fx-list-row')
    let titles = Array.from(rows).map((r) => r.textContent ?? '')
    expect(titles[0]).toMatch(/Apple link/)
    expect(titles[1]).toMatch(/Mango note/)
    expect(titles[2]).toMatch(/Zebra folder/)
    unmount()

    renderWithProviders(
      <ListView
        folders={[{ ...baseFolder, name: 'Zebra folder' }]}
        entries={[{ kind: 'link', ...baseLink, title: 'Apple link' }, { ...baseNote, title: 'Mango note' }]}
        sort="alpha_desc"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    rows = document.querySelectorAll('.fx-list-row')
    titles = Array.from(rows).map((r) => r.textContent ?? '')
    expect(titles[0]).toMatch(/Zebra folder/)
    expect(titles[2]).toMatch(/Apple link/)
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

  it('calls onEdit for link and note action buttons', async () => {
    const onEdit = vi.fn()
    const onEditNote = vi.fn()
    renderWithProviders(
      <ListView
        folders={[]}
        entries={[{ kind: 'link', ...baseLink }, baseNote]}
        sort="created"
        onEdit={onEdit}
        onEditNote={onEditNote}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    const user = userEvent.setup()
    const editBtns = screen.getAllByLabelText(/^edit$/i)
    await user.click(editBtns[0])
    expect(onEdit).toHaveBeenCalledWith(expect.objectContaining({ id: 1 }))
    await user.click(editBtns[1])
    expect(onEditNote).toHaveBeenCalledWith(2)
  })

  it('confirms and deletes a link', async () => {
    state.links = [baseLink]
    renderWithProviders(
      <ListView
        folders={[]}
        entries={[{ kind: 'link', ...baseLink }]}
        sort="created"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/^delete$/i))
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: /^Delete link$/i }))
    await waitFor(() => expect(state.links.find((l) => l.id === 1)).toBeUndefined())
  })

  it('cancels link delete when confirm is dismissed', async () => {
    state.links = [baseLink]
    renderWithProviders(
      <ListView
        folders={[]}
        entries={[{ kind: 'link', ...baseLink }]}
        sort="created"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/^delete$/i))
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: /cancel/i }))
    expect(state.links.find((l) => l.id === 1)).toBeTruthy()
  })

  it('confirms and deletes a note', async () => {
    state.notes = [{
      id: 2, title: 'A note', slug: 'a-note', body_html: '<p>x</p>',
      pinned: false, created_at: '', updated_at: '', click_count: 0, tags: [],
    }]
    renderWithProviders(
      <ListView
        folders={[]}
        entries={[baseNote]}
        sort="created"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/^delete$/i))
    await screen.findByRole('dialog')
    await user.click(screen.getByRole('button', { name: /^Delete note$/i }))
    await waitFor(() => expect(state.notes.find((n) => n.id === 2)).toBeUndefined())
  })

  it('opens folder on double-click and open button; edits folder', async () => {
    const onOpenFolder = vi.fn()
    const onEditFolder = vi.fn()
    renderWithProviders(
      <ListView
        folders={[baseFolder, lockedFolder]}
        entries={[]}
        sort="created"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={onOpenFolder}
        onEditFolder={onEditFolder}
      />,
    )
    const user = userEvent.setup()
    const folderRow = screen.getByText('A folder').closest('.fx-list-row')!
    await user.dblClick(folderRow)
    expect(onOpenFolder).toHaveBeenCalledWith(3)

    await user.click(screen.getByLabelText(/edit folder.*secret|edit.*secret/i))
    expect(onEditFolder).toHaveBeenCalledWith(expect.objectContaining({ id: 4 }))

    await user.click(screen.getByLabelText(/open folder.*a folder|open.*a folder/i))
    expect(onOpenFolder).toHaveBeenCalledWith(3)

    expect(document.querySelector('.fx-folder-lock-icon')).toBeTruthy()
  })

  it('renders open hrefs for links and notes', () => {
    renderWithProviders(
      <ListView
        folders={[]}
        entries={[{ kind: 'link', ...baseLink }, baseNote]}
        sort="created"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    const anchors = screen.getAllByRole('link') as HTMLAnchorElement[]
    expect(anchors.some((a) => a.getAttribute('href')?.includes('/go/'))).toBe(true)
    expect(anchors.some((a) => a.getAttribute('href')?.includes('/n/'))).toBe(true)
  })

  it('formats last-clicked ages across minute/hour/day buckets and em-dash', () => {
    renderWithProviders(
      <ListView
        folders={[]}
        entries={[
          { kind: 'link', ...baseLink },
          baseNote,
          oldNote,
          { kind: 'link', ...neverClicked },
        ]}
        sort="created"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    const lasts = Array.from(document.querySelectorAll('.fx-list-last')).map((n) => n.textContent)
    expect(lasts.some((t) => t === '—')).toBe(true)
    expect(lasts.some((t) => /m$/.test(t ?? ''))).toBe(true)
    expect(lasts.some((t) => /h$/.test(t ?? ''))).toBe(true)
    expect(lasts.some((t) => /d$/.test(t ?? ''))).toBe(true)
  })

  it('caps tags at 3 chips per row', () => {
    renderWithProviders(
      <ListView
        folders={[]}
        entries={[{ kind: 'link', ...baseLink }]}
        sort="created"
        onEdit={vi.fn()}
        onEditNote={vi.fn()}
        onOpenFolder={vi.fn()}
        onEditFolder={vi.fn()}
      />,
    )
    const chips = document.querySelectorAll('.fx-list-tags .fx-tag, .fx-list-tags [class*="tag"]')
    // TagChip renders; just ensure we didn't dump all 4 tag names
    expect(screen.queryByText('z')).toBeNull()
    expect(chips.length).toBeLessThanOrEqual(3)
  })
})
