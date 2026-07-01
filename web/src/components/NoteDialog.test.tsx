import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { NoteDialog, buildImageUploadHandler } from './NoteDialog'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

describe('NoteDialog', () => {
  it('renders nothing when closed', () => {
    const { container } = renderWithProviders(<NoteDialog open={false} noteId={null} onClose={vi.fn()} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('shows the create title and an empty title field when opened for create', () => {
    renderWithProviders(<NoteDialog open noteId={null} onClose={vi.fn()} />)
    expect(screen.getByText('New note')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('Give your note a title…')).toHaveValue('')
  })

  it('shows the edit title and loaded fields when opened for edit', async () => {
    state.notes.push({
      id: 1, title: 'Existing note', slug: 'existing-note', body_html: '<p>hello</p>', pinned: true,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    renderWithProviders(<NoteDialog open noteId={1} onClose={vi.fn()} />)
    await waitFor(() => expect(screen.getByPlaceholderText('Give your note a title…')).toHaveValue('Existing note'))
    expect(screen.getByText('Edit note')).toBeInTheDocument()
  })

  it('disables submit until a title is entered', async () => {
    renderWithProviders(<NoteDialog open noteId={null} onClose={vi.fn()} />)
    const submit = screen.getByRole('button', { name: /Create note/i })
    expect(submit).toBeDisabled()
    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Give your note a title…'), 'My note')
    expect(submit).not.toBeDisabled()
  })

  it('creates a note on submit', async () => {
    const onClose = vi.fn()
    renderWithProviders(<NoteDialog open noteId={null} onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Give your note a title…'), 'My note')
    await user.click(screen.getByRole('button', { name: /Create note/i }))
    await waitFor(() => expect(state.notes).toHaveLength(1))
    expect(state.notes[0].title).toBe('My note')
    expect(onClose).toHaveBeenCalled()
  })

  it('closes on Escape', async () => {
    const onClose = vi.fn()
    renderWithProviders(<NoteDialog open noteId={null} onClose={onClose} />)
    const user = userEvent.setup()
    await user.keyboard('{Escape}')
    expect(onClose).toHaveBeenCalled()
  })
})

describe('buildImageUploadHandler', () => {
  it('uploads the file and inserts an image node at the current selection', async () => {
    const uploadFn = vi.fn().mockResolvedValue({ url: '/api/files/notes/abc.jpg' })
    const onError = vi.fn()
    const dispatch = vi.fn()
    const imageNode = { type: 'image' }
    const view = {
      state: {
        schema: { nodes: { image: { create: vi.fn().mockReturnValue(imageNode) } } },
        tr: { }, // replaceSelectionWith is chained below
      },
      dispatch,
    } as any
    view.state.tr.replaceSelectionWith = vi.fn().mockReturnValue('final-tr')

    const handler = buildImageUploadHandler(uploadFn, onError)
    const file = new File(['x'], 'a.png', { type: 'image/png' })
    handler(view, file)

    await waitFor(() => expect(dispatch).toHaveBeenCalledWith('final-tr'))
    expect(uploadFn).toHaveBeenCalledWith(file)
    expect(view.state.schema.nodes.image.create).toHaveBeenCalledWith({ src: '/api/files/notes/abc.jpg' })
    expect(onError).not.toHaveBeenCalled()
  })

  it('calls onError when the upload fails', async () => {
    const uploadFn = vi.fn().mockRejectedValue(new Error('nope'))
    const onError = vi.fn()
    const view = { state: { schema: { nodes: { image: { create: vi.fn() } } }, tr: {} }, dispatch: vi.fn() } as any

    const handler = buildImageUploadHandler(uploadFn, onError)
    handler(view, new File(['x'], 'a.png', { type: 'image/png' }))

    await waitFor(() => expect(onError).toHaveBeenCalledWith('upload_failed'))
    expect(view.dispatch).not.toHaveBeenCalled()
  })
})
