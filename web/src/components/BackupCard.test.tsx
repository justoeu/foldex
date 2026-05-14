import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BackupCard } from './BackupCard'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
  localStorage.clear()
})

describe('BackupCard', () => {
  it('renders the generate button and a hint about scope', () => {
    render(<BackupCard onRestored={vi.fn()} />)
    expect(screen.getByRole('button', { name: /Gerar backup completo/i })).toBeInTheDocument()
    expect(screen.getByText(/DB \+ MinIO/i)).toBeInTheDocument()
  })

  it('shows empty history by default', () => {
    render(<BackupCard onRestored={vi.fn()} />)
    expect(screen.queryByText('Histórico')).toBeNull()
  })

  it('renders existing history entries', () => {
    localStorage.setItem('foldex.backups', JSON.stringify([
      {
        id: 'a',
        created_at: '2026-05-14T03:00:00Z',
        duration_ms: 1200,
        size_bytes: 12 * 1024 * 1024,
        counts: { links: 25, tags: 7, folders: 3, link_tags: 0, click_logs: 0, files: 24, file_bytes: 12 * 1024 * 1024 },
      },
    ]))
    render(<BackupCard onRestored={vi.fn()} />)
    expect(screen.getByText('Histórico')).toBeInTheDocument()
    expect(screen.getByText(/24 files/)).toBeInTheDocument()
    expect(screen.getByText(/25 links \/ 7 tags/)).toBeInTheDocument()
  })

  it('clicking "Gerar backup completo" appends a history entry', async () => {
    const clickSpy = vi.fn()
    const origCreate = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((tag: string) => {
      const el = origCreate(tag)
      if (tag === 'a') (el as HTMLAnchorElement).click = clickSpy
      return el
    })

    render(<BackupCard onRestored={vi.fn()} />)
    await userEvent.setup().click(screen.getByRole('button', { name: /Gerar backup completo/i }))
    await waitFor(() => expect(screen.getByText('Histórico')).toBeInTheDocument())
    expect(clickSpy).toHaveBeenCalledOnce()
  })

  it('rejects non-zip files with an error message', () => {
    const { container } = render(<BackupCard onRestored={vi.fn()} />)
    const input = container.querySelector('input[type=file]') as HTMLInputElement
    const file = new File(['hi'], 'wrong.txt', { type: 'text/plain' })
    fireEvent.change(input, { target: { files: [file] } })
    expect(screen.getByText(/precisa ser um \.zip/i)).toBeInTheDocument()
  })

  it('opens the restore dialog when a .zip is dropped', async () => {
    const { container } = render(<BackupCard onRestored={vi.fn()} />)
    const input = container.querySelector('input[type=file]') as HTMLInputElement
    const file = new File([new Uint8Array([0])], 'foo.zip', { type: 'application/zip' })
    fireEvent.change(input, { target: { files: [file] } })
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument())
    expect(screen.getByText(/Revisar backup/i)).toBeInTheDocument()
  })

  it('accepts a .zip via the drop zone (drag → drop)', async () => {
    const { container } = render(<BackupCard onRestored={vi.fn()} />)
    const zone = container.querySelector('.fx-backup-dropzone') as HTMLElement
    const file = new File([new Uint8Array([0])], 'backup.zip', { type: 'application/zip' })

    fireEvent.dragOver(zone)
    expect(zone.className).toContain('fx-backup-dropzone-drag')
    fireEvent.dragLeave(zone)
    expect(zone.className).not.toContain('fx-backup-dropzone-drag')
    fireEvent.drop(zone, { dataTransfer: { files: [file] } })

    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument())
  })

  it('opening the file picker via the dropzone click is tied to the hidden input', () => {
    const { container } = render(<BackupCard onRestored={vi.fn()} />)
    const input = container.querySelector('input[type=file]') as HTMLInputElement
    const clickSpy = vi.spyOn(input, 'click').mockImplementation(() => {})
    const zone = container.querySelector('.fx-backup-dropzone') as HTMLElement
    fireEvent.click(zone)
    expect(clickSpy).toHaveBeenCalled()
  })

  it('reacts to a cross-tab storage event by re-reading history', async () => {
    render(<BackupCard onRestored={vi.fn()} />)
    expect(screen.queryByText('Histórico')).toBeNull()
    localStorage.setItem('foldex.backups', JSON.stringify([
      {
        id: 'cross-tab',
        created_at: '2026-05-14T03:00:00Z',
        duration_ms: 800,
        size_bytes: 1024,
        counts: { links: 1, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 1, file_bytes: 1024 },
      },
    ]))
    window.dispatchEvent(new StorageEvent('storage', { key: 'foldex.backups' }))
    await waitFor(() => expect(screen.getByText('Histórico')).toBeInTheDocument())
  })

  it('closing the restore dialog clears the file', async () => {
    const { container } = render(<BackupCard onRestored={vi.fn()} />)
    const input = container.querySelector('input[type=file]') as HTMLInputElement
    const file = new File([new Uint8Array([0])], 'foo.zip', { type: 'application/zip' })
    fireEvent.change(input, { target: { files: [file] } })
    await waitFor(() => expect(screen.getByRole('dialog')).toBeInTheDocument())
    // Use Esc since we already validated that triggers onClose in the dialog.
    await userEvent.setup().keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })
})
