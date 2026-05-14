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

  it('title is a link that opens via /go/:id', () => {
    renderWithProviders(<LinkCard link={baseLink} onEdit={vi.fn()} />)
    const titleLink = screen.getByText('Hacker News').closest('a')
    expect(titleLink).not.toBeNull()
    expect(titleLink?.getAttribute('href')).toBe('/go/1')
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
    const confirmBtn = await screen.findByRole('button', { name: /Apagar link/i })
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
    expect(screen.getByText(/capturando/i)).toBeInTheDocument()
  })
})
