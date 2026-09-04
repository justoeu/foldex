import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CommandPalette } from './CommandPalette'
import { renderWithProviders, testAdminUser } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { http } from '../api/client'

let state: MockState

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
    renderWithProviders(<CommandPalette open onClose={vi.fn()} />)
    const user = userEvent.setup()
    const input = await screen.findByPlaceholderText(/Search by/i)
    await user.type(input, 'Hacker')
    await waitFor(() => expect(screen.getByText('Hacker News')).toBeInTheDocument())
  })

  it('shows "no matches" when filter excludes everything', async () => {
    renderWithProviders(<CommandPalette open onClose={vi.fn()} />)
    const user = userEvent.setup()
    const input = await screen.findByPlaceholderText(/Search by/i)
    await user.type(input, 'zzzzz')
    await waitFor(() => expect(screen.getByText(/no matches/i)).toBeInTheDocument())
  })

  it('closes when a result is selected', async () => {
    const onClose = vi.fn()
    renderWithProviders(<CommandPalette open onClose={onClose} />)
    await waitFor(() => expect(screen.getAllByText('Hacker News').length).toBeGreaterThan(0))
    const user = userEvent.setup()
    await user.click(screen.getAllByText('Hacker News')[0])
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
    renderWithProviders(<CommandPalette open onClose={onClose} onRevealLink={onRevealLink} />)
    await waitFor(() => expect(screen.getAllByText('Hacker News').length).toBeGreaterThan(0))
    await userEvent.click(screen.getAllByRole('button', { name: 'Show in Work' })[0])
    expect(onRevealLink).toHaveBeenCalledTimes(1)
    expect(onRevealLink.mock.calls[0][0].id).toBe(1)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('hides the edit icon when content.write is missing', async () => {
    renderWithProviders(
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
    renderWithProviders(<CommandPalette open onClose={vi.fn()} onEditLink={onEditLink} />)
    await waitFor(() => expect(screen.getAllByText('Hacker News').length).toBeGreaterThan(0))
    await userEvent.click(screen.getAllByRole('button', { name: 'Edit link' })[0])
    expect(onEditLink).toHaveBeenCalledTimes(1)
    expect(onEditLink.mock.calls[0][0].title).toBe('Hacker News')
  })

  it('debounces: fires one query per settled input, not one per keystroke', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime.bind(vi) })

    renderWithProviders(<CommandPalette open onClose={vi.fn()} />)
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
})
