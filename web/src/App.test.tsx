import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor, fireEvent, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import App from './App'
import { renderWithProviders } from './test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from './test/server'
import { http } from './api/client'

let state: MockState

beforeEach(() => {
  state = freshState()
  state.tags.push({ id: 1, name: 'jira', color: '#1f6feb', icon: null })
  installAxiosMock(state)
  const fallbackGet = vi.mocked(http.get).getMockImplementation()!
  vi.mocked(http.get).mockImplementation((async (url: string, ...rest: any[]) => {
    if (url === '/api/entries/counts') {
      return { data: { links: state.links.length, notes: state.notes.length } }
    }
    return fallbackGet(url, ...rest)
  }) as never)
  // Reset persisted UI preferences so localStorage state doesn't leak across
  // tests (viewMode/gridCols/sidebarCollapsed all read from localStorage in
  // App.tsx's initializers).
  if (typeof localStorage !== 'undefined') localStorage.clear()
  // jsdom keeps window.location across tests — strip the ?folder= param so a
  // prior test that entered a folder doesn't leak its state into the next.
  if (typeof window !== 'undefined') {
    window.history.replaceState({}, '', '/')
  }
})

describe('App', () => {
  it('shows the empty state on first load', async () => {
    renderWithProviders(<App />)
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    expect(screen.getByText(/Your base is still empty/i)).toBeInTheDocument()
    expect(screen.getAllByText(/⌥N/).length).toBeGreaterThan(0)
  })

  it('opens the new-link dialog via the New link button', async () => {
    /* The FAB and the topbar CTA both expose `aria-label="New link"`.
       We want to assert the desktop CTA click here — pick the topbar one
       by walking from the brand: the visible CTA sits in the same
       `<header>` and the FAB is fixed-positioned outside it. */
    renderWithProviders(<App />)
    const user = userEvent.setup()
    const newLinkButtons = screen.getAllByRole('button', { name: /new link/i })
    // First one is the topbar CTA (rendered before the FAB in App.tsx).
    await user.click(newLinkButtons[0])
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })

  it('navigates to the Import page via the settings hub', async () => {
    renderWithProviders(<App />)
    const user = userEvent.setup()
    // Import/export lives in the settings hub now (shortcut tile); the hub is
    // lazy-loaded, so the tile query has to wait for the chunk.
    await user.click(screen.getByRole('button', { name: /^settings$/i }))
    await user.click(await screen.findByRole('button', { name: /Import \/ Export/i }))
    expect(await screen.findByRole('heading', { name: 'Import' })).toBeInTheDocument()
  })

  it('filters links via the search box', async () => {
    state.links.push(
      {
        id: 1, url: 'https://hn', title: 'Hacker News', click_count: 0,
        preview_status: 'ok', created_at: '', updated_at: '', tags: [],
      } as any,
      {
        id: 2, url: 'https://ex', title: 'Example', click_count: 0,
        preview_status: 'ok', created_at: '', updated_at: '', tags: [],
      } as any,
    )
    renderWithProviders(<App />)
    await waitFor(() => expect(screen.getByText('Hacker News')).toBeInTheDocument())
    // Use fireEvent.change to avoid triggering the palette onClick on the parent div
    fireEvent.change(screen.getByLabelText(/^Search$/i), { target: { value: 'Hacker' } })
    await waitFor(() => expect(screen.queryByText('Example')).not.toBeInTheDocument())
  })

  it('density picker updates --fx-cols and persists the choice', async () => {
    state.links.push({
      id: 1, url: 'https://hn', title: 'Hacker News', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any)
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByText('Hacker News')).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /8 Density/i }))
    const mainarea = document.querySelector('.fx-mainarea') as HTMLElement
    expect(mainarea.style.getPropertyValue('--fx-cols')).toBe('8')
    expect(localStorage.getItem('foldex.grid.cols')).toBe('8')
    await user.click(screen.getByRole('button', { name: /3 Density/i }))
    expect(mainarea.style.getPropertyValue('--fx-cols')).toBe('3')
  })

  it('removes an unsupported persisted density and uses the default', async () => {
    localStorage.setItem('foldex.grid.cols', '7')
    renderWithProviders(<App />)

    await waitFor(() => expect(document.querySelector('.fx-mainarea')).not.toBeNull())
    const mainarea = document.querySelector('.fx-mainarea') as HTMLElement
    expect(mainarea.style.getPropertyValue('--fx-cols')).toBe('5')
    expect(localStorage.getItem('foldex.grid.cols')).toBeNull()
  })

  it('removes a garbage persisted view mode and falls back to cards', async () => {
    localStorage.setItem('foldex.viewMode.map', JSON.stringify({ home: 'garbage' }))
    renderWithProviders(<App />)

    expect((await screen.findAllByRole('button', { name: /^Cards$/i }))
      .some((button) => button.classList.contains('fx-vs-active'))).toBe(true)
    expect(localStorage.getItem('foldex.viewMode.map')).toBeNull()
  })

  it('toggles sort buttons (Novos/Top/Recentes)', async () => {
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /^Top$/i }))
    expect(screen.getByRole('button', { name: /^Top$/i })).toHaveAttribute('aria-pressed', 'true')
    expect(localStorage.getItem('foldex.sort')).toBe('"clicks"')
    await user.click(screen.getByRole('button', { name: /^Recent$/i }))
    expect(screen.getByRole('button', { name: /^Recent$/i })).toHaveAttribute('aria-pressed', 'true')
  })

  it('removes an unsupported persisted sort and uses the default', async () => {
    localStorage.setItem('foldex.sort', '"garbage"')
    renderWithProviders(<App />)

    expect((await screen.findAllByRole('button', { name: /^Newest$/i }))
      .some((button) => button.getAttribute('aria-pressed') === 'true')).toBe(true)
    expect(localStorage.getItem('foldex.sort')).toBeNull()
  })

  it('toggles a tag filter via the sidebar', async () => {
    state.links.push(
      {
        id: 1, url: 'https://a', title: 'Alpha', click_count: 0,
        preview_status: 'ok', created_at: '', updated_at: '',
        tags: [state.tags[0]],
      } as any,
      {
        id: 2, url: 'https://b', title: 'Beta', click_count: 0,
        preview_status: 'ok', created_at: '', updated_at: '', tags: [],
      } as any,
    )
    renderWithProviders(<App />)
    await waitFor(() => expect(screen.getByText('Alpha')).toBeInTheDocument())
    const user = userEvent.setup()
    const sidebarJira = screen.getAllByText('jira')[0]
    await user.click(sidebarJira)
    await waitFor(() => expect(screen.queryByText('Beta')).not.toBeInTheDocument())
    await user.click(screen.getAllByText('jira')[0])
    await waitFor(() => expect(screen.getByText('Beta')).toBeInTheDocument())
  })

  it('opens the command palette via ⌥K', async () => {
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await user.keyboard('{Alt>}k{/Alt}')
    expect(await screen.findByPlaceholderText(/Search by.*action/i)).toBeInTheDocument()
  })

  it('opens the new-folder dialog via ⌥F', async () => {
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await user.keyboard('{Alt>}f{/Alt}')
    expect(await screen.findByRole('dialog', { name: /new folder/i })).toBeInTheDocument()
  })

  it('renders the Nova pasta CTA in the topbar', () => {
    renderWithProviders(<App />)
    expect(screen.getByRole('button', { name: /new folder/i })).toBeInTheDocument()
  })

  it('A→Z sort interleaves folders and links by name in the cards grid', async () => {
    state.folders.push({
      id: 1, name: 'Zebra folder', color: '#000', link_count: 0, preview_links: [], preview_folders: [],
      created_at: '',
    } as any)
    state.links.push({
      id: 10, url: 'https://a', title: 'Alpha link', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any)
    state.links.push({
      id: 11, url: 'https://m', title: 'Middle thing', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any)
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByText('Alpha link')).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /A→Z/ }))
    // After alpha sort: "Alpha link" → "Middle thing" → "Zebra folder"
    const cards = document.querySelectorAll('.fx-card')
    const titles = Array.from(cards).map((c) => c.textContent ?? '')
    expect(titles[0]).toMatch(/Alpha link/)
    expect(titles[titles.length - 1]).toMatch(/Zebra folder/)
  })

  it('default sort puts folders first regardless of name', async () => {
    state.folders.push({
      id: 1, name: 'Zebra', color: '#000', link_count: 0, preview_links: [], preview_folders: [],
      created_at: '',
    } as any)
    state.links.push({
      id: 10, url: 'https://a', title: 'Alpha link', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any)
    renderWithProviders(<App />)
    await waitFor(() => expect(screen.getByText('Alpha link')).toBeInTheDocument())
    const cards = document.querySelectorAll('.fx-card')
    const first = cards[0].textContent ?? ''
    expect(first).toMatch(/Zebra/)
  })

  it('renders notes interleaved with links in the default cards grid', async () => {
    state.links.push({
      id: 10, url: 'https://a', title: 'A link', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any)
    state.notes.push({
      id: 1, title: 'A note', slug: 'a-note', body_html: '<p>hi</p>', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    renderWithProviders(<App />)
    await waitFor(() => expect(screen.getByText('A link')).toBeInTheDocument())
    expect(screen.getByText('A note')).toBeInTheDocument()
    expect(document.querySelector('.fx-card-note-badge')).not.toBeNull()
  })

  it('pins a note through the grid-level mutation boundary', async () => {
    state.notes.push({
      id: 1, title: 'Pin this note', slug: 'pin-this-note', body_html: '', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    renderWithProviders(<App />)
    await screen.findByText('Pin this note')

    await userEvent.setup().click(screen.getByRole('button', { name: /^Pin$/i }))

    await waitFor(() => expect(state.notes[0].pinned).toBe(true))
  })

  it('confirms and deletes a note through the grid-level mutation boundary', async () => {
    state.notes.push({
      id: 1, title: 'Delete this note', slug: 'delete-this-note', body_html: '', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    renderWithProviders(<App />)
    await screen.findByText('Delete this note')
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /^Delete$/i }))
    await user.click(await screen.findByRole('button', { name: /^Delete note$/i }))

    await waitFor(() => expect(state.notes).toHaveLength(0))
  })

  it('uses the global count when every link is inside a folder', async () => {
    state.folders.push({
      id: 9, name: 'Archive', color: '#111111', parent_id: null, link_count: 2, folder_count: 0,
      preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.links.push(
      {
        id: 1, url: 'https://one.example', title: 'Folder link one', slug: 'folder-link-one', click_count: 0,
        preview_status: 'ok', pinned: false, folder_id: 9, created_at: '', updated_at: '', tags: [],
      },
      {
        id: 2, url: 'https://two.example', title: 'Folder link two', slug: 'folder-link-two', click_count: 0,
        preview_status: 'ok', pinned: false, folder_id: 9, created_at: '', updated_at: '', tags: [],
      },
    )

    renderWithProviders(<App />)

    await screen.findByRole('button', { name: /Open folder Archive/i })
    expect(screen.queryByText('Folder link one')).not.toBeInTheDocument()
    expect(screen.queryByText('Folder link two')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /All links/i })).toHaveTextContent(/All links\s*2/i)
    expect(document.querySelector('.fx-pagehead-stats .fx-stat')).toHaveTextContent(/^2links$/i)
  })

  it('does not derive the global count from loaded pagination', async () => {
    state.links.push({
      id: 1, url: 'https://loaded.example', title: 'Loaded link', slug: 'loaded-link', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    })
    const fallbackGet = vi.mocked(http.get).getMockImplementation()!
    vi.mocked(http.get).mockImplementation((async (url: string, ...rest: any[]) => {
      if (url === '/api/entries/counts') return { data: { links: 501, notes: 0 } }
      return fallbackGet(url, ...rest)
    }) as never)

    renderWithProviders(<App />)

    await screen.findByText('Loaded link')
    await waitFor(() => expect(screen.getByRole('button', { name: /All links/i })).toHaveTextContent(/All links\s*501/i))
    expect(document.querySelector('.fx-pagehead-stats .fx-stat')).toHaveTextContent(/^501links$/i)
  })

  it('A→Z sort interleaves folders, links, and notes by name', async () => {
    state.folders.push({
      id: 1, name: 'Zebra folder', color: '#000', link_count: 0, preview_links: [], preview_folders: [],
      created_at: '',
    } as any)
    state.links.push({
      id: 10, url: 'https://a', title: 'Apple link', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any)
    state.notes.push({
      id: 1, title: 'Mango note', slug: 'mango-note', body_html: '', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByText('Apple link')).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /A→Z/ }))
    const cards = document.querySelectorAll('.fx-card')
    const titles = Array.from(cards).map((c) => c.textContent ?? '')
    expect(titles[0]).toMatch(/Apple link/)
    expect(titles[1]).toMatch(/Mango note/)
    expect(titles[2]).toMatch(/Zebra folder/)
  })

  it('opens the new-note dialog via ⌥M', async () => {
    renderWithProviders(<App />)
    const user = userEvent.setup()
    // Wait for the shell before pressing. A keystroke dispatched before the
    // hotkey handler is bound is simply dropped — and findByRole's retries
    // cannot recover from that, because the dialog never opens at all, so the
    // test fails on a timeout that looks like a missing element.
    await screen.findAllByLabelText(/new link/i)
    await user.keyboard('{Alt>}m{/Alt}')
    expect(await screen.findByRole('dialog', { name: /new note/i })).toBeInTheDocument()
  })

  it('opens edit dialog with loaded fields from a note card', async () => {
    state.notes.push({
      id: 1, title: 'Editable note', slug: 'editable-note', body_html: '<p>content</p>', pinned: false,
      folder_id: null, cover_url: null, click_count: 0, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByText('Editable note')).toBeInTheDocument())
    await user.click(screen.getByText('Editable note'))
    await waitFor(() => expect(screen.getByRole('dialog', { name: /edit note/i })).toBeInTheDocument())
    await waitFor(() => expect(screen.getByPlaceholderText('Give your note a title…')).toHaveValue('Editable note'))
  })

  it('Esc closes a modal without popping the folder underneath it', async () => {
    state.folders.push({
      id: 1, name: 'A', color: '#000', parent_id: null, link_count: 0, folder_count: 0,
      preview_links: [], preview_folders: [], created_at: '',
    } as any)
    renderWithProviders(<App />)
    const user = userEvent.setup()
    // Enter folder A
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder A/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Open folder A/i }))
    // Open the LinkDialog (new link) — sits on top of the folder view
    await user.keyboard('{Alt>}n{/Alt}')
    expect(await screen.findByRole('dialog', { name: /new link/i })).toBeInTheDocument()
    // Esc should close ONLY the dialog. The folder stays open.
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: /new link/i })).not.toBeInTheDocument(),
    )
    // Still inside A — the home page-head should NOT be visible.
    expect(screen.queryByText(/Your link base/i)).not.toBeInTheDocument()
  })

  it('creating a subfolder while inside a folder shows it in the grid (level 3)', async () => {
    // Seed: root folder A, subfolder B inside A.
    state.folders.push({
      id: 1, name: 'A', color: '#000', parent_id: null, link_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.folders.push({
      id: 2, name: 'B', color: '#000', parent_id: 1, link_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    renderWithProviders(<App />)
    const user = userEvent.setup()
    // Home renders A. Click to enter A.
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder A/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Open folder A/i }))
    // Inside A, we should see B as a child folder. Click to enter B.
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder B/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Open folder B/i }))
    // Inside B (level 2). Open the "Nova pasta" CTA and create "C" — should
    // land as a child of B (level 3).
    await user.click(screen.getByRole('button', { name: /new folder/i }))
    await user.type(screen.getByLabelText('folder name'), 'C')
    await user.click(screen.getByRole('button', { name: /Create folder/i }))
    // After save, the grid inside B should show folder C.
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /Open folder C/i })).toBeInTheDocument(),
    )
    // Verify state: C exists with parent_id=B(2).
    const c = state.folders.find((f) => f.name === 'C')
    expect(c?.parent_id).toBe(2)
  })

  it('viewMode is per-context — folder remembers a different choice than home', async () => {
    state.folders.push({
      id: 1, name: 'Trabalho', color: '#0EA5E9', link_count: 0, preview_links: [], preview_folders: [],
      created_at: '',
    } as any)
    state.links.push({
      id: 9, url: 'https://x', title: 'Solto', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any)
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Trabalho/i })).toBeInTheDocument())
    // Enter folder (cards mode is default)
    await user.click(screen.getByRole('button', { name: /Open folder Trabalho/i }))
    // Switch the folder to compact
    await user.click(screen.getByRole('button', { name: /^Compact$/i }))
    const map = JSON.parse(localStorage.getItem('foldex.viewMode.map') ?? '{}')
    expect(map['folder.1']).toBe('compact')
    expect(map['home']).toBeUndefined()
  })

  it('opens the new-link dialog via ⌥N', async () => {
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await screen.findAllByLabelText(/new link/i)
    await user.keyboard('{Alt>}n{/Alt}')
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })

  it('opens edit dialog from a card', async () => {
    state.links.push({
      id: 1, url: 'https://a', title: 'Alpha', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any)
    renderWithProviders(<App />)
    await waitFor(() => expect(screen.getByText('Alpha')).toBeInTheDocument())
    const user = userEvent.setup()
    const editBtns = screen.getAllByRole('button', { name: /^edit$/i })
    await user.click(editBtns[0])
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })

  it('drops a home link onto a folder and removes the card from the grid', async () => {
    state.folders.push({
      id: 3, name: 'Tools', color: '#111111', parent_id: null, has_password: false,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.links.push({
      id: 1, url: 'https://a', title: 'Alpha', slug: 'alpha', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    })
    renderWithProviders(<App />)
    await waitFor(() => expect(screen.getByText('Alpha')).toBeInTheDocument())
    fireEvent.drop(document.querySelector('.fx-folder-card') as HTMLElement, {
      dataTransfer: {
        types: ['application/x-foldex-link'],
        getData: (type: string) => type === 'application/x-foldex-link' ? '1' : '',
      },
    })
    await waitFor(() => expect(state.links[0].folder_id).toBe(3))
    await waitFor(() => expect(screen.queryByText('Alpha')).not.toBeInTheDocument())
  })

  it('merges a dropped note and link into a new root folder and opens the naming dialog', async () => {
    state.links.push({
      id: 1, url: 'https://a', title: 'Alpha', slug: 'alpha', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    })
    state.notes.push({
      id: 2, title: 'Beta note', slug: 'beta-note', body_html: '', click_count: 0,
      pinned: false, folder_id: null, cover_url: null, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    renderWithProviders(<App />)
    await waitFor(() => expect(screen.getByText('Beta note')).toBeInTheDocument())

    fireEvent.drop(screen.getByText('Alpha').closest('.fx-card') as HTMLElement, {
      dataTransfer: {
        types: ['application/x-foldex-note'],
        getData: (type: string) => type === 'application/x-foldex-note' ? '2' : '',
      },
    })

    await waitFor(() => expect(state.folders).toHaveLength(1))
    await waitFor(() => expect(state.links[0].folder_id).toBe(state.folders[0].id))
    await waitFor(() => expect(state.notes[0].folder_id).toBe(state.folders[0].id))
    expect(state.folders[0].parent_id).toBeNull()
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    expect(screen.queryByLabelText(/delete folder/i)).not.toBeInTheDocument()
  })

  it('surfaces a partial merge failure, reconciles the view, and skips the success dialog', async () => {
    state.links.push({
      id: 1, url: 'https://a', title: 'Alpha', slug: 'alpha', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    })
    state.notes.push({
      id: 2, title: 'Beta note', slug: 'beta-note', body_html: '', click_count: 0,
      pinned: false, folder_id: null, cover_url: null, last_clicked_at: null,
      created_at: '', updated_at: '', tags: [],
    })
    const originalPatch = vi.mocked(http.patch).getMockImplementation()!
    vi.mocked(http.patch).mockImplementation(((url: string, ...args: unknown[]) => {
      if (url === '/api/notes/2') return Promise.reject(new Error('offline'))
      return originalPatch(url, ...args)
    }) as typeof http.patch)
    renderWithProviders(<App />)
    await screen.findByText('Beta note')

    fireEvent.drop(screen.getByText('Alpha').closest('.fx-card') as HTMLElement, {
      dataTransfer: {
        types: ['application/x-foldex-note'],
        getData: (type: string) => type === 'application/x-foldex-note' ? '2' : '',
      },
    })

    expect(await screen.findByRole('alert')).toHaveTextContent(/not every item.*refreshed.*move the remaining item/i)
    await waitFor(() => expect(state.links[0].folder_id).toBe(state.folders[0]?.id))
    expect(state.notes[0].folder_id).toBeNull()
    expect(await screen.findByRole('button', { name: /Open folder New folder/i })).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByText('Alpha')).not.toBeInTheDocument())
    expect(screen.getByText('Beta note')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('clicking a locked folder shows the password prompt instead of navigating', async () => {
    state.folders.push({
      id: 1, name: 'Secret', color: '#000', parent_id: null, has_password: true,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.folderPasswords[1] = 'hunter22'
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Secret/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Open folder Secret/i }))
    // The password prompt appears...
    expect(await screen.findByText(/Secret/, { selector: 'h2' })).toBeInTheDocument()
    expect(screen.getByLabelText('folder password')).toBeInTheDocument()
    // ...and we never actually navigated: the SAME "Open folder Secret" card
    // is still on screen underneath the prompt (it would be gone — replaced
    // by the folder's own contents/breadcrumb — had requestOpenFolder called
    // setOpenFolder before/instead of prompting).
    expect(screen.getByRole('button', { name: /Open folder Secret/i })).toBeInTheDocument()
  })

  it('entering the correct password unlocks and navigates into the folder', async () => {
    state.folders.push({
      id: 1, name: 'Secret', color: '#000', parent_id: null, has_password: true,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.folderPasswords[1] = 'hunter22'
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Secret/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Open folder Secret/i }))
    await user.type(await screen.findByLabelText('folder password'), 'hunter22')
    await user.click(screen.getByRole('button', { name: /unlock/i }))
    // Prompt closes and we're now inside the folder — the breadcrumb back
    // control is folder-view-only, and the prompt input is gone.
    await waitFor(() => expect(screen.queryByLabelText('folder password')).not.toBeInTheDocument())
    expect(screen.queryByText(/Your link base/i)).not.toBeInTheDocument()
  })

  it('forwards the current unlock token when deleting the open protected folder', async () => {
    state.folders.push({
      id: 1, name: 'Secret', color: '#000', parent_id: null, has_password: true,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.folderPasswords[1] = 'hunter22'
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Secret/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Open folder Secret/i }))
    await user.type(await screen.findByLabelText('folder password'), 'hunter22')
    await user.click(screen.getByRole('button', { name: /unlock/i }))
    await waitFor(() => expect(screen.queryByLabelText('folder password')).not.toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: /edit folder Secret/i }))
    await user.click(await screen.findByLabelText(/delete folder, keep links/i))
    await user.click(await screen.findByRole('button', { name: /^Delete folder$/i }))
    await waitFor(() => expect(state.folders).toHaveLength(0))
  })

  it('prompts for the password when deleting a protected folder from its card', async () => {
    state.folders.push({
      id: 1, name: 'Secret', color: '#000', parent_id: null, has_password: true,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.folderPasswords[1] = 'hunter22'
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Secret/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /^Edit folder$/i }))
    await user.click(await screen.findByLabelText(/delete folder, keep links/i))
    await user.click(await screen.findByRole('button', { name: /^Delete folder$/i }))

    await user.type(await screen.findByLabelText('folder password'), 'hunter22')
    await user.click(screen.getByRole('button', { name: /unlock/i }))
    await waitFor(() => expect(state.folders).toHaveLength(0))
  })

  it('wrong password keeps the prompt open with an inline error and never navigates', async () => {
    state.folders.push({
      id: 1, name: 'Secret', color: '#000', parent_id: null, has_password: true,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.folderPasswords[1] = 'hunter22'
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Secret/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Open folder Secret/i }))
    await user.type(await screen.findByLabelText('folder password'), 'wrong-guess')
    await user.click(screen.getByRole('button', { name: /unlock/i }))
    // First wrong attempt surfaces the attempts-remaining message (ADR-28 rate limit).
    expect(await screen.findByText(/attempts left before lockout/i)).toBeInTheDocument()
    expect(screen.getByLabelText('folder password')).toBeInTheDocument()
  })

  it('jumping to a locked folder via the Command Palette also prompts for the password', async () => {
    state.folders.push({
      id: 1, name: 'Secret', color: '#000', parent_id: null, has_password: true,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.folderPasswords[1] = 'hunter22'
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await user.keyboard('{Alt>}k{/Alt}')
    const palette = await screen.findByRole('dialog')
    await user.type(within(palette).getByPlaceholderText(/Search by.*action/i), 'Secret')
    await user.click(await within(palette).findByRole('button', { name: /open folder Secret/i }))
    // Palette closes; the password prompt takes over.
    await waitFor(() => expect(screen.queryByPlaceholderText(/Search by.*action/i)).not.toBeInTheDocument())
    expect(await screen.findByLabelText('folder password')).toBeInTheDocument()
  })

  it('does not reprompt for a folder already unlocked this session', async () => {
    state.folders.push({
      id: 1, name: 'Secret', color: '#000', parent_id: null, has_password: true,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.folderPasswords[1] = 'hunter22'
    renderWithProviders(<App />)
    const user = userEvent.setup()
    // Unlock once.
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Secret/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Open folder Secret/i }))
    await user.type(await screen.findByLabelText('folder password'), 'hunter22')
    await user.click(screen.getByRole('button', { name: /unlock/i }))
    await waitFor(() => expect(screen.queryByLabelText('folder password')).not.toBeInTheDocument())
    // Leave the folder (Esc pops the folder-navigation stack — same
    // affordance as the breadcrumb's "back" button).
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Secret/i })).toBeInTheDocument())
    // Re-enter: the cached unlock token must skip the prompt entirely.
    await user.click(screen.getByRole('button', { name: /Open folder Secret/i }))
    await waitFor(() => expect(screen.queryByText(/Your link base/i)).not.toBeInTheDocument())
    expect(screen.queryByLabelText('folder password')).not.toBeInTheDocument()
  })

  // RACE-HER-011: ref is updated synchronously on unlock so a rapid second
  // open (before useEffect paint sync) must not re-open the password dialog.
  it('double-open after unlock does not reprompt (sync unlockedFoldersRef)', async () => {
    state.folders.push({
      id: 1, name: 'Secret', color: '#000', parent_id: null, has_password: true,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.folderPasswords[1] = 'hunter22'
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Secret/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Open folder Secret/i }))
    await user.type(await screen.findByLabelText('folder password'), 'hunter22')
    await user.click(screen.getByRole('button', { name: /unlock/i }))
    await waitFor(() => expect(screen.queryByLabelText('folder password')).not.toBeInTheDocument())
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Secret/i })).toBeInTheDocument())
    const openBtn = screen.getByRole('button', { name: /Open folder Secret/i })
    await user.dblClick(openBtn)
    await waitFor(() => expect(screen.queryByText(/Your link base/i)).not.toBeInTheDocument())
    expect(screen.queryByLabelText('folder password')).not.toBeInTheDocument()
  })

  it('recovers from a stale unlock token (password changed elsewhere mid-session)', async () => {
    state.folders.push({
      id: 1, name: 'Secret', color: '#000', parent_id: null, has_password: true,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.folderPasswords[1] = 'hunter22'
    renderWithProviders(<App />)
    const user = userEvent.setup()
    // Unlock and enter.
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Secret/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Open folder Secret/i }))
    await user.type(await screen.findByLabelText('folder password'), 'hunter22')
    await user.click(screen.getByRole('button', { name: /unlock/i }))
    await waitFor(() => expect(screen.queryByLabelText('folder password')).not.toBeInTheDocument())
    // Simulate the password having been changed in another tab: the cached
    // token is now stale server-side. A subsequent gated request (the search
    // box re-triggering the entries query — via fireEvent.change, not
    // userEvent.type, which triggers the palette's parent-div onClick in
    // this suite, see the "Use fireEvent.change" precedent above) must come
    // back 403 folder_locked, which App.tsx's defensive effect should catch
    // by dropping the stale token and navigating back out — never getting
    // stuck showing a broken/empty folder view.
    state.folderPasswords[1] = 'new-password-set-elsewhere'
    fireEvent.change(screen.getByLabelText(/^Search$/i), { target: { value: 'x' } })
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Secret/i })).toBeInTheDocument(), { timeout: 3000 })
    expect(screen.queryByText(/Your link base/i)).toBeInTheDocument()
    // Back on the home grid — re-entering must prompt again (the stale
    // token was dropped, not silently reused).
    await user.click(screen.getByRole('button', { name: /Open folder Secret/i }))
    expect(await screen.findByLabelText('folder password')).toBeInTheDocument()
  })

  it('palette reveal opens the folder and highlights the link', async () => {
    state.folders.push({
      id: 1, name: 'Work', color: '#000', parent_id: null, has_password: false,
      link_count: 1, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.links.push({
      id: 10, url: 'https://news.ycombinator.com', title: 'Hacker News', slug: 'hn',
      click_count: 0, preview_status: 'ok', folder_id: 1, created_at: '', updated_at: '', tags: [],
    } as any)
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Work/i })).toBeInTheDocument())
    expect(screen.queryByText('Hacker News')).not.toBeInTheDocument()
    await user.click(screen.getByLabelText(/^Search$/i))
    const palette = await screen.findByRole('dialog', { name: /command palette/i })
    await waitFor(() => expect(within(palette).getAllByText('Hacker News').length).toBeGreaterThan(0))
    await user.click(within(palette).getAllByRole('button', { name: 'Show in Work' })[0])
    await waitFor(() => expect(screen.queryByRole('dialog', { name: /command palette/i })).not.toBeInTheDocument())
    expect(screen.getByRole('heading', { name: 'Work' })).toBeInTheDocument()
    const card = document.querySelector('[data-entry="link-10"]')
    expect(card).toBeInTheDocument()
    expect(card).toHaveClass('fx-entry-reveal')
  })

  it('palette reveal of a home link leaves a folder and highlights the card', async () => {
    state.folders.push({
      id: 1, name: 'Work', color: '#000', parent_id: null, has_password: false,
      link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    } as any)
    state.links.push({
      id: 10, url: 'https://example.com', title: 'Home link', slug: 'home-link',
      click_count: 0, preview_status: 'ok', folder_id: null, created_at: '', updated_at: '', tags: [],
    } as any)
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Work/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Open folder Work/i }))
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Work' })).toBeInTheDocument())
    await user.click(screen.getByLabelText(/^Search$/i))
    const palette = await screen.findByRole('dialog', { name: /command palette/i })
    await waitFor(() => expect(within(palette).getAllByText('Home link').length).toBeGreaterThan(0))
    await user.click(within(palette).getAllByRole('button', { name: 'Show on Home' })[0])
    await waitFor(() => expect(screen.queryByRole('heading', { level: 1, name: 'Work' })).not.toBeInTheDocument())
    expect(screen.getByText('Home link')).toBeInTheDocument()
    expect(document.querySelector('[data-entry="link-10"]')).toHaveClass('fx-entry-reveal')
  })

  it('palette reveal replaces the folder path instead of nesting', async () => {
    state.folders.push(
      {
        id: 1, name: 'Work', color: '#000', parent_id: null, has_password: false,
        link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
      } as any,
      {
        id: 2, name: 'Personal', color: '#111', parent_id: null, has_password: false,
        link_count: 1, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
      } as any,
    )
    state.links.push({
      id: 10, url: 'https://personal.test', title: 'Personal link', slug: 'personal-link',
      click_count: 0, preview_status: 'ok', folder_id: 2, created_at: '', updated_at: '', tags: [],
    } as any)
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByRole('button', { name: /Open folder Work/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Open folder Work/i }))
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Work' })).toBeInTheDocument())
    await user.click(screen.getByLabelText(/^Search$/i))
    const palette = await screen.findByRole('dialog', { name: /command palette/i })
    await waitFor(() => expect(within(palette).getAllByText('Personal link').length).toBeGreaterThan(0))
    await user.click(within(palette).getAllByRole('button', { name: 'Show in Personal' })[0])
    await waitFor(() => expect(screen.getByRole('heading', { level: 1, name: 'Personal' })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: '← Folders' }))
    await waitFor(() => expect(screen.queryByRole('heading', { level: 1, name: 'Personal' })).not.toBeInTheDocument())
    expect(screen.queryByRole('heading', { level: 1, name: 'Work' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Open folder Work/i })).toBeInTheDocument()
  })

  it('palette edit opens the link dialog when content.write is granted', async () => {
    state.links.push({
      id: 10, url: 'https://example.com', title: 'Example', slug: 'example',
      click_count: 0, preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any)
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await waitFor(() => expect(screen.getByText('Example')).toBeInTheDocument())
    await user.click(screen.getByLabelText(/^Search$/i))
    const palette = await screen.findByRole('dialog', { name: /command palette/i })
    await user.click(within(palette).getAllByRole('button', { name: 'Edit link' })[0])
    await waitFor(() => expect(screen.queryByRole('dialog', { name: /command palette/i })).not.toBeInTheDocument())
    expect(await screen.findByDisplayValue('https://example.com')).toBeInTheDocument()
  })
})
