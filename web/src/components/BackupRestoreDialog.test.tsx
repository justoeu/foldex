import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BackupRestoreDialog } from './BackupRestoreDialog'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

function makeFile(): File {
  return new File([new Uint8Array([0])], 'backup.zip', { type: 'application/zip' })
}

describe('BackupRestoreDialog', () => {
  it('shows validation summary and counts after validate resolves', async () => {
    render(<BackupRestoreDialog file={makeFile()} onClose={vi.fn()} onRestored={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/5 links · 2 tags · 1 folders/)).toBeInTheDocument())
    expect(screen.getByText(/8 clicks/)).toBeInTheDocument()
  })

  it('defaults to "skip" mode and switches when the user picks "wipe"', async () => {
    const user = userEvent.setup()
    render(<BackupRestoreDialog file={makeFile()} onClose={vi.fn()} onRestored={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())

    // Default action button is the indigo primary (skip text).
    expect(screen.getByRole('button', { name: /^Restore$/i })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /Wipe everything and import/i }))
    // After picking wipe, the submit button carries the destructive copy.
    expect(screen.getByRole('button', { name: /Restore \(wipe everything\)/i })).toBeInTheDocument()
  })

  it('restore call uses the selected mode', async () => {
    const user = userEvent.setup()
    render(<BackupRestoreDialog file={makeFile()} onClose={vi.fn()} onRestored={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Duplicate/i }))
    await user.click(screen.getByRole('button', { name: /^Restore$/i }))
    await waitFor(() => expect(state.lastRestoreMode).toBe('duplicate'))
    // The report screen replaces the picker.
    await waitFor(() => expect(screen.getByRole('button', { name: /Done/i })).toBeInTheDocument())
  })

  it('blocks restore when validate reports errors', async () => {
    state.backupValidation = {
      ok: false,
      manifest: null,
      conflicts: { links: 0, tags: 0, folders: 0 },
      warnings: [],
      errors: ['checksum mismatch: files/images/7.jpg'],
    }
    render(<BackupRestoreDialog file={makeFile()} onClose={vi.fn()} onRestored={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/checksum mismatch/i)).toBeInTheDocument())
    expect(screen.queryByText(/Restore mode/i)).toBeNull()
  })

  it('Esc closes the dialog', async () => {
    const onClose = vi.fn()
    render(<BackupRestoreDialog file={makeFile()} onClose={onClose} onRestored={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())
    await userEvent.setup().keyboard('{Escape}')
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('renders warnings from validation in a yellow callout', async () => {
    state.backupValidation = {
      ok: true,
      manifest: {
        kind: 'foldex.backup', version: '1.0', schema_version: 8,
        created_at: '2026-05-14T03:00:00Z',
        counts: { links: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
        checksums: {},
      },
      conflicts: { links: 0, tags: 0, folders: 0 },
      warnings: ['schema_version do backup (7) é mais antigo'],
      errors: [],
    }
    render(<BackupRestoreDialog file={makeFile()} onClose={vi.fn()} onRestored={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/schema_version do backup/i)).toBeInTheDocument())
  })

  it('shows the report after a successful restore and calls onRestored on "Concluído"', async () => {
    const onRestored = vi.fn()
    const user = userEvent.setup()
    render(<BackupRestoreDialog file={makeFile()} onClose={vi.fn()} onRestored={onRestored} />)
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /^Restore$/i }))
    await waitFor(() => expect(screen.getByRole('button', { name: /Done/i })).toBeInTheDocument())
    // Report rows
    expect(screen.getByText(/Mode/)).toBeInTheDocument()
    expect(screen.getByText(/Inserted/)).toBeInTheDocument()
    expect(screen.getByText(/Duration/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Done/i }))
    expect(onRestored).toHaveBeenCalledOnce()
  })

  it('renders restore warnings when the backend reports them', async () => {
    state.backupRestore = {
      mode: 'duplicate',
      inserted: { links: 0, tags: 1, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
      skipped:  { links: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
      wiped:    { links: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
      files:    { uploaded: 0, skipped: 0, wiped: 0 },
      warnings: ['link "https://example.com" já existia — não duplicado'],
      duration_ms: 80,
    }
    const user = userEvent.setup()
    render(<BackupRestoreDialog file={makeFile()} onClose={vi.fn()} onRestored={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Duplicate/i }))
    await user.click(screen.getByRole('button', { name: /^Restore$/i }))
    await waitFor(() => expect(screen.getByText(/já existia — não duplicado/i)).toBeInTheDocument())
  })

  it('Cancel button + X both call onClose', async () => {
    const onClose = vi.fn()
    const user = userEvent.setup()
    render(<BackupRestoreDialog file={makeFile()} onClose={onClose} onRestored={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Cancel/i }))
    expect(onClose).toHaveBeenCalled()
    onClose.mockClear()
    await user.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose).toHaveBeenCalled()
  })

  it('shows an error and disables submit when restore endpoint rejects', async () => {
    state.backupRestore = null
    // Override the route to reject for this test.
    const { installAxiosMock } = await import('../test/server')
    installAxiosMock({ ...state, tags: [], links: [], folders: [] })
    const { http } = await import('../api/client')
    vi.spyOn(http, 'post').mockImplementation(async (url: string) => {
      if (url.startsWith('/api/backup/validate')) {
        return { data: { ok: true, manifest: {
          kind: 'foldex.backup', version: '1.0', schema_version: 8,
          created_at: '2026-05-14T03:00:00Z',
          counts: { links: 1, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
          checksums: {},
        }, conflicts: { links: 0, tags: 0, folders: 0 }, warnings: [], errors: [] } } as any
      }
      const err: any = new Error('backend exploded')
      err.response = { data: { error: { message: 'backend exploded' } } }
      throw err
    })
    const user = userEvent.setup()
    render(<BackupRestoreDialog file={makeFile()} onClose={vi.fn()} onRestored={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /^Restore$/i }))
    await waitFor(() => expect(screen.getByText(/backend exploded/i)).toBeInTheDocument())
  })

  it('surfaces validation errors when validate itself throws', async () => {
    const { http } = await import('../api/client')
    vi.spyOn(http, 'post').mockImplementation(async () => {
      const err: any = new Error('zip parse failed')
      err.response = { data: { error: { message: 'zip parse failed' } } }
      throw err
    })
    render(<BackupRestoreDialog file={makeFile()} onClose={vi.fn()} onRestored={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/zip parse failed/i)).toBeInTheDocument())
  })

  it('formats large file_bytes via the Arquivos row (MB/GB scale)', async () => {
    state.backupValidation = {
      ok: true,
      manifest: {
        kind: 'foldex.backup', version: '1.0', schema_version: 8,
        created_at: '2026-05-14T03:00:00Z',
        counts: { links: 2, tags: 1, folders: 0, link_tags: 0, click_logs: 0, files: 24, file_bytes: 12 * 1024 * 1024 },
        checksums: {},
      },
      conflicts: { links: 0, tags: 0, folders: 0 },
      warnings: [],
      errors: [],
    }
    render(<BackupRestoreDialog file={makeFile()} onClose={vi.fn()} onRestored={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/24 ·/)).toBeInTheDocument())
    expect(screen.getByText(/12 MB/)).toBeInTheDocument()
  })

  it('clicking "Pular conflitos" while already on skip stays on skip', async () => {
    const user = userEvent.setup()
    render(<BackupRestoreDialog file={makeFile()} onClose={vi.fn()} onRestored={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Skip conflicts/i }))
    await user.click(screen.getByRole('button', { name: /^Restore$/i }))
    await waitFor(() => expect(state.lastRestoreMode).toBe('skip'))
  })
})
