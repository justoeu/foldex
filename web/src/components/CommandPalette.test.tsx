import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { CommandPalette } from './CommandPalette'
import { renderWithProviders, testAdminUser } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { http } from '../api/client'
import { QueryClient } from '@tanstack/react-query'

let state: MockState

function paletteClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 60_000, staleTime: Infinity },
      mutations: { retry: false },
    },
  })
}

function seedCachedEntries(client: QueryClient) {
  client.setQueryData(['entries', '', '', 'created', 'ungrouped', 'locked', 100], {
    pages: [state.links.map((link) => ({ kind: 'link' as const, ...link }))],
    pageParams: [0],
  })
}

function renderPalette(ui: ReactNode, options?: Parameters<typeof renderWithProviders>[1]) {
  const client = options?.client ?? paletteClient()
  seedCachedEntries(client)
  return renderWithProviders(ui, { ...options, client })
}

beforeEach(() => {
  state = freshState()
  state.links.push(
    {
      id: 1, url: 'https://news.ycombinator.com', title: 'Hacker News',
      click_count: 0, preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any,
    {
      id: 2, url: 'https://example.com', title: 'Example', click_count: 0,
      preview_status: 'ok', created_at: '', updated_at: '', tags: [],
    } as any,
  )
  installAxiosMock(state)
})

afterEach(() => {
  vi.useRealTimers()
})

describe('CommandPalette', () => {
  it('closed state renders nothing visible', () => {
    renderWithProviders(<CommandPalette open={false} onClose={vi.fn()} />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('does not query the links API when closed', async () => {
    vi.useFakeTimers()
    renderWithProviders(<CommandPalette open={false} onClose={vi.fn()} />)
    await vi.advanceTimersByTimeAsync(200)
    const linkCalls = (http.get as ReturnType<typeof vi.spyOn>).mock.calls
      .filter(([u]: [string]) => u.startsWith('/api/links'))
    expect(linkCalls).toHaveLength(0)
  })

  it('lists results matching the query', async () => {
    renderPalette(<CommandPalette open onClose={vi.fn()} />)
    const user = userEvent.setup()
    const input = await screen.findByPlaceholderText(/Search by/i)
    await user.type(input, 'Hacker')
    await waitFor(() => expect(screen.getByText('Hacker News')).toBeInTheDocument())
  })

  it('shows "no matches" when filter excludes everything', async () => {
    renderPalette(<CommandPalette open onClose={vi.fn()} />)
    const user = userEvent.setup()
    const input = await screen.findByPlaceholderText(/Search by/i)
    await user.type(input, 'zzzzz')
    await waitFor(() => expect(screen.getByText(/no matches/i)).toBeInTheDocument())
  })

  it('closes when a result is selected', async () => {
    const onClose = vi.fn()
    renderPalette(<CommandPalette open onClose={onClose} />)
    const user = userEvent.setup()
    const title = await screen.findAllByText('Hacker News')
    await user.click(title[0].closest('a') ?? title[0])
    expect(onClose).toHaveBeenCalled()
  })

  it('reveals a foldered link without following /go', async () => {
    state.folders.push({
      id: 9, name: 'Work', color: '#000', parent_id: null, has_password: false,
      link_count: 1, folder_count: 0, preview_links: [], preview_folders: [], created_at: '',
    })
    state.links[0] = { ...state.links[0], folder_id: 9 } as MockState['links'][number]
    const onRevealLink = vi.fn()
    const onClose = vi.fn()
    renderPalette(<CommandPalette open onClose={onClose} onRevealLink={onRevealLink} />)
    await userEvent.click((await screen.findAllByRole('button', { name: 'Show in Work' }))[0])
    expect(onRevealLink).toHaveBeenCalledTimes(1)
    expect(onRevealLink.mock.calls[0][0].id).toBe(1)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('hides the edit icon when content.write is missing', async () => {
    renderPalette(
      <CommandPalette open onClose={vi.fn()} onRevealLink={vi.fn()} onEditLink={vi.fn()} />,
      {
        session: {
          status: 'authenticated',
          user: { ...testAdminUser, role: 'viewer' },
          csrfToken: 'test-csrf-token',
          features: { google_oauth: false, two_factor: false, email_delivery: false },
          permissions: ['content.read'],
        },
      },
    )
    await waitFor(() => expect(screen.getAllByText('Hacker News').length).toBeGreaterThan(0))
    expect(screen.queryByRole('button', { name: 'Edit link' })).not.toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: 'Show on Home' }).length).toBeGreaterThan(0)
  })

  it('edit icon calls onEditLink', async () => {
    const onEditLink = vi.fn()
    renderPalette(<CommandPalette open onClose={vi.fn()} onEditLink={onEditLink} />)
    await userEvent.click((await screen.findAllByRole('button', { name: 'Edit link' }))[0])
    expect(onEditLink).toHaveBeenCalledTimes(1)
    expect(onEditLink.mock.calls[0][0].title).toBe('Hacker News')
  })

  it('debounces: fires one query per settled input, not one per keystroke', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime.bind(vi) })

    renderPalette(<CommandPalette open onClose={vi.fn()} />)
    const input = await screen.findByPlaceholderText(/Search by/i)

    await user.type(input, 'hack')

    const callsDuring = (http.get as ReturnType<typeof vi.spyOn>).mock.calls
      .filter(([u]: [string]) => u.includes('q=hack')).length
    expect(callsDuring).toBe(0)

    await vi.advanceTimersByTimeAsync(200)

    await waitFor(() => {
      const callsAfter = (http.get as ReturnType<typeof vi.spyOn>).mock.calls
        .filter(([u]: [string]) => u.includes('q=hack')).length
      expect(callsAfter).toBe(1)
    })
  })

  it('does not refetch a 200-row entries page', async () => {
    renderPalette(<CommandPalette open onClose={vi.fn()} />)
    await waitFor(() => expect(screen.getAllByText('Hacker News').length).toBeGreaterThan(0))

    const entryListCalls = () =>
      vi.mocked(http.get).mock.calls
        .map(([url]) => String(url))
        .filter((url) => url.split('?')[0] === '/api/entries')

    expect(entryListCalls()).toEqual([])

    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText(/Search by/i), 'Hacker')
    await waitFor(() => {
      expect(entryListCalls().some((url) => url.includes('q=Hacker'))).toBe(true)
    })
    const limits = entryListCalls().map((url) => Number(new URL(url, 'https://foldex.test').searchParams.get('limit')))
    expect(limits.length).toBeGreaterThan(0)
    expect(limits.every((limit) => limit > 0 && limit < 100)).toBe(true)
    expect(entryListCalls().some((url) => /(?:\?|&)limit=200(?:&|$)/.test(url))).toBe(false)
  })
})

