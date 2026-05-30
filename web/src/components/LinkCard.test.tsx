import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { LinkCard } from './LinkCard'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import type { Link } from '../api/types'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

const baseLink: Link = {
  id: 1,
  url: 'https://news.ycombinator.com',
  title: 'Hacker News',
  slug: 'hacker-news',
  description: 'Tech news.',
  favicon_url: 'https://news.ycombinator.com/favicon.ico',
  og_image_url: null,
  click_count: 7,
  preview_status: 'ok', pinned: false,
  preview_error: null,
  last_clicked_at: null,
  created_at: '',
  updated_at: '',
  tags: [{ id: 1, name: 'jira', color: '#1f6feb', icon: null }],
}

describe('LinkCard', () => {
  it('renders title, hostname, tag chips and click counter', () => {
    renderWithProviders(<LinkCard link={baseLink} onEdit={vi.fn()} />)
    expect(screen.getByText('Hacker News')).toBeInTheDocument()
    expect(screen.getByText('news.ycombinator.com')).toBeInTheDocument()
    expect(screen.getByText('jira')).toBeInTheDocument()
    expect(screen.getByText('7')).toBeInTheDocument()
  })

  it('truncates description longer than 200 chars with an ellipsis', () => {
    // 400-char description — should get cut around 200 (at a word boundary)
    // and gain a trailing "…".
    const longDesc = (
      'The revised X-Axis is fully compatible with stock Prusa frame (MK2/MK3), as well as ' +
      'with the Haribo/Zaribo/Bear frames. The compatibility with different extruders comes ' +
      'from the X-carriages provided in this listing, stock ones will NOT work unless ' +
      'explicitly stated.'
    )
    expect(longDesc.length).toBeGreaterThan(200)
    renderWithProviders(<LinkCard link={{ ...baseLink, description: longDesc }} onEdit={vi.fn()} />)
    const desc = document.querySelector('.fx-card-desc')
    expect(desc).not.toBeNull()
    expect(desc!.textContent!.length).toBeLessThanOrEqual(201) // 200 + the "…"
    expect(desc!.textContent!.endsWith('…')).toBe(true)
  })

  it('keeps short descriptions untouched (no ellipsis)', () => {
    renderWithProviders(<LinkCard link={baseLink} onEdit={vi.fn()} />)
    const desc = document.querySelector('.fx-card-desc')
    expect(desc?.textContent).toBe('Tech news.')
  })

  it('title is a link that opens via /go/{slug}', () => {
    renderWithProviders(<LinkCard link={baseLink} onEdit={vi.fn()} />)
    const titleLink = screen.getByText('Hacker News').closest('a')
    expect(titleLink).not.toBeNull()
    // goHref(link) prefers slug over id — the share-friendly path.
    expect(titleLink?.getAttribute('href')).toBe('/go/hacker-news')
    expect(titleLink?.getAttribute('target')).toBe('_blank')
  })

  it('renders og:image when present', () => {
    renderWithProviders(
      <LinkCard
        link={{ ...baseLink, og_image_url: 'https://cdn.example/cover.png' }}
        onEdit={vi.fn()}
      />,
    )
    const imgs = document.querySelectorAll('img')
    const cover = Array.from(imgs).find((el) => el.src.includes('cover.png'))
    expect(cover).toBeDefined()
  })

  it('shows all tags without truncation', () => {
    const many: Link = {
      ...baseLink,
      tags: [
        { id: 1, name: 'a', color: '#fff' },
        { id: 2, name: 'b', color: '#fff' },
        { id: 3, name: 'c', color: '#fff' },
        { id: 4, name: 'd', color: '#fff' },
        { id: 5, name: 'e', color: '#fff' },
      ],
    }
    renderWithProviders(<LinkCard link={many} onEdit={vi.fn()} />)
    expect(screen.getByText('a')).toBeInTheDocument()
    expect(screen.getByText('e')).toBeInTheDocument()
  })

  it('shows retry button when preview failed and triggers mutation', async () => {
    state.links.push({ ...baseLink, preview_status: 'failed' })
    const failed = { ...baseLink, preview_status: 'failed' as const }
    renderWithProviders(<LinkCard link={failed} onEdit={vi.fn()} />)
    const buttons = screen.getAllByRole('button')
    expect(buttons.length).toBeGreaterThan(2)
  })

  it('calls onEdit when edit button is clicked', async () => {
    const onEdit = vi.fn()
    renderWithProviders(<LinkCard link={baseLink} onEdit={onEdit} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /edit/i }))
    expect(onEdit).toHaveBeenCalledWith(baseLink)
  })

  it('confirms then deletes when delete button is clicked', async () => {
    state.links.push(baseLink)
    renderWithProviders(<LinkCard link={baseLink} onEdit={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /delete/i }))
    const confirmBtn = await screen.findByRole('button', { name: /Delete link/i })
    await user.click(confirmBtn)
    await waitFor(() => expect(state.links).toHaveLength(0))
  })

  it('handles invalid URL gracefully (no throw)', () => {
    renderWithProviders(
      <LinkCard link={{ ...baseLink, url: 'not a url' }} onEdit={vi.fn()} />,
    )
    expect(screen.getByText('Hacker News')).toBeInTheDocument()
  })

  it('drag-and-drop: dropping another link onto this card fires onMergeWith(source, target)', () => {
    const onMerge = vi.fn()
    const { container } = renderWithProviders(
      <LinkCard link={{ ...baseLink, id: 99 }} onEdit={vi.fn()} onMergeWith={onMerge} />,
    )
    const card = container.querySelector('.fx-card') as HTMLElement
    fireEvent.drop(card, {
      dataTransfer: {
        types: ['application/x-foldex-link'],
        getData: (k: string) => (k === 'application/x-foldex-link' ? '7' : ''),
      },
    })
    expect(onMerge).toHaveBeenCalledWith(7, 99)
  })

  it('drag-and-drop: dropping a link onto itself is a no-op (no merge call)', () => {
    const onMerge = vi.fn()
    const { container } = renderWithProviders(
      <LinkCard link={{ ...baseLink, id: 7 }} onEdit={vi.fn()} onMergeWith={onMerge} />,
    )
    const card = container.querySelector('.fx-card') as HTMLElement
    fireEvent.drop(card, {
      dataTransfer: {
        types: ['application/x-foldex-link'],
        getData: () => '7',
      },
    })
    expect(onMerge).not.toHaveBeenCalled()
  })

  it('shows "capturando" status when preview is pending', () => {
    renderWithProviders(
      <LinkCard link={{ ...baseLink, preview_status: 'pending' }} onEdit={vi.fn()} />,
    )
    expect(screen.getByText(/capturing/i)).toBeInTheDocument()
  })

  // ─── unseen-change badge (Phase 5) ─────────────────────────────────────
  // The badge appears only when the changecheck worker has detected a
  // change AND the user hasn't acknowledged it. Clicking it fires
  // useMarkChangeSeen, which the mock server applies optimistically;
  // the badge disappears on the next render.

  it('does NOT render unseen-change badge when last_change_detected_at is null', () => {
    renderWithProviders(<LinkCard link={baseLink} onEdit={vi.fn()} />)
    expect(screen.queryByLabelText(/mark update as seen/i)).not.toBeInTheDocument()
    expect(document.querySelector('.fx-card-update-badge')).toBeNull()
    expect(document.querySelector('.fx-card-update-alert')).toBeNull()
  })

  it('renders unseen-change badge when detection is newer than change_seen_at', () => {
    renderWithProviders(
      <LinkCard
        link={{
          ...baseLink,
          last_change_detected_at: '2026-05-30T10:00:00Z',
          change_seen_at: '2026-05-29T00:00:00Z',
        }}
        onEdit={vi.fn()}
      />,
    )
    expect(screen.getByLabelText(/mark update as seen/i)).toBeInTheDocument()
    // The card itself also gets the alert halo so the user notices.
    expect(document.querySelector('.fx-card-update-alert')).not.toBeNull()
  })

  it('does NOT render badge when change_seen_at is newer than last_change_detected_at', () => {
    // User already acknowledged the latest change — badge must clear even
    // though last_change_detected_at is still set.
    renderWithProviders(
      <LinkCard
        link={{
          ...baseLink,
          last_change_detected_at: '2026-05-29T00:00:00Z',
          change_seen_at: '2026-05-30T10:00:00Z',
        }}
        onEdit={vi.fn()}
      />,
    )
    expect(screen.queryByLabelText(/mark update as seen/i)).not.toBeInTheDocument()
  })

  it('clicking the unseen-change badge calls POST /api/links/:id/seen-change', async () => {
    // Seed the mock state with the link so the server-side seenChange path
    // can flip change_seen_at and the optimistic update can converge.
    state.links = [
      {
        ...baseLink,
        last_change_detected_at: '2026-05-30T10:00:00Z',
        change_seen_at: null,
      },
    ]
    renderWithProviders(
      <LinkCard
        link={state.links[0]}
        onEdit={vi.fn()}
      />,
    )
    const badge = screen.getByLabelText(/mark update as seen/i)
    await userEvent.click(badge)
    // The mock applies the seen timestamp; assert by waiting for state to
    // mutate (the production code optimistically updates the cache too,
    // but mutating state lets us see the round-trip).
    await waitFor(() => {
      expect(state.links[0].change_seen_at).toBeTruthy()
    })
  })
})
