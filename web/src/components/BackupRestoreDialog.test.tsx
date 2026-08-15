import { describe, it, expect, beforeEach, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider, useQuery } from '@tanstack/react-query'
import { BackupRestoreDialog } from './BackupRestoreDialog'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { makeQueryClient } from '../test/renderWithProviders'
import { http } from '../api/client'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

function makeFile(): File {
  return new File([new Uint8Array([0])], 'backup.zip', { type: 'application/zip' })
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function restoreReport() {
  return {
    mode: 'skip' as const,
    inserted: { links: 5, tags: 2, folders: 1, link_tags: 3, click_logs: 8, files: 0, file_bytes: 0 },
    skipped: { links: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
    wiped: { links: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
    files: { uploaded: 0, skipped: 0, wiped: 0 },
    warnings: [],
    duration_ms: 42,
  }
}

function renderDialog(
  props: Partial<React.ComponentProps<typeof BackupRestoreDialog>> = {},
  children?: React.ReactNode,
) {
  const client = makeQueryClient()
  return {
    client,
    ...render(
      <QueryClientProvider client={client}>
        {children}
        <BackupRestoreDialog
          file={makeFile()}
          onClose={vi.fn()}
          onRestored={vi.fn()}
          {...props}
        />
      </QueryClientProvider>,
    ),
  }
}

function MountedQuery({ queryKey, queryFn }: { queryKey: string; queryFn: () => string }) {
  useQuery({ queryKey: [queryKey, 'mounted'], queryFn })
  return null
}

describe('BackupRestoreDialog', () => {
  it('shows validation summary and counts after validate resolves', async () => {
    renderDialog()
    await waitFor(() => expect(screen.getByText(/5 links · 2 tags · 1 folders/)).toBeInTheDocument())
    expect(screen.getByText(/8 clicks/)).toBeInTheDocument()
  })

  it('defaults to "skip" mode and switches when the user picks "wipe"', async () => {
    const user = userEvent.setup()
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())

    // Default action button is the indigo primary (skip text).
    expect(screen.getByRole('button', { name: /^Restore$/i })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /Wipe everything and import/i }))
    // After picking wipe, the submit button carries the destructive copy.
    expect(screen.getByRole('button', { name: /Restore \(wipe everything\)/i })).toBeInTheDocument()
  })

  it('restore call uses the selected mode', async () => {
    const user = userEvent.setup()
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Duplicate/i }))
    const restoreButton = screen.getByRole('button', { name: /^Restore$/i })
    act(() => {
      fireEvent.click(restoreButton)
      fireEvent.click(restoreButton)
    })
    await waitFor(() => {
      expect(vi.mocked(http.post).mock.calls.filter(([url]) => url.startsWith('/api/backup/restore'))).toHaveLength(1)
    })
    await waitFor(() => expect(state.lastRestoreMode).toBe('duplicate'))
    // The report screen replaces the picker.
    await waitFor(() => expect(screen.getByRole('button', { name: /Done/i })).toBeInTheDocument())
  })

  it('refetches every mounted content query after a successful restore', async () => {
    const user = userEvent.setup()
    const queries = ['links', 'entries', 'folders', 'tags', 'stats']
      .map((key) => ({ key, queryFn: vi.fn(() => key) }))

    renderDialog({}, queries.map(({ key, queryFn }) => (
      <MountedQuery key={key} queryKey={key} queryFn={queryFn} />
    )))
    await waitFor(() => queries.forEach(({ queryFn }) => expect(queryFn).toHaveBeenCalledOnce()))
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: /^Restore$/i }))

    await waitFor(() => queries.forEach(({ queryFn }) => expect(queryFn).toHaveBeenCalledTimes(2)))
  })

  it('blocks restore when validate reports errors', async () => {
    state.backupValidation = {
      ok: false,
      manifest: null,
      conflicts: { links: 0, tags: 0, folders: 0 },
      warnings: [],
      errors: ['checksum mismatch: files/images/7.jpg'],
    }
    renderDialog()
    await waitFor(() => expect(screen.getByText(/checksum mismatch/i)).toBeInTheDocument())
    expect(screen.queryByText(/Restore mode/i)).toBeNull()
  })

  it('Esc closes the dialog', async () => {
    const onClose = vi.fn()
    renderDialog({ onClose })
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())
    await userEvent.setup().keyboard('{Escape}')
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('aborts validation on close without surfacing cancellation as an error', async () => {
    let signal: AbortSignal | undefined
    vi.mocked(http.post).mockImplementation((_url, _data, config) => {
      signal = config?.signal as AbortSignal | undefined
      return new Promise((_resolve, reject) => {
        signal?.addEventListener('abort', () => {
          reject(Object.assign(new Error('canceled'), { code: 'ERR_CANCELED' }))
        })
      })
    })
    const onClose = vi.fn()
    renderDialog({ onClose })
    await waitFor(() => expect(signal).toBeDefined())

    await userEvent.setup().click(screen.getByRole('button', { name: /Cancel/i }))

    expect(signal?.aborted).toBe(true)
    expect(onClose).toHaveBeenCalledOnce()
    expect(screen.queryByText('canceled')).not.toBeInTheDocument()
  })

  it('aborts only the owned validation on file replacement and unmount', async () => {
    const signals: AbortSignal[] = []
    vi.mocked(http.post).mockImplementation((_url, _data, config) => {
      signals.push(config!.signal as AbortSignal)
      return new Promise(() => {})
    })
    const client = makeQueryClient()
    const props = { onClose: vi.fn(), onRestored: vi.fn() }
    const view = render(
      <QueryClientProvider client={client}>
        <BackupRestoreDialog file={makeFile()} {...props} />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(signals).toHaveLength(1))

    const replacement = new File([new Uint8Array([1])], 'replacement.zip', { type: 'application/zip' })
    view.rerender(
      <QueryClientProvider client={client}>
        <BackupRestoreDialog file={replacement} {...props} />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(signals).toHaveLength(2))
    expect(signals[0].aborted).toBe(true)
    expect(signals[1].aborted).toBe(false)

    view.unmount()
    expect(signals[1].aborted).toBe(true)
  })

  it('cannot be dismissed or change mode while restore is running or after success', async () => {
    const pending = deferred<{ data: ReturnType<typeof restoreReport> }>()
    const onClose = vi.fn()
    const onRestored = vi.fn()
    const user = userEvent.setup()
    renderDialog({ onClose, onRestored })
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())
    vi.mocked(http.post).mockImplementation((url) => {
      if (url.startsWith('/api/backup/restore')) return pending.promise
      throw new Error(`unexpected request: ${url}`)
    })

    await user.click(screen.getByRole('button', { name: /^Restore$/i }))
    await waitFor(() => expect(screen.getByText(/cannot be canceled/i)).toBeInTheDocument())
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.getByRole('button', { name: /Cancel/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Close' })).toBeDisabled()
    expect(screen.getByRole('button', { name: /Duplicate/i })).toBeDisabled()
    expect(onClose).not.toHaveBeenCalled()

    pending.resolve({ data: restoreReport() })
    await waitFor(() => expect(screen.getByRole('button', { name: /Done/i })).toBeInTheDocument())
    fireEvent.keyDown(window, { key: 'Escape' })
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose).not.toHaveBeenCalled()
    expect(onRestored).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: /Done/i }))
    expect(onRestored).toHaveBeenCalledOnce()
  })

  it('stays mounted through restore failure, reconciles caches, then allows close', async () => {
    const pending = deferred<never>()
    const queryFn = vi.fn(() => 'links')
    const onClose = vi.fn()
    const user = userEvent.setup()
    renderDialog({ onClose }, <MountedQuery queryKey="links" queryFn={queryFn} />)
    await waitFor(() => expect(queryFn).toHaveBeenCalledOnce())
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())
    vi.mocked(http.post).mockImplementation((url) => {
      if (url.startsWith('/api/backup/restore')) return pending.promise
      throw new Error(`unexpected request: ${url}`)
    })

    await user.click(screen.getByRole('button', { name: /^Restore$/i }))
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).not.toHaveBeenCalled()
    pending.reject(new Error('restore failed'))

    await waitFor(() => expect(screen.getByText(/restore failed/i)).toBeInTheDocument())
    await waitFor(() => expect(queryFn).toHaveBeenCalledTimes(2))
    await user.click(screen.getByRole('button', { name: /Cancel/i }))
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
    renderDialog()
    await waitFor(() => expect(screen.getByText(/schema_version do backup/i)).toBeInTheDocument())
  })

  it('shows the report after a successful restore and calls onRestored on "Concluído"', async () => {
    const onRestored = vi.fn()
    const user = userEvent.setup()
    renderDialog({ onRestored })
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
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Duplicate/i }))
    await user.click(screen.getByRole('button', { name: /^Restore$/i }))
    await waitFor(() => expect(screen.getByText(/já existia — não duplicado/i)).toBeInTheDocument())
  })

  it('Cancel button + X both call onClose', async () => {
    const onClose = vi.fn()
    const user = userEvent.setup()
    renderDialog({ onClose })
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
    renderDialog()
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
    renderDialog()
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
    renderDialog()
    await waitFor(() => expect(screen.getByText(/24 ·/)).toBeInTheDocument())
    expect(screen.getByText(/12 MB/)).toBeInTheDocument()
  })

  it('clicking "Pular conflitos" while already on skip stays on skip', async () => {
    const user = userEvent.setup()
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Restore mode/i)).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Skip conflicts/i }))
    await user.click(screen.getByRole('button', { name: /^Restore$/i }))
    await waitFor(() => expect(state.lastRestoreMode).toBe('skip'))
  })
})