describe('CommandPalette keyboard activation', () => {
  // The footer promises ↵ "open via /go" and ⌘↵ "open in new tab", but the
  // input had no Enter handler — keyboard users (the palette's natural
  // audience, it opens with Alt+K) hit a dead key on every search.
  let openSpy: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)
  })

  async function search(query: string) {
    renderPalette(<CommandPalette open onClose={vi.fn()} />)
    const user = userEvent.setup()
    const input = await screen.findByPlaceholderText(/Search by/i)
    await user.type(input, query)
    await waitFor(() => expect(screen.getByText('Hacker News')).toBeInTheDocument())
    return { user, input }
  }

  it('Enter opens the first match via /go in the same tab and closes the palette', async () => {
    const onClose = vi.fn()
    renderPalette(<CommandPalette open onClose={onClose} />)
    const user = userEvent.setup()
    const input = await screen.findByPlaceholderText(/Search by/i)
    await user.type(input, 'Hacker')
    await waitFor(() => expect(screen.getByText('Hacker News')).toBeInTheDocument())
    await user.keyboard('{Enter}')
    expect(openSpy).toHaveBeenCalledWith('/go/1', '_self')
    expect(onClose).toHaveBeenCalled()
  })

  it('⌘↵ opens the first match in a new tab', async () => {
    const { user } = await search('Hacker')
    await user.keyboard('{Meta>}{Enter}{/Meta}')
    expect(openSpy).toHaveBeenCalledWith('/go/1', '_blank')
  })

  it('ArrowDown moves the highlight (aria-activedescendant) and Enter opens the second match', async () => {
    const { user, input } = await search('a') // matches Hacker News + Example
    await waitFor(() => expect(screen.getByText('Example')).toBeInTheDocument())
    expect(input).toHaveAttribute('aria-activedescendant', 'fx-cmdk-row-0')
    await user.keyboard('{ArrowDown}')
    expect(input).toHaveAttribute('aria-activedescendant', 'fx-cmdk-row-1')
    await user.keyboard('{Enter}')
    expect(openSpy).toHaveBeenCalledWith('/go/2', '_self')
  })

  it('Enter with no matches does nothing', async () => {
    renderPalette(<CommandPalette open onClose={vi.fn()} />)
    const user = userEvent.setup()
    const input = await screen.findByPlaceholderText(/Search by/i)
    await user.type(input, 'zzzzz')
    await waitFor(() => expect(screen.getByText(/no matches/i)).toBeInTheDocument())
    await user.keyboard('{Enter}')
    expect(openSpy).not.toHaveBeenCalled()
  })

  // CC-DAE-005: the empty state used to be a five-term conjunction that
  // had to grow with every new group. A tag-only match is results — this
  // is the extension-regression the derived hasResults guard makes
  // structural.
  it('a query matching only a tag is results, not "no matches"', async () => {
    state.tags.push({ id: 1, name: 'jira', color: '#1f6feb', icon: null })
    renderPalette(<CommandPalette open onClose={vi.fn()} />)
    const user = userEvent.setup()
    const input = await screen.findByPlaceholderText(/Search by/i)
    await user.type(input, 'jira')
    await waitFor(() => expect(screen.getByText(/filter by/i)).toBeInTheDocument())
    expect(screen.queryByText(/no matches/i)).not.toBeInTheDocument()
  })
})
