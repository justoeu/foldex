import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { NoteViewDialog } from './NoteViewDialog'
import { renderWithProviders } from '../test/renderWithProviders'
import { http } from '../api/client'
import type { Note } from '../api/types'

const note: Note = {
  id: 7,
  title: 'Shopping list',
  slug: 'shopping-list',
  body_html: '<h2>Produce</h2><p>Apples and <strong>pears</strong>.</p>',
  pinned: false,
  cover_url: null,
  click_count: 3,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-02T00:00:00Z',
  tags: [{ id: 1, name: 'home', color: '#8B85FF' }],
}

function mockGet(data: Note | Error) {
  return data instanceof Error
    ? vi.spyOn(http, 'get').mockRejectedValue(data)
    : vi.spyOn(http, 'get').mockResolvedValue({ data } as never)
}

beforeEach(() => vi.restoreAllMocks())
afterEach(() => vi.restoreAllMocks())

describe('the note reader', () => {
  it('renders the note body as rich text, not as escaped markup', async () => {
    mockGet(note)
    renderWithProviders(<NoteViewDialog noteId={7} onClose={vi.fn()} onEdit={vi.fn()} />)

    // The heading has to be a real <h2>: rendering body_html as TEXT is the
    // failure this asserts against, and it looks identical in a string match.
    expect(await screen.findByRole('heading', { level: 2, name: 'Produce' })).toBeInTheDocument()
    expect(screen.getByText('pears').tagName).toBe('STRONG')
  })

  it('shows what the reader needs before the text: tags and freshness', async () => {
    mockGet(note)
    renderWithProviders(<NoteViewDialog noteId={7} onClose={vi.fn()} onEdit={vi.fn()} />)

    expect(await screen.findByText('home')).toBeInTheDocument()
    expect(screen.getByText(/3 views/i)).toBeInTheDocument()
  })

  // The public page is the only path that records a view, so the reader must
  // keep a way to reach it — and it must still be a real link, in a new tab.
  it('keeps the public page one click away, as a link', async () => {
    mockGet(note)
    renderWithProviders(<NoteViewDialog noteId={7} onClose={vi.fn()} onEdit={vi.fn()} />)

    const link = await screen.findByRole('link', { name: /open public page/i })
    expect(link).toHaveAttribute('href', '/n/shopping-list')
    expect(link).toHaveAttribute('target', '_blank')
  })

  it('hands the loaded note to the editor', async () => {
    mockGet(note)
    const onEdit = vi.fn()
    renderWithProviders(<NoteViewDialog noteId={7} onClose={vi.fn()} onEdit={onEdit} />)

    await userEvent.click(await screen.findByRole('button', { name: /edit/i }))
    expect(onEdit).toHaveBeenCalledWith(note)
  })

  it('closes on Escape', async () => {
    mockGet(note)
    const onClose = vi.fn()
    renderWithProviders(<NoteViewDialog noteId={7} onClose={onClose} onEdit={vi.fn()} />)
    await screen.findByRole('heading', { level: 2, name: 'Produce' })

    await userEvent.keyboard('{Escape}')
    expect(onClose).toHaveBeenCalled()
  })

  // A failed load must say so and offer a way out. Rendering an empty modal
  // reads as a note with no content, which is a different problem entirely.
  it('reports a failed load instead of showing an empty note', async () => {
    mockGet(new Error('offline'))
    renderWithProviders(<NoteViewDialog noteId={7} onClose={vi.fn()} onEdit={vi.fn()} />)

    expect(await screen.findByText(/could not load this note/i)).toBeInTheDocument()
    const foot = screen.getByRole('dialog')
    expect(within(foot).queryByRole('link', { name: /open public page/i })).toBeNull()
  })

  it('does not record a view — that is what the public page is for', async () => {
    const get = mockGet(note)
    renderWithProviders(<NoteViewDialog noteId={7} onClose={vi.fn()} onEdit={vi.fn()} />)
    await screen.findByRole('heading', { level: 2, name: 'Produce' })

    await waitFor(() => expect(get).toHaveBeenCalledWith('/api/notes/7'))
    // One read of the note, and nothing that would log a click.
    expect(get.mock.calls.every(([url]) => String(url).startsWith('/api/notes/'))).toBe(true)
  })
})
