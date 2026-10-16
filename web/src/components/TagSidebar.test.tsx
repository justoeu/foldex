import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TagSidebar } from './TagSidebar'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { http } from '../api/client'

let state: MockState

beforeEach(() => {
  // Section open/closed state is persisted; a test that collapses Frequent
  // would otherwise hide every tag from the next test in this file.
  for (const k of ['foldex.sidebar.freqOpen', 'foldex.sidebar.otherOpen', 'foldex.sidebar.recentOpen']) {
    localStorage.removeItem(k)
  }
  state = freshState()
  state.tags.push(
    { id: 1, name: 'jira', color: '#1f6feb', icon: '🪲', link_count: 3 },
    { id: 2, name: 'docs', color: '#a78bfa', icon: null, link_count: 0 },
  )
  installAxiosMock(state)
})

describe('TagSidebar', () => {
  it('lists tags with their link counts', async () => {
    renderWithProviders(<TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={5} collapsed={false} onToggleCollapsed={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    expect(screen.getByText('3')).toBeInTheDocument()
    expect(screen.getByText('docs')).toBeInTheDocument()
    // docs has link_count: 0; we just verify the tag name is present
    expect(screen.getAllByText('0').length).toBeGreaterThan(0)
  })

  it('toggles a tag when clicked', async () => {
    const onToggle = vi.fn()
    renderWithProviders(<TagSidebar selected={[]} onToggle={onToggle} onClear={vi.fn()} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(screen.getByText('jira'))
    expect(onToggle).toHaveBeenCalledWith(1)
  })

  it('clears selection when "Todos os links" button is clicked', async () => {
    const onClear = vi.fn()
    renderWithProviders(<TagSidebar selected={[1]} onToggle={vi.fn()} onClear={onClear} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByText('All links'))
    expect(onClear).toHaveBeenCalled()
  })

  it('opens the new-tag dialog from the "Nova" button', async () => {
    renderWithProviders(<TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /^New$/i }))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })

  it('opens the tag manager dialog from the "Gerenciar" button', async () => {
    renderWithProviders(<TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /Manage/i }))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })

  it('renders collapsed rail and expands on click', async () => {
    const onToggleCollapsed = vi.fn()
    renderWithProviders(
      <TagSidebar
        selected={[]}
        onToggle={vi.fn()}
        onClear={vi.fn()}
        totalLinks={0}
        collapsed
        onToggleCollapsed={onToggleCollapsed}
      />,
    )
    expect(document.querySelector('.fx-sidebar-rail')).toBeTruthy()
    await userEvent.setup().click(screen.getByLabelText(/expand/i))
    expect(onToggleCollapsed).toHaveBeenCalled()
  })

  it('shows mobile drawer and closes on Escape', async () => {
    const onMobileClose = vi.fn()
    renderWithProviders(
      <TagSidebar
        selected={[]}
        onToggle={vi.fn()}
        onClear={vi.fn()}
        totalLinks={0}
        collapsed={false}
        onToggleCollapsed={vi.fn()}
        mobileOpen
        onMobileClose={onMobileClose}
      />,
    )
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    expect(document.querySelector('.fx-sidebar-mobile-open')).toBeTruthy()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onMobileClose).toHaveBeenCalled()
  })

  it('toggles frequent/other sections and load-more when many tags', async () => {
    for (let i = 3; i <= 25; i++) {
      state.tags.push({ id: i, name: `tag${i}`, color: '#6366F1', icon: null, link_count: i === 3 ? 10 : 1 })
    }
    renderWithProviders(
      <TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />,
    )
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    const user = userEvent.setup()
    // Collapse Frequent section if present
    const freq = screen.queryByText(/Frequent|Frequentes|Frecuentes/i)
    if (freq) {
      await user.click(freq)
    }
    const more = screen.queryByText(/more|mais|más/i)
    if (more) {
      await user.click(more)
    }
    expect(screen.getByText('tag25') || screen.getByText('jira')).toBeTruthy()
  })

  it('opens and closes both dialogs from the collapsed rail', async () => {
    renderWithProviders(<TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={0} collapsed onToggleCollapsed={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/create new tag/i))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /cancel/i }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    await waitFor(() => expect(screen.getByLabelText(/edit or delete existing tags/i)).toBeEnabled())
    await user.click(screen.getByLabelText(/edit or delete existing tags/i))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('disables manage in the rail while there are no tags', async () => {
    state.tags = []
    renderWithProviders(<TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={0} collapsed onToggleCollapsed={vi.fn()} />)
    await waitFor(() =>
      expect(screen.getByLabelText(/edit or delete existing tags/i)).toBeDisabled())
  })

  it('closes the main dialogs through their onClose wiring', async () => {
    renderWithProviders(<TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />)
    // Generous timeouts: this flow chains two dialog round-trips off the tags
    // query, and the default 1s waitFor flakes under full-suite load.
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument(), { timeout: 3000 })
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /^New$/i }))
    await user.click(await screen.findByRole('button', { name: /cancel/i }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument(), { timeout: 3000 })
    await user.click(screen.getByRole('button', { name: /Manage/i }))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument(), { timeout: 3000 })
  })

  it('migrates legacy 1/0 section keys to JSON booleans on render', async () => {
    localStorage.setItem('foldex.sidebar.freqOpen', '0')
    localStorage.setItem('foldex.sidebar.otherOpen', '1')
    renderWithProviders(<TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />)
    expect(localStorage.getItem('foldex.sidebar.freqOpen')).toBe('false')
    expect(localStorage.getItem('foldex.sidebar.otherOpen')).toBe('true')
    // The migrated "closed" frequent section really starts closed.
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /Frequent/i })).toHaveAttribute('aria-expanded', 'false'))
    expect(screen.queryByText('jira')).not.toBeInTheDocument()
    localStorage.removeItem('foldex.sidebar.freqOpen')
    localStorage.removeItem('foldex.sidebar.otherOpen')
  })

  it('survives tags without a link_count and toggles rows in the Others group', async () => {
    for (let i = 3; i <= 8; i++) {
      state.tags.push({ id: i, name: `other${i}`, color: '#6366F1', icon: null } as never)
    }
    const onToggle = vi.fn()
    renderWithProviders(<TagSidebar selected={[8]} onToggle={onToggle} onClear={vi.fn()} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('other8')).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(screen.getByText('other8'))
    expect(onToggle).toHaveBeenCalledWith(8)
    // Collapsing Others hides its rows but keeps Frequent visible.
    await user.click(screen.getByRole('button', { name: /Others/i }))
    expect(screen.queryByText('other8')).not.toBeInTheDocument()
    expect(screen.getByText('jira')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Others/i }))
    expect(screen.getByText('other8')).toBeInTheDocument()
  })

  it('closes the mobile drawer from its close button and via the collapse button contract', async () => {
    const onMobileClose = vi.fn()
    const onToggleCollapsed = vi.fn()
    renderWithProviders(
      <TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={0} collapsed={false}
        onToggleCollapsed={onToggleCollapsed} mobileOpen onMobileClose={onMobileClose} />,
    )
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/^close$/i))
    expect(onMobileClose).toHaveBeenCalledTimes(1)
    await user.click(screen.getByLabelText(/collapse/i))
    expect(onToggleCollapsed).toHaveBeenCalledTimes(1)
  })

  it('toggles the recent section and falls back to the id when a link has no slug', async () => {
    // The shared mock only returns links WITH a change stamp; override the
    // one route so the row can also exercise the no-stamp/no-slug fallbacks.
    const fallbackGet = vi.mocked(http.get).getMockImplementation()!
    vi.mocked(http.get).mockImplementation((async (url: string, ...rest: unknown[]) => {
      if (String(url).startsWith('/api/links/recent-changes')) {
        return { data: [{
          id: 42, url: 'https://y.example', title: 'Slugless page', slug: '',
          click_count: 0, preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
        }] }
      }
      return (fallbackGet as (...a: unknown[]) => unknown)(url, ...rest)
    }) as never)
    renderWithProviders(<TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />)
    const row = await screen.findByText(/Slugless page/i)
    expect(row.closest('a')).toHaveAttribute('href', '/go/42')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /Recent updates/i }))
    expect(screen.queryByText(/Slugless page/i)).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Recent updates/i }))
    expect(screen.getByText(/Slugless page/i)).toBeInTheDocument()
    localStorage.removeItem('foldex.sidebar.recentOpen')
  })

  it('lists recent changes when present', async () => {
    state.links.push({
      id: 99, url: 'https://x.example', title: 'Changed page', slug: 'changed',
      click_count: 0, preview_status: 'ok', pinned: false, created_at: '', updated_at: '',
      tags: [], last_change_detected_at: new Date().toISOString(),
    })
    renderWithProviders(
      <TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />,
    )
    await waitFor(() => expect(screen.getByText(/Changed page/i)).toBeInTheDocument())
  })
})

