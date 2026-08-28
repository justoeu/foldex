import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AccountPage } from '../../pages/AccountPage'
import { renderWithProviders } from '../../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../../test/server'
import { http } from '../../api/client'

let state: MockState

const row = (id: number, over: Partial<MockState['activity'][number]> = {}) => ({
  id,
  action: 'link.created',
  category: 'content' as const,
  severity: 'info' as const,
  detail: null,
  ip: '191.55.8.140',
  ip_trusted: false,
  user_agent: 'Chrome/141',
  entity_kind: 'link',
  entity_id: id,
  subject: `Bookmark ${id}`,
  created_at: new Date(Date.now() - id * 60_000).toISOString(),
  ...over,
})

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

async function openActivity() {
  renderWithProviders(<AccountPage />)
  await userEvent.setup().click(await screen.findByRole('button', { name: /activity/i }))
}

describe('account page — my activity', () => {
  // The other half of ADR-46's read split. The administrative trail withholds
  // the label from everyone; here the reader IS the actor, so there is nothing
  // to withhold — and the screen has to actually show it, or the split has a
  // side that never pays off.
  it('shows the content label the administrative trail withholds', async () => {
    state.activity = [row(3), row(2)]
    await openActivity()

    expect(await screen.findByText('Bookmark 3')).toBeInTheDocument()
    expect(screen.getByText('Bookmark 2')).toBeInTheDocument()
    // Both rows are "link created", so the count is the assertion — getByText
    // would be ambiguous, and picking one arbitrarily would hide a row going
    // missing.
    expect(screen.getAllByText(/link created/i)).toHaveLength(2)
    expect(screen.getAllByTestId('fx-activity-row')).toHaveLength(2)
  })

  // A title is text the user typed. The one thing that must never happen to it
  // is being parsed as markup.
  it('renders a subject as text, never as markup', async () => {
    state.activity = [row(1, { subject: '<img src=x onerror="alert(1)">' })]
    await openActivity()

    expect(await screen.findByText('<img src=x onerror="alert(1)">')).toBeInTheDocument()
    expect(document.querySelector('img[src="x"]')).toBeNull()
  })

  it('says so when the account has done nothing yet', async () => {
    state.activity = []
    await openActivity()
    expect(await screen.findByText(/no activity recorded yet/i)).toBeInTheDocument()
  })

  // Keyset: "older" is a new request from the last id, and the pages
  // ACCUMULATE. The first version of this test could not fail — the mock
  // filters `id < before`, so page two came back empty and the assertion
  // re-checked a row that was already on screen.
  it('appends the next page to the list instead of replacing it', async () => {
    // A full page, so the feed reports another one to fetch.
    state.activity = Array.from({ length: 50 }, (_, i) => row(100 - i))
    const get = vi.spyOn(http, 'get')
    await openActivity()
    await screen.findByText('Bookmark 100')
    expect(screen.getAllByTestId('fx-activity-row')).toHaveLength(50)

    // The next page comes from BELOW the cursor, which is what the mock's
    // `id < before` filter models.
    state.activity = [...state.activity, row(50), row(49)]
    await userEvent.setup().click(screen.getByRole('button', { name: /load older/i }))

    await waitFor(() =>
      expect(get).toHaveBeenCalledWith('/api/activity', { params: { before: 51 } }),
    )
    // Both pages on screen at once — the assertion the old test could not make.
    await waitFor(() => expect(screen.getAllByTestId('fx-activity-row')).toHaveLength(52))
    expect(screen.getByText('Bookmark 100')).toBeInTheDocument()
    expect(screen.getByText('Bookmark 50')).toBeInTheDocument()
  })

  // A short page is the end of the feed: asking again would return the same
  // nothing, and a button that does nothing reads as a bug.
  it('hides the pager once a short page arrives', async () => {
    state.activity = [row(3), row(2)]
    await openActivity()
    await screen.findByText('Bookmark 3')

    expect(screen.queryByRole('button', { name: /load older/i })).not.toBeInTheDocument()
  })

  it('reports a failure rather than rendering an empty list', async () => {
    vi.spyOn(http, 'get').mockRejectedValue(new Error('boom'))
    await openActivity()
    expect(await screen.findByText(/activity could not be loaded/i)).toBeInTheDocument()
  })
})
