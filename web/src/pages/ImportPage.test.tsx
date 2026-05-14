import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ImportPage } from './ImportPage'
import { renderWithProviders } from '../test/renderWithProviders'
import { http } from '../api/client'

beforeEach(() => {
  vi.restoreAllMocks()
})

describe('ImportPage', () => {
  it('renders import + export sections', () => {
    renderWithProviders(<ImportPage onDone={vi.fn()} />)
    expect(screen.getByRole('heading', { name: 'Importar' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Exportar' })).toBeInTheDocument()
    expect(screen.getAllByText(/Bookmarks HTML/i).length).toBeGreaterThan(0)
  })

  it('exposes Export buttons that point at backend endpoints', () => {
    renderWithProviders(<ImportPage onDone={vi.fn()} />)
    const links = screen.getAllByRole('link') as HTMLAnchorElement[]
    expect(links.some((a) => a.href.includes('/api/export?format=netscape'))).toBe(true)
    expect(links.some((a) => a.href.includes('/api/export?format=json'))).toBe(true)
  })

  it('disables "Revisar e importar" when no file is picked', () => {
    renderWithProviders(<ImportPage onDone={vi.fn()} />)
    expect(screen.getByRole('button', { name: /Revisar e importar/i })).toBeDisabled()
  })

  it('opens the preview dialog when a file is picked + the button is clicked', async () => {
    const postSpy = vi.spyOn(http, 'post').mockResolvedValue({
      data: {
        format: 'netscape',
        counts: { links: 3, folders: 1, tags: 0 },
        conflicts: { links: 0, folders: 0, tags: 0 },
        folders: [{ path: 'Bookmarks Bar', name: 'Bookmarks Bar', count: 3 }],
        links: [],
        warnings: [],
      },
    } as any)
    renderWithProviders(<ImportPage onDone={vi.fn()} />)
    const user = userEvent.setup()

    const file = new File(['<DL></DL>'], 'bookmarks.html', { type: 'text/html' })
    const input = document.getElementById('foldex-file') as HTMLInputElement
    await user.upload(input, file)

    await user.click(screen.getByRole('button', { name: /Revisar e importar/i }))
    await waitFor(() => expect(screen.getByText(/Revisar antes de importar/i)).toBeInTheDocument())
    expect(postSpy).toHaveBeenCalledWith(
      '/api/import/validate',
      expect.any(FormData),
      expect.objectContaining({ headers: expect.any(Object) }),
    )
  })

  it('toggles between netscape and json format', async () => {
    renderWithProviders(<ImportPage onDone={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /Foldex JSON/i }))
    expect(screen.getByRole('button', { name: /Foldex JSON/i })).toHaveAttribute('aria-pressed', 'true')
  })
})
