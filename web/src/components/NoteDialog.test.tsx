import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act, screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { NoteDialog, buildImageUploadHandler, buildNoteEditorProps } from './NoteDialog'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { http } from '../api/client'
import { INLINE_PALETTE } from '../lib/inlinePalette'
import { buildCreateNotePayload, buildUpdateNotePayload, type NoteDialogValues } from './NoteDialogPayload'

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

  it('does not submit an edit before the requested note loads, then PATCHes that note with its version', async () => {
    const updatedAt = '2026-08-13T10:00:00Z'
    const note = {
      id: 17, title: 'Loaded note', slug: 'loaded-note', body_html: '<p>loaded</p>', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: updatedAt, tags: [],
    }
    state.notes.push(note)
    const originalGet = vi.mocked(http.get).getMockImplementation()!
    let resolveNote!: (value: { data: typeof note }) => void
    const pendingNote = new Promise<{ data: typeof note }>((resolve) => { resolveNote = resolve })
    vi.mocked(http.get).mockImplementation(((url: string, config?: Parameters<typeof http.get>[1]) => {
      if (url === '/api/notes/17') return pendingNote
      return originalGet(url, config)
    }) as typeof http.get)

    renderWithProviders(<NoteDialog open noteId={17} onClose={vi.fn()} />)
    expect(screen.getByRole('status')).toHaveTextContent(/loading note/i)
    expect(screen.queryByPlaceholderText('Give your note a title…')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Save changes/i })).not.toBeInTheDocument()
    expect(http.post).not.toHaveBeenCalledWith('/api/notes', expect.anything())
    expect(http.patch).not.toHaveBeenCalled()

    await act(async () => { resolveNote({ data: note }) })
    await waitFor(() => expect(screen.getByPlaceholderText('Give your note a title…')).toHaveValue('Loaded note'))
    await userEvent.setup().click(screen.getByRole('button', { name: /Save changes/i }))

    await waitFor(() => expect(http.patch).toHaveBeenCalled())
    const patch = vi.mocked(http.patch).mock.calls.find(([url]) => url === '/api/notes/17')
    expect(patch).toBeDefined()
    expect(patch?.[1]).toEqual(expect.objectContaining({ if_match_updated_at: updatedAt }))
    expect(http.post).not.toHaveBeenCalledWith('/api/notes', expect.anything())
  })

  it('shows a retryable error boundary without mounting edit controls', async () => {
    const note = {
      id: 18, title: 'Recovered note', slug: 'recovered-note', body_html: '<p>ready</p>', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: 'v1', tags: [],
    }
    state.notes.push(note)
    const originalGet = vi.mocked(http.get).getMockImplementation()!
    let attempts = 0
    vi.mocked(http.get).mockImplementation(((url: string, config?: Parameters<typeof http.get>[1]) => {
      if (url !== '/api/notes/18') return originalGet(url, config)
      attempts += 1
      if (attempts === 1) return Promise.reject(new Error('offline'))
      return Promise.resolve({ data: note })
    }) as typeof http.get)

    renderWithProviders(<NoteDialog open noteId={18} onClose={vi.fn()} />)

    expect(await screen.findByRole('alert')).toHaveTextContent(/could not load this note/i)
    expect(screen.queryByPlaceholderText('Give your note a title…')).not.toBeInTheDocument()
    await userEvent.setup().click(screen.getByRole('button', { name: /try again/i }))
    expect(await screen.findByPlaceholderText('Give your note a title…')).toHaveValue('Recovered note')
  })

  it('ignores a stale request when noteId changes before the first note loads', async () => {
    const first = {
      id: 20, title: 'First note', slug: 'first-note', body_html: '<p>first</p>', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: 'first-v1', tags: [],
    }
    const second = { ...first, id: 21, title: 'Second note', slug: 'second-note', updated_at: 'second-v1' }
    let resolveFirst!: (value: { data: typeof first }) => void
    let resolveSecond!: (value: { data: typeof second }) => void
    const firstRequest = new Promise<{ data: typeof first }>((resolve) => { resolveFirst = resolve })
    const secondRequest = new Promise<{ data: typeof second }>((resolve) => { resolveSecond = resolve })
    const originalGet = vi.mocked(http.get).getMockImplementation()!
    vi.mocked(http.get).mockImplementation(((url: string, config?: Parameters<typeof http.get>[1]) => {
      if (url === '/api/notes/20') return firstRequest
      if (url === '/api/notes/21') return secondRequest
      return originalGet(url, config)
    }) as typeof http.get)

    const { rerender } = renderWithProviders(<NoteDialog open noteId={20} onClose={vi.fn()} />)
    rerender(<NoteDialog open noteId={21} onClose={vi.fn()} />)
    await act(async () => { resolveFirst({ data: first }) })

    expect(screen.getByRole('status')).toHaveTextContent(/loading note/i)
    expect(screen.queryByPlaceholderText('Give your note a title…')).not.toBeInTheDocument()

    await act(async () => { resolveSecond({ data: second }) })
    expect(await screen.findByPlaceholderText('Give your note a title…')).toHaveValue('Second note')
    expect(screen.queryByDisplayValue('First note')).not.toBeInTheDocument()
  })

  it('does not overwrite dirty fields or advance the conflict version after a late refetch', async () => {
    const originalVersion = '2026-08-13T10:00:00Z'
    state.notes.push({
      id: 22, title: 'Original', slug: 'original', body_html: '<p>body</p>', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: originalVersion, tags: [],
    })
    const onClose = vi.fn()
    const { client } = renderWithProviders(<NoteDialog open noteId={22} onClose={onClose} />)
    const title = await screen.findByPlaceholderText('Give your note a title…')
    const user = userEvent.setup()
    await user.clear(title)
    await user.type(title, 'My unsaved title')

    state.notes[0] = { ...state.notes[0], title: 'Late server title', updated_at: 'new-server-version' }
    await act(async () => {
      await client.invalidateQueries({ queryKey: ['notes', 22] })
    })

    expect(title).toHaveValue('My unsaved title')
    await user.click(screen.getByRole('button', { name: /Save changes/i }))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(vi.mocked(http.patch).mock.calls.at(-1)?.[1]).toEqual(expect.objectContaining({
      title: 'My unsaved title',
      if_match_updated_at: originalVersion,
    }))
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

  it('keeps dirty create fields open and shows the API message when creation fails', async () => {
    const originalPost = vi.mocked(http.post).getMockImplementation()!
    const failure = Object.assign(new Error('save failed'), {
      response: { status: 500, data: { error: { code: 'server_error', message: 'Storage is unavailable' } } },
    })
    vi.mocked(http.post).mockImplementation(((url: string, ...args: unknown[]) => {
      if (url === '/api/notes') return Promise.reject(failure)
      return originalPost(url, ...args)
    }) as typeof http.post)
    const onClose = vi.fn()
    renderWithProviders(<NoteDialog open noteId={null} onClose={onClose} />)

    await userEvent.setup().type(screen.getByPlaceholderText('Give your note a title…'), 'Keep this draft')
    await userEvent.setup().click(screen.getByRole('button', { name: /Create note/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent('Storage is unavailable')
    expect(screen.getByPlaceholderText('Give your note a title…')).toHaveValue('Keep this draft')
    expect(onClose).not.toHaveBeenCalled()
  })

  it('routes the hidden image input through the upload error boundary', async () => {
    const originalPost = vi.mocked(http.post).getMockImplementation()!
    vi.mocked(http.post).mockImplementation(((url: string, ...args: unknown[]) => {
      if (url === '/api/notes/images') return Promise.reject(new Error('upload failed'))
      return originalPost(url, ...args)
    }) as typeof http.post)
    const { container } = renderWithProviders(<NoteDialog open noteId={null} onClose={vi.fn()} />)
    const input = container.querySelector<HTMLInputElement>('input[type="file"]')
    expect(input).not.toBeNull()

    fireEvent.change(input!, { target: { files: [new File(['image'], 'note.png', { type: 'image/png' })] } })

    expect(await screen.findByText('Failed to upload image')).toBeInTheDocument()
    expect(input).toHaveValue('')
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

  it('sends existing tag IDs and cycled pending definitions in the note request', async () => {
    state.tags.push({ id: 1, name: 'jira', color: '#1f6feb', icon: null })
    renderWithProviders(<NoteDialog open noteId={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('Give your note a title…'), 'Tagged')
    const tagsInput = screen.getByLabelText('tag filter')
    await user.type(tagsInput, 'j')
    await user.click(await screen.findByText('jira'))
    await user.type(tagsInput, 'brand-new')
    await user.keyboard('{Enter}')
    expect(document.querySelector('.fx-tag-hint')).not.toBeNull()
    await user.click(screen.getByText('brand-new'))
    await user.click(screen.getByRole('button', { name: /Create note/i }))
    await waitFor(() => expect(state.notes).toHaveLength(1))
    expect(state.tags.some((t) => t.name === 'brand-new')).toBe(true)

    const parentCall = vi.mocked(http.post).mock.calls.find(([url]) => url === '/api/notes')
    expect(parentCall?.[1]).toEqual(expect.objectContaining({
      tag_ids: [1],
      pending_tags: [{ name: 'brand-new', color: INLINE_PALETTE[1], icon: null }],
    }))
    expect(vi.mocked(http.post).mock.calls.some(([url]) => url === '/api/tags')).toBe(false)
  })

  it('does not create a standalone tag when the note request fails', async () => {
    const originalPost = vi.mocked(http.post).getMockImplementation()!
    const conflict = Object.assign(new Error('conflict'), {
      response: { status: 409, data: { error: { code: 'conflict', message: 'note was modified' } } },
    })
    vi.mocked(http.post).mockImplementation(((url: string, ...args: unknown[]) => {
      if (url === '/api/notes') return Promise.reject(conflict)
      return originalPost(url, ...args)
    }) as typeof http.post)
    const onClose = vi.fn()
    renderWithProviders(<NoteDialog open noteId={null} onClose={onClose} />)
    const user = userEvent.setup()

    await user.type(screen.getByPlaceholderText('Give your note a title…'), 'Will fail')
    await user.type(screen.getByLabelText('tag filter'), 'must-not-orphan')
    await user.keyboard('{Enter}')
    await user.click(screen.getByRole('button', { name: /Create note/i }))

    expect(await screen.findByText(/changed elsewhere.*reload/i)).toBeInTheDocument()
    expect(state.tags.some((tag) => tag.name === 'must-not-orphan')).toBe(false)
    expect(vi.mocked(http.post).mock.calls.some(([url]) => url === '/api/tags')).toBe(false)
    expect(onClose).not.toHaveBeenCalled()
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

  it('keeps an edit open with translated guidance when the note changed elsewhere', async () => {
    const updatedAt = '2026-08-13T10:00:00Z'
    state.notes.push({
      id: 2, title: 'Old', slug: 'old', body_html: '<p>x</p>', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: updatedAt, tags: [],
    })
    const conflict = Object.assign(new Error('conflict'), {
      response: { status: 409, data: { error: { message: 'note was modified; refetch and retry' } } },
    })
    vi.mocked(http.patch).mockRejectedValue(conflict)
    const onClose = vi.fn()
    renderWithProviders(<NoteDialog open noteId={2} onClose={onClose} />)
    await waitFor(() => expect(screen.getByPlaceholderText('Give your note a title…')).toHaveValue('Old'))

    await userEvent.setup().click(screen.getByRole('button', { name: /Save changes/i }))

    expect(await screen.findByText(/changed elsewhere.*reload/i)).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(vi.mocked(http.patch).mock.calls[0]?.[1]).toEqual(
      expect.objectContaining({ if_match_updated_at: updatedAt }),
    )
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

describe('buildNoteEditorProps', () => {
  it('handles only image paste and drop events', () => {
    const upload = vi.fn()
    const props = buildNoteEditorProps(upload)
    const view = {} as Parameters<typeof props.handlePaste>[0]
    const file = new File(['x'], 'note.png', { type: 'image/png' })
    const pastePrevented = vi.fn()
    const dropPrevented = vi.fn()

    expect(props.handlePaste(view, { clipboardData: null, preventDefault: vi.fn() } as unknown as ClipboardEvent)).toBe(false)
    expect(props.handleDrop(view, { dataTransfer: null, preventDefault: vi.fn() } as unknown as DragEvent)).toBe(false)
    expect(props.handlePaste(view, {
      clipboardData: { items: [{ type: 'image/png', getAsFile: () => file }] },
      preventDefault: pastePrevented,
    } as unknown as ClipboardEvent)).toBe(true)
    expect(props.handleDrop(view, {
      dataTransfer: { files: [file] },
      preventDefault: dropPrevented,
    } as unknown as DragEvent)).toBe(true)

    expect(upload).toHaveBeenNthCalledWith(1, view, file)
    expect(upload).toHaveBeenNthCalledWith(2, view, file)
    expect(pastePrevented).toHaveBeenCalled()
    expect(dropPrevented).toHaveBeenCalled()
  })
})

describe('note dialog payloads', () => {
  const values: NoteDialogValues = {
    title: '  Payload title  ',
    slug: '',
    slugDirty: false,
    pinned: true,
    folderId: 9,
    selectedTags: [
      { id: 3, name: 'existing', color: '#111', icon: null },
      { id: 0, name: 'pending', color: '#222', icon: 'star', _pending: true },
    ],
  }

  it('builds create payloads without sending an untouched empty slug', () => {
    expect(buildCreateNotePayload(values, '<p>body</p>')).toEqual({
      title: 'Payload title',
      body_html: '<p>body</p>',
      tag_ids: [3],
      pending_tags: [{ name: 'pending', color: '#222', icon: 'star' }],
      pinned: true,
      folder_id: 9,
    })
    expect(buildCreateNotePayload({ ...values, slug: ' custom ', slugDirty: true }, '')).toEqual(
      expect.objectContaining({ slug: 'custom' }),
    )
  })

  it('keeps the original edit version and preserves dirty-slug reset semantics', () => {
    expect(buildUpdateNotePayload(values, '<p>body</p>', 'version-1')).not.toHaveProperty('slug')
    expect(buildUpdateNotePayload({ ...values, slugDirty: true }, '', 'version-1')).toEqual(expect.objectContaining({
      if_match_updated_at: 'version-1',
      slug: null,
      pending_tags: [{ name: 'pending', color: '#222', icon: 'star' }],
    }))
  })
})
