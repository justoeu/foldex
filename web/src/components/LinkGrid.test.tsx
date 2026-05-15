import { describe, it, expect, vi } from 'vitest'
import { screen } from '@testing-library/react'
import { LinkGrid } from './LinkGrid'
import { renderWithProviders } from '../test/renderWithProviders'
import type { Link } from '../api/types'

describe('LinkGrid', () => {
  it('shows loading state', () => {
    renderWithProviders(<LinkGrid links={[]} isLoading onEdit={vi.fn()} />)
    expect(screen.getByText(/carregando/i)).toBeInTheDocument()
  })

  it('shows empty state', () => {
    renderWithProviders(<LinkGrid links={[]} isLoading={false} onEdit={vi.fn()} />)
    expect(screen.getByText(/Nada por aqui/i)).toBeInTheDocument()
    expect(screen.getByText(/⌘N/)).toBeInTheDocument()
  })

  it('renders each link in the grid', () => {
    const links: Link[] = [
      mk(1, 'One'),
      mk(2, 'Two'),
      mk(3, 'Three'),
    ]
    renderWithProviders(<LinkGrid links={links} isLoading={false} onEdit={vi.fn()} />)
    expect(screen.getByText('One')).toBeInTheDocument()
    expect(screen.getByText('Two')).toBeInTheDocument()
    expect(screen.getByText('Three')).toBeInTheDocument()
  })
})

function mk(id: number, title: string): Link {
  return {
    id, url: 'https://x/' + id, title, slug: title.toLowerCase(),
    click_count: 0, preview_status: 'ok', pinned: false,
    created_at: '', updated_at: '', tags: [],
  } as Link
}
