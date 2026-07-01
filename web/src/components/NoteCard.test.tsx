import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { NoteCard, type NoteEntry } from './NoteCard'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

const baseNote: NoteEntry = {
  kind: 'note',
  id: 1,
  title: 'Shopping list',
  slug: 'shopping-list',
  pinned: false,
  folder_id: null,
  created_at: '',
  updated_at: '',
  click_count: 3,
  last_clicked_at: null,
  tags: [{ id: 1, name: 'home', color: '#1f6feb', icon: null }],
  cover_url: null,
  body_text_snippet: 'milk, eggs, bread',
}

describe('NoteCard', () => {
  it('renders title, snippet, tag chips, and click counter', () => {
    renderWithProviders(<NoteCard note={baseNote} onEdit={vi.fn()} />)
    expect(screen.getByText('Shopping list')).toBeInTheDocument()
    expect(screen.getByText('milk, eggs, bread')).toBeInTheDocument()
    expect(screen.getByText('home')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  it('always shows the note badge, regardless of content', () => {
    renderWithProviders(<NoteCard note={{ ...baseNote, body_text_snippet: null }} onEdit={vi.fn()} />)
    expect(document.querySelector('.fx-card-note-badge')).not.toBeNull()
  })

  it('calls onEdit when the title is clicked', async () => {
    const onEdit = vi.fn()
    renderWithProviders(<NoteCard note={baseNote} onEdit={onEdit} />)
    const user = userEvent.setup()
    await user.click(screen.getByText('Shopping list'))
    expect(onEdit).toHaveBeenCalledWith(1)
  })

  it('calls onEdit when the edit icon button is clicked', async () => {
    const onEdit = vi.fn()
    renderWithProviders(<NoteCard note={baseNote} onEdit={onEdit} />)
    const user = userEvent.setup()
    await user.click(screen.getByLabelText('Edit'))
    expect(onEdit).toHaveBeenCalledWith(1)
  })

  it('toggles pinned on badge click, optimistically', async () => {
    state.notes.push({
      id: 1, title: 'Shopping list', slug: 'shopping-list', body_html: '', pinned: false,
      folder_id: null, cover_url: null, click_count: 3, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    renderWithProviders(<NoteCard note={baseNote} onEdit={vi.fn()} />)
    const pinBtn = screen.getByLabelText('Pin')
    fireEvent.click(pinBtn)
    await waitFor(() => expect(state.notes[0].pinned).toBe(true))
  })

  it('confirms then deletes when delete button is clicked', async () => {
    state.notes.push({
      id: 1, title: 'Shopping list', slug: 'shopping-list', body_html: '', pinned: false,
      folder_id: null, cover_url: null, click_count: 3, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    renderWithProviders(<NoteCard note={baseNote} onEdit={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /delete/i }))
    const confirmBtn = await screen.findByRole('button', { name: /Delete note/i })
    await user.click(confirmBtn)
    await waitFor(() => expect(state.notes).toHaveLength(0))
  })

  it('sets the drag payload MIME type on dragstart', () => {
    const { container } = renderWithProviders(<NoteCard note={baseNote} onEdit={vi.fn()} />)
    const card = container.querySelector('.fx-card') as HTMLElement
    const setData = vi.fn()
    fireEvent.dragStart(card, { dataTransfer: { setData, effectAllowed: '' } })
    expect(setData).toHaveBeenCalledWith('application/x-foldex-note', '1')
  })

  it('drag-and-drop: dropping a link onto this card fires onMergeWith({kind:"link",...}, target)', () => {
    const onMerge = vi.fn()
    const { container } = renderWithProviders(<NoteCard note={{ ...baseNote, id: 9 }} onEdit={vi.fn()} onMergeWith={onMerge} />)
    const card = container.querySelector('.fx-card') as HTMLElement
    fireEvent.drop(card, {
      dataTransfer: {
        types: ['application/x-foldex-link'],
        getData: (k: string) => (k === 'application/x-foldex-link' ? '5' : ''),
      },
    })
    expect(onMerge).toHaveBeenCalledWith({ kind: 'link', id: 5 }, 9)
  })

  it('drag-and-drop: dropping this note onto itself is a no-op', () => {
    const onMerge = vi.fn()
    const { container } = renderWithProviders(<NoteCard note={{ ...baseNote, id: 4 }} onEdit={vi.fn()} onMergeWith={onMerge} />)
    const card = container.querySelector('.fx-card') as HTMLElement
    fireEvent.drop(card, {
      dataTransfer: {
        types: ['application/x-foldex-note'],
        getData: (k: string) => (k === 'application/x-foldex-note' ? '4' : ''),
      },
    })
    expect(onMerge).not.toHaveBeenCalled()
  })

  it('links to the public /n/ route with the slug', () => {
    renderWithProviders(<NoteCard note={baseNote} onEdit={vi.fn()} />)
    const openLink = screen.getByText('Open').closest('a')
    expect(openLink?.getAttribute('href')).toBe('/n/shopping-list')
  })
})
