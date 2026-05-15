import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CommandPalette } from './CommandPalette'
import { renderWithProviders } from '../test/renderWithProviders'
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
    renderWithProviders(<CommandPalette open={false} onClose={vi.fn()} />)
    await new Promise((r) => setTimeout(r, 50))
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

  it('debounces: fires one query per settled input, not one per keystroke', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime.bind(vi) })

    renderWithProviders(<CommandPalette open onClose={vi.fn()} />)
    const input = await screen.findByPlaceholderText(/Search by/i)

    await user.type(input, 'hack')

    const callsDuring = (http.get as ReturnType<typeof vi.spyOn>).mock.calls
      .filter(([u]: [string]) => u.includes('q=hack')).length
    expect(callsDuring).toBe(0)

    await vi.advanceTimersByTimeAsync(250)

    await waitFor(() => {
      const callsAfter = (http.get as ReturnType<typeof vi.spyOn>).mock.calls
        .filter(([u]: [string]) => u.includes('q=hack')).length
      expect(callsAfter).toBe(1)
    })
  })
})
