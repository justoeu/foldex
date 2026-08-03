import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor, fireEvent } from '@testing-library/react'
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

  it('includes the selected tag, folder, and pinned state in the create payload', async () => {
    state.tags.push({ id: 1, name: 'jira', color: '#1f6feb', icon: null })
    state.folders.push({
      id: 5, name: 'Work', color: '#6366F1', parent_id: null,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: false, created_at: '',
    })
    renderWithProviders(<NoteDialog open noteId={null} onClose={vi.fn()} />)
    const user = userEvent.setup()

    await user.type(screen.getByPlaceholderText('Give your note a title…'), 'Tagged note')

    const tagsInput = screen.getByLabelText('tag filter')
    await user.type(tagsInput, 'j')
    const jiraChip = await screen.findByText('jira')
    await user.click(jiraChip)

    const folderInput = screen.getByLabelText('folder')
    await user.click(folderInput)
    await user.click(await screen.findByText('Work'))

    await user.click(screen.getByRole('checkbox', { name: 'Pin this note' }))

    await user.click(screen.getByRole('button', { name: /Create note/i }))
    await waitFor(() => expect(state.notes).toHaveLength(1))
    expect(state.notes[0].tags[0].name).toBe('jira')
    expect(state.notes[0].folder_id).toBe(5)
    expect(state.notes[0].pinned).toBe(true)
  })

  it('round-trips the loaded body_html back into the edit PATCH payload unmodified', async () => {
    state.notes.push({
      id: 1, title: 'Existing note', slug: 'existing-note', body_html: '<p>hello</p>', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    const onClose = vi.fn()
    renderWithProviders(<NoteDialog open noteId={1} onClose={onClose} />)
    // Guards the isInitialized-race fix in NoteDialog: the editor's content
    // must actually be populated from the loaded note (not just the title
    // field), otherwise saving without touching the body would silently
    // overwrite body_html with an empty string.
    await waitFor(() => expect(screen.getByText('hello')).toBeInTheDocument())

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /Save changes/i }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(state.notes[0].body_html).toContain('hello')
  })

  it('closes on Escape', async () => {
    const onClose = vi.fn()
    renderWithProviders(<NoteDialog open noteId={null} onClose={onClose} />)
    const user = userEvent.setup()
    await user.keyboard('{Escape}')
    expect(onClose).toHaveBeenCalled()
  })

  it('closes via Cancel button', async () => {
    const onClose = vi.fn()
    renderWithProviders(<NoteDialog open noteId={null} onClose={onClose} />)
    await userEvent.setup().click(screen.getByRole('button', { name: /cancel/i }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('closes via X button', async () => {
    const onClose = vi.fn()
    renderWithProviders(<NoteDialog open noteId={null} onClose={onClose} />)
    await userEvent.setup().click(screen.getByRole('button', { name: /^close$/i }))
    expect(onClose).toHaveBeenCalled()
  })

  it('auto-derives slug from title and allows reset after dirty edit', async () => {
    renderWithProviders(<NoteDialog open noteId={null} onClose={vi.fn()} />)
    const title = screen.getByPlaceholderText('Give your note a title…')
    fireEvent.change(title, { target: { value: 'My Cool Note' } })
    const slug = screen.getByLabelText(/note slug/i) as HTMLInputElement
    await waitFor(() => expect(slug.value).toBe('my-cool-note'))
    fireEvent.change(slug, { target: { value: 'custom-slug' } })
    await userEvent.click(screen.getByRole('button', { name: /reset to auto/i }))
    expect(slug.value).toBe('my-cool-note')
  })

  it('ships a custom dirty slug on create', async () => {
    const onClose = vi.fn()
    renderWithProviders(<NoteDialog open noteId={null} onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Give your note a title…'), 'Custom')
    fireEvent.change(screen.getByLabelText(/note slug/i), { target: { value: 'my-custom' } })
    await user.click(screen.getByRole('button', { name: /Create note/i }))
    await waitFor(() => expect(state.notes).toHaveLength(1))
    expect(state.notes[0].slug).toBe('my-custom')
    expect(onClose).toHaveBeenCalled()
  })

  it('creates an inline pending tag via Enter and saves it', async () => {
    renderWithProviders(<NoteDialog open noteId={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Give your note a title…'), 'Tagged')
    const tagsInput = screen.getByLabelText('tag filter')
    await user.type(tagsInput, 'brand-new{Enter}')
    expect(document.querySelector('.fx-tag-hint')).not.toBeNull()
    await user.click(screen.getByRole('button', { name: /Create note/i }))
    await waitFor(() => expect(state.notes).toHaveLength(1))
    expect(state.tags.some((t) => t.name === 'brand-new')).toBe(true)
  })

  it('cycles pending tag color on chip click', async () => {
    renderWithProviders(<NoteDialog open noteId={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Give your note a title…'), 'Color me')
    await user.type(screen.getByLabelText('tag filter'), 'hue{Enter}')
    const chip = screen.getByText('hue')
    await user.click(chip)
    await user.click(screen.getByRole('button', { name: /Create note/i }))
    await waitFor(() => expect(state.notes).toHaveLength(1))
  })

  it('paginates registered tags when more than 7 are available', async () => {
    for (let i = 0; i < 10; i++) {
      state.tags.push({ id: 200 + i, name: `ntag-${i}`, color: '#222', icon: null })
    }
    renderWithProviders(<NoteDialog open noteId={null} onClose={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('1/2')).toBeInTheDocument())
    await userEvent.click(screen.getByRole('button', { name: /next page/i }))
    expect(screen.getByText('2/2')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /previous page/i }))
    expect(screen.getByText('1/2')).toBeInTheDocument()
  })

  it('removes a selected tag via chip close', async () => {
    state.tags.push({ id: 1, name: 'jira', color: '#1f6feb', icon: null })
    renderWithProviders(<NoteDialog open noteId={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Give your note a title…'), 'T')
    await user.type(screen.getByLabelText('tag filter'), 'j')
    await user.click(await screen.findByText('jira'))
    const closeBtns = screen.getAllByRole('button').filter((b) => b.getAttribute('aria-label')?.match(/remove|close/i))
    if (closeBtns[0]) await user.click(closeBtns[0])
  })

  it('EDIT: updates title and ships dirty slug', async () => {
    state.notes.push({
      id: 2, title: 'Old', slug: 'old', body_html: '<p>x</p>', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    const onClose = vi.fn()
    renderWithProviders(<NoteDialog open noteId={2} onClose={onClose} />)
    await waitFor(() => expect(screen.getByPlaceholderText('Give your note a title…')).toHaveValue('Old'))
    const user = userEvent.setup()
    const title = screen.getByPlaceholderText('Give your note a title…')
    await user.clear(title)
    await user.type(title, 'New title')
    fireEvent.change(screen.getByLabelText(/note slug/i), { target: { value: 'new-title' } })
    await user.click(screen.getByRole('button', { name: /Save changes/i }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(state.notes[0].title).toBe('New title')
  })

  it('preselects defaultFolderId on create', async () => {
    state.folders.push({
      id: 9, name: 'Inbox', color: '#6366F1', parent_id: null,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: false, created_at: '',
    })
    renderWithProviders(<NoteDialog open noteId={null} defaultFolderId={9} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Give your note a title…'), 'In folder')
    await user.click(screen.getByRole('button', { name: /Create note/i }))
    await waitFor(() => expect(state.notes[0]?.folder_id).toBe(9))
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
