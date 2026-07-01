import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import App from './App'
import { renderWithProviders } from './test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from './test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  state.tags.push({ id: 1, name: 'jira', color: '#1f6feb', icon: null })
  installAxiosMock(state)
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

  it('navigates to the Import page', async () => {
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /Import \/ Export/i }))
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

  it('toggles sort buttons (Novos/Top/Recentes)', async () => {
    renderWithProviders(<App />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /^Top$/i }))
    expect(screen.getByRole('button', { name: /^Top$/i })).toHaveAttribute('aria-pressed', 'true')
    await user.click(screen.getByRole('button', { name: /^Recent$/i }))
    expect(screen.getByRole('button', { name: /^Recent$/i })).toHaveAttribute('aria-pressed', 'true')
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
})
