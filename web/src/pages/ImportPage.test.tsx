import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ImportPage } from './ImportPage'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  vi.restoreAllMocks()
  state = freshState()
  installAxiosMock(state)
})

describe('ImportPage', () => {
  it('renders import + export sections', () => {
    renderWithProviders(<ImportPage onDone={vi.fn()} />)
    expect(screen.getByRole('heading', { name: 'Import' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Export' })).toBeInTheDocument()
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
    expect(screen.getByRole('button', { name: /Review and import/i })).toBeDisabled()
  })

  it('opens the preview dialog when a file is picked + the button is clicked', async () => {
    renderWithProviders(<ImportPage onDone={vi.fn()} />)
    const user = userEvent.setup()

    const file = new File(['<DL></DL>'], 'bookmarks.html', { type: 'text/html' })
    const input = document.getElementById('foldex-file') as HTMLInputElement
    await user.upload(input, file)

    await user.click(screen.getByRole('button', { name: /Review and import/i }))
    await waitFor(() => expect(screen.getByText(/Review before importing/i)).toBeInTheDocument())
  })

  it('toggles between netscape and json format and updates accept + hint', async () => {
    renderWithProviders(<ImportPage onDone={vi.fn()} />)
    const user = userEvent.setup()
    const input = document.getElementById('foldex-file') as HTMLInputElement
    expect(input.accept).toMatch(/html/)

    await user.click(screen.getByRole('button', { name: /Foldex JSON/i }))
    expect(screen.getByRole('button', { name: /Foldex JSON/i })).toHaveAttribute('aria-pressed', 'true')
    expect(input.accept).toMatch(/json/)

    await user.click(screen.getByRole('button', { name: /Bookmarks HTML/i }))
    expect(screen.getByRole('button', { name: /Bookmarks HTML/i })).toHaveAttribute('aria-pressed', 'true')
    expect(input.accept).toMatch(/html/)
  })

  it('accepts a dropped file on the drop zone', async () => {
    renderWithProviders(<ImportPage onDone={vi.fn()} />)
    const file = new File(['{}'], 'export.json', { type: 'application/json' })
    const zone = screen.getByText(/Drag a file here/i).closest('div')!
    fireEvent.dragOver(zone, { dataTransfer: { files: [file] } })
    fireEvent.drop(zone, {
      dataTransfer: { files: [file] },
    })
    await waitFor(() => expect(screen.getByText('export.json')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: /Review and import/i })).not.toBeDisabled()
  })

  it('clicking the drop zone opens the hidden file input', async () => {
    renderWithProviders(<ImportPage onDone={vi.fn()} />)
    const input = document.getElementById('foldex-file') as HTMLInputElement
    const clickSpy = vi.spyOn(input, 'click')
    const zone = screen.getByText(/Drag a file here/i).closest('div')!
    fireEvent.click(zone)
    expect(clickSpy).toHaveBeenCalled()
  })

  it('clears file and calls onDone after apply finishes', async () => {
    const onDone = vi.fn()
    renderWithProviders(<ImportPage onDone={onDone} />)
    const user = userEvent.setup()

    const file = new File(['<DL></DL>'], 'bookmarks.html', { type: 'text/html' })
    const input = document.getElementById('foldex-file') as HTMLInputElement
    await user.upload(input, file)
    await user.click(screen.getByRole('button', { name: /Review and import/i }))
    await waitFor(() => expect(screen.getByText(/Review before importing/i)).toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: /Import \d+ links?/i }))
    await waitFor(() => expect(screen.getByRole('button', { name: /Done/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Done/i }))
    expect(onDone).toHaveBeenCalled()
    await waitFor(() =>
      expect(screen.queryByText('bookmarks.html')).toBeNull(),
    )
  })

  it('closes preview without applying via Esc', async () => {
    renderWithProviders(<ImportPage onDone={vi.fn()} />)
    const user = userEvent.setup()
    const file = new File(['<DL></DL>'], 'bookmarks.html', { type: 'text/html' })
    await user.upload(document.getElementById('foldex-file') as HTMLInputElement, file)
    await user.click(screen.getByRole('button', { name: /Review and import/i }))
    await waitFor(() => expect(screen.getByText(/Review before importing/i)).toBeInTheDocument())
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByText(/Review before importing/i)).toBeNull())
  })
})
