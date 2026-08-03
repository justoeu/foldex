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

const noopCardProps = {
  onEdit: vi.fn(),
  onDelete: vi.fn(),
  onPin: vi.fn(),
  onRefreshPreview: vi.fn(),
  onMarkSeen: vi.fn(),
}

describe('LinkCard', () => {
  it('renders title, hostname, tag chips and click counter', () => {
    renderWithProviders(<LinkCard link={baseLink} {...noopCardProps} />)
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
    renderWithProviders(<LinkCard link={{ ...baseLink, description: longDesc }} {...noopCardProps} />)
    const desc = document.querySelector('.fx-card-desc')
    expect(desc).not.toBeNull()
    expect(desc!.textContent!.length).toBeLessThanOrEqual(201) // 200 + the "…"
    expect(desc!.textContent!.endsWith('…')).toBe(true)
  })

  it('keeps short descriptions untouched (no ellipsis)', () => {
    renderWithProviders(<LinkCard link={baseLink} {...noopCardProps} />)
    const desc = document.querySelector('.fx-card-desc')
    expect(desc?.textContent).toBe('Tech news.')
  })

  it('title is a link that opens via /go/{slug}', () => {
    renderWithProviders(<LinkCard link={baseLink} {...noopCardProps} />)
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
        {...noopCardProps}
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
    renderWithProviders(<LinkCard link={many} {...noopCardProps} />)
    expect(screen.getByText('a')).toBeInTheDocument()
    expect(screen.getByText('e')).toBeInTheDocument()
  })

  it('shows retry button when preview failed and triggers mutation', async () => {
    state.links.push({ ...baseLink, preview_status: 'failed' })
    const failed = { ...baseLink, preview_status: 'failed' as const }
    renderWithProviders(<LinkCard link={failed} {...noopCardProps} />)
    const buttons = screen.getAllByRole('button')
    expect(buttons.length).toBeGreaterThan(2)
  })

  it('calls onEdit when edit button is clicked', async () => {
    const onEdit = vi.fn()
    renderWithProviders(<LinkCard link={baseLink} onEdit={onEdit} onDelete={vi.fn()} onPin={vi.fn()} onRefreshPreview={vi.fn()} onMarkSeen={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /edit/i }))
    expect(onEdit).toHaveBeenCalledWith(baseLink)
  })

  it('calls onDelete when delete button is clicked (confirm lives in parent)', async () => {
    const onDelete = vi.fn()
    renderWithProviders(<LinkCard link={baseLink} {...noopCardProps} onDelete={onDelete} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /delete/i }))
    expect(onDelete).toHaveBeenCalledWith(baseLink)
  })

  it('handles invalid URL gracefully (no throw)', () => {
    renderWithProviders(
      <LinkCard link={{ ...baseLink, url: 'not a url' }} {...noopCardProps} />,
    )
    expect(screen.getByText('Hacker News')).toBeInTheDocument()
  })

  it('drag-and-drop: dropping another link onto this card fires onMergeWith(source, target)', () => {
    const onMerge = vi.fn()
    const { container } = renderWithProviders(
      <LinkCard link={{ ...baseLink, id: 99 }} {...noopCardProps} onMergeWith={onMerge} />,
    )
    const card = container.querySelector('.fx-card') as HTMLElement
    fireEvent.drop(card, {
      dataTransfer: {
        types: ['application/x-foldex-link'],
        getData: (k: string) => (k === 'application/x-foldex-link' ? '7' : ''),
      },
    })
    expect(onMerge).toHaveBeenCalledWith({ kind: 'link', id: 7 }, 99)
  })

  it('drag-and-drop: dropping a note onto this card fires onMergeWith({kind:"note",...}, target)', () => {
    const onMerge = vi.fn()
    const { container } = renderWithProviders(
      <LinkCard link={{ ...baseLink, id: 99 }} {...noopCardProps} onMergeWith={onMerge} />,
    )
    const card = container.querySelector('.fx-card') as HTMLElement
    fireEvent.drop(card, {
      dataTransfer: {
        types: ['application/x-foldex-note'],
        getData: (k: string) => (k === 'application/x-foldex-note' ? '3' : ''),
      },
    })
    expect(onMerge).toHaveBeenCalledWith({ kind: 'note', id: 3 }, 99)
  })

  it('drag-and-drop: dropping a link onto itself is a no-op (no merge call)', () => {
    const onMerge = vi.fn()
    const { container } = renderWithProviders(
      <LinkCard link={{ ...baseLink, id: 7 }} {...noopCardProps} onMergeWith={onMerge} />,
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
      <LinkCard link={{ ...baseLink, preview_status: 'pending' }} {...noopCardProps} />,
    )
    expect(screen.getByText(/capturing/i)).toBeInTheDocument()
  })

  // ─── unseen-change badge (Phase 5) ─────────────────────────────────────
  // The badge appears only when the changecheck worker has detected a
  // change AND the user hasn't acknowledged it. Clicking it fires
  // useMarkChangeSeen, which the mock server applies optimistically;
  // the badge disappears on the next render.

  it('does NOT render unseen-change badge when last_change_detected_at is null', () => {
    renderWithProviders(<LinkCard link={baseLink} {...noopCardProps} />)
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
        {...noopCardProps}
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
        {...noopCardProps}
      />,
    )
    expect(screen.queryByLabelText(/mark update as seen/i)).not.toBeInTheDocument()
  })

  it('clicking the unseen-change badge calls onMarkSeen', async () => {
    const onMarkSeen = vi.fn()
    const link = {
      ...baseLink,
      last_change_detected_at: '2026-05-30T10:00:00Z',
      change_seen_at: null,
    }
    renderWithProviders(
      <LinkCard
        link={link}
        {...noopCardProps}
        onMarkSeen={onMarkSeen}
      />,
    )
    const badge = screen.getByLabelText(/mark update as seen/i)
    await userEvent.click(badge)
    expect(onMarkSeen).toHaveBeenCalledWith(link.id)
  })

  // ─── "Monitored" footer chip (PR #8) ──────────────────────────────────
  // The chip surfaces link.check_interval regardless of detection state.
  // The unseen-change badge above signals "you have an UPDATE"; this
  // chip signals "this link is being WATCHED". Both can coexist.
  // The tooltip's interval label is interpolated via a CONSTRUCTED i18n
  // key (`link_dialog.check_updates_` + interval) — that path is the
  // fragile bit, so the three tests below specifically exercise each
  // valid interval value to catch any silent fallback.

  it('does NOT render the "Monitored" chip when check_interval is null', () => {
    renderWithProviders(<LinkCard link={baseLink} {...noopCardProps} />)
    expect(document.querySelector('.fx-meta-monitor')).toBeNull()
    expect(screen.queryByText(/monitored/i)).not.toBeInTheDocument()
  })

  it('renders the "Monitored" chip when check_interval is "daily"', () => {
    renderWithProviders(
      <LinkCard link={{ ...baseLink, check_interval: 'daily' }} {...noopCardProps} />,
    )
    const chip = document.querySelector('.fx-meta-monitor')
    expect(chip).not.toBeNull()
    expect(chip!.textContent).toMatch(/monitored/i)
    // Tooltip carries the interval label resolved through the cross-
    // namespace i18n lookup — locks the construction shape.
    expect(chip!.getAttribute('data-tooltip')).toMatch(/every day/i)
  })

  it('renders the chip with the correct tooltip for each interval', () => {
    for (const interval of ['hourly', 'daily', 'weekly'] as const) {
      const { unmount } = renderWithProviders(
        <LinkCard link={{ ...baseLink, check_interval: interval }} {...noopCardProps} />,
      )
      const chip = document.querySelector('.fx-meta-monitor')
      expect(chip, `interval=${interval}`).not.toBeNull()
      const tip = chip!.getAttribute('data-tooltip') ?? ''
      // Locks i18n fallback hazard: if the constructed key
      // `link_dialog.check_updates_<interval>` were missing, t() would
      // return the raw key — none of these substrings would match.
      const expected = {
        hourly: /every hour/i,
        daily: /every day/i,
        weekly: /every week/i,
      }[interval]
      expect(tip, `interval=${interval}`).toMatch(expected)
      unmount()
    }
  })

  it('calls onPin via the pin badge', async () => {
    const onPin = vi.fn()
    const link = { ...baseLink, pinned: false }
    renderWithProviders(<LinkCard link={link} {...noopCardProps} onPin={onPin} />)
    await userEvent.click(screen.getByRole('button', { name: /^pin$/i }))
    expect(onPin).toHaveBeenCalledWith(link, true)
  })

  it('shows unpin label when already pinned', () => {
    renderWithProviders(<LinkCard link={{ ...baseLink, pinned: true }} {...noopCardProps} />)
    expect(screen.getByRole('button', { name: /unpin/i })).toBeInTheDocument()
    expect(document.querySelector('.fx-card-pinned')).not.toBeNull()
  })

  it('formats last_clicked_at across time buckets', () => {
    const cases: Array<{ agoMs: number; re: RegExp }> = [
      { agoMs: 10_000, re: /just now/i },
      { agoMs: 5 * 60_000, re: /5m ago/i },
      { agoMs: 3 * 60 * 60_000, re: /3h ago/i },
      { agoMs: 25 * 60 * 60_000, re: /yesterday/i },
      { agoMs: 5 * 24 * 60 * 60_000, re: /5d ago/i },
      { agoMs: 60 * 24 * 60 * 60_000, re: /\d/ },
    ]
    for (const c of cases) {
      const { unmount } = renderWithProviders(
        <LinkCard
          link={{ ...baseLink, last_clicked_at: new Date(Date.now() - c.agoMs).toISOString() }}
          {...noopCardProps}
        />,
      )
      expect(screen.getByLabelText(/last click/i).textContent).toMatch(c.re)
      unmount()
    }
  })

  it('floors last_click units near hour/day boundaries (no Math.round bump)', () => {
    // 59.6 minutes → still minutes with floor; round would show 1h.
    const almostHour = 59 * 60_000 + 40_000
    const { unmount: u1 } = renderWithProviders(
      <LinkCard
        link={{ ...baseLink, last_clicked_at: new Date(Date.now() - almostHour).toISOString() }}
        {...noopCardProps}
      />,
    )
    expect(screen.getByLabelText(/last click/i).textContent).toMatch(/59m ago/i)
    u1()

    // 23.6 hours → still hours with floor; round would show 1d/yesterday.
    const almostDay = 23 * 60 * 60_000 + 40 * 60_000
    const { unmount: u2 } = renderWithProviders(
      <LinkCard
        link={{ ...baseLink, last_clicked_at: new Date(Date.now() - almostDay).toISOString() }}
        {...noopCardProps}
      />,
    )
    expect(screen.getByLabelText(/last click/i).textContent).toMatch(/23h ago/i)
    u2()
  })

  it('shows never when last_clicked_at is null', () => {
    renderWithProviders(<LinkCard link={baseLink} {...noopCardProps} />)
    expect(screen.getByLabelText(/last click/i)).toHaveTextContent(/never/i)
  })

  it('bumps click_count optimistically when open is clicked', async () => {
    renderWithProviders(<LinkCard link={baseLink} {...noopCardProps} />)
    const open = screen.getByRole('link', { name: /open/i })
    fireEvent.click(open)
    expect(open).toHaveAttribute('href', '/go/hacker-news')
  })

  it('fires onRefreshPreview when status is not ok', async () => {
    const onRefreshPreview = vi.fn()
    renderWithProviders(
      <LinkCard
        link={{ ...baseLink, preview_status: 'failed', og_image_url: null }}
        {...noopCardProps}
        onRefreshPreview={onRefreshPreview}
      />,
    )
    await userEvent.click(screen.getByRole('button', { name: /recapture preview/i }))
    expect(onRefreshPreview).toHaveBeenCalledWith(baseLink.id)
  })

  it('sets dragging class on drag start and clears on drag end', () => {
    const { container } = renderWithProviders(<LinkCard link={baseLink} {...noopCardProps} />)
    const card = container.querySelector('.fx-card') as HTMLElement
    fireEvent.dragStart(card, {
      dataTransfer: { setData: vi.fn(), effectAllowed: 'move' },
    })
    expect(card.className).toMatch(/dragging/)
    fireEvent.dragEnd(card)
    expect(card.className).not.toMatch(/dragging/)
  })

  it('highlights on drag enter of another link and clears on leave', () => {
    const { container } = renderWithProviders(<LinkCard link={baseLink} {...noopCardProps} />)
    const card = container.querySelector('.fx-card') as HTMLElement
    fireEvent.dragEnter(card, {
      dataTransfer: { types: ['application/x-foldex-link'] },
    })
    expect(card.className).toMatch(/drop-over/)
    fireEvent.dragLeave(card, { relatedTarget: document.body })
    expect(card.className).not.toMatch(/drop-over/)
  })

  it('accepts dragOver for link and note MIME types', () => {
    const { container } = renderWithProviders(<LinkCard link={baseLink} {...noopCardProps} />)
    const card = container.querySelector('.fx-card') as HTMLElement
    const ev = { preventDefault: vi.fn(), dataTransfer: { types: ['application/x-foldex-note'], dropEffect: '' } }
    fireEvent.dragOver(card, ev)
    fireEvent.dragOver(card, {
      dataTransfer: { types: ['text/plain'], dropEffect: '' },
    })
  })

  it('collapses density when og:image errors at runtime', async () => {
    const { container } = renderWithProviders(
      <LinkCard
        link={{ ...baseLink, og_image_url: 'https://cdn.example/broken.png' }}
        {...noopCardProps}
      />,
    )
    expect(container.querySelector('.fx-card-tall')).not.toBeNull()
    const img = container.querySelector('.fx-preview img') as HTMLImageElement
    fireEvent.error(img)
    await waitFor(() => expect(container.querySelector('.fx-card-tall')).toBeNull())
  })

  it('hard-truncates description with no whitespace near the cut', () => {
    const longDesc = 'a'.repeat(250)
    renderWithProviders(<LinkCard link={{ ...baseLink, description: longDesc }} {...noopCardProps} />)
    const desc = document.querySelector('.fx-card-desc')
    expect(desc!.textContent!.endsWith('…')).toBe(true)
    expect(desc!.textContent!.length).toBeLessThanOrEqual(201)
  })

  it('ignores drop with empty payload', () => {
    const onMerge = vi.fn()
    const { container } = renderWithProviders(
      <LinkCard link={baseLink} {...noopCardProps} onMergeWith={onMerge} />,
    )
    fireEvent.drop(container.querySelector('.fx-card')!, {
      dataTransfer: { types: [], getData: () => '' },
    })
    expect(onMerge).not.toHaveBeenCalled()
  })
})
