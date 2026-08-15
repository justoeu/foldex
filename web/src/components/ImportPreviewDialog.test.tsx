import { describe, it, expect, beforeEach, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { ImportPreviewDialog } from './ImportPreviewDialog'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { makeQueryClient } from '../test/renderWithProviders'
import { http } from '../api/client'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

function makeFile() {
  return new File(['<DL></DL>'], 'bookmarks.html', { type: 'text/html' })
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

function importResult() {
  return {
    format: 'netscape' as const,
    mode: 'skip' as const,
    imported: 3,
    skipped: 1,
    wiped: 0,
    warnings: [],
  }
}

function renderDialog(props: Partial<React.ComponentProps<typeof ImportPreviewDialog>> = {}) {
  const client = makeQueryClient()
  return {
    client,
    ...render(
      <QueryClientProvider client={client}>
        <ImportPreviewDialog
          file={makeFile()}
          format="netscape"
          onClose={vi.fn()}
          onApplied={vi.fn()}
          {...props}
        />
      </QueryClientProvider>,
    ),
  }
}

describe('ImportPreviewDialog', () => {
  it('shows counts + duplicates after validate resolves', async () => {
    renderDialog()
    await waitFor(() => expect(screen.getByText(/4 links · 2 folders/)).toBeInTheDocument())
    expect(screen.getByText(/1 links · 0 tags/)).toBeInTheDocument()
  })

  it('lists folders with checkboxes; toggling reflects in the effective count', async () => {
    const user = userEvent.setup()
    renderDialog()
    await waitFor(() => expect(screen.getByText('Work')).toBeInTheDocument())
    const checkboxes = screen.getAllByRole('checkbox')
    expect(checkboxes).toHaveLength(2)
    await user.click(checkboxes[1])  // uncheck "Work"
    await waitFor(() =>
      expect(screen.getByText(/2 links · 1 folders · 1 duplicates/)).toBeInTheDocument(),
    )
  })

  it('submitting passes the picked mode + excluded folders to apply', async () => {
    const onApplied = vi.fn()
    const user = userEvent.setup()
    renderDialog({ onApplied })
    await waitFor(() => expect(screen.getByText('Work')).toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: /Erase duplicates and re-import/i }))
    // Exclude "Work"
    const checkboxes = screen.getAllByRole('checkbox')
    await user.click(checkboxes[1])

    await user.click(screen.getByRole('button', { name: /Import \(replaces duplicates\)/i }))
    await waitFor(() => expect(state.lastImportMode).toBe('wipe'))
    expect(state.lastImportExcluded).toEqual(['Work'])

    // Result block appears with Concluído button.
    await waitFor(() => expect(screen.getByRole('button', { name: /Done/i })).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Done/i }))
    expect(onApplied).toHaveBeenCalledOnce()
  })

  it('"todas" / "nenhuma" buttons reset the folder selection', async () => {
    const user = userEvent.setup()
    renderDialog()
    await waitFor(() => expect(screen.getByText('Work')).toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: /^none$/i }))
    const checkboxes = screen.getAllByRole('checkbox')
    expect(checkboxes.every((c) => !(c as HTMLInputElement).checked)).toBe(true)

    await user.click(screen.getByRole('button', { name: /^all$/i }))
    const checkboxes2 = screen.getAllByRole('checkbox')
    expect(checkboxes2.every((c) => (c as HTMLInputElement).checked)).toBe(true)
  })

  it('Esc closes the dialog', async () => {
    const onClose = vi.fn()
    renderDialog({ onClose })
    await waitFor(() => expect(screen.getByText(/Import mode/i)).toBeInTheDocument())
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
    const props = { format: 'netscape' as const, onClose: vi.fn(), onApplied: vi.fn() }
    const view = render(
      <QueryClientProvider client={client}>
        <ImportPreviewDialog file={makeFile()} {...props} />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(signals).toHaveLength(1))

    const replacement = new File(['replacement'], 'replacement.html', { type: 'text/html' })
    view.rerender(
      <QueryClientProvider client={client}>
        <ImportPreviewDialog file={replacement} {...props} />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(signals).toHaveLength(2))
    expect(signals[0].aborted).toBe(true)
    expect(signals[1].aborted).toBe(false)

    view.unmount()
    expect(signals[1].aborted).toBe(true)
  })

  it('cannot be dismissed or change mode while apply is running or after success', async () => {
    const pending = deferred<{ data: ReturnType<typeof importResult> }>()
    const onClose = vi.fn()
    const onApplied = vi.fn()
    const user = userEvent.setup()
    renderDialog({ onClose, onApplied })
    await waitFor(() => expect(screen.getByText(/Import mode/i)).toBeInTheDocument())
    vi.mocked(http.post).mockImplementation((url) => {
      if (url === '/api/import/apply') return pending.promise
      throw new Error(`unexpected request: ${url}`)
    })

    const importButton = screen.getByRole('button', { name: /Import \d+ links?/i })
    act(() => {
      fireEvent.click(importButton)
      fireEvent.click(importButton)
    })
    await waitFor(() => {
      expect(vi.mocked(http.post).mock.calls.filter(([url]) => url === '/api/import/apply')).toHaveLength(1)
    })
    await waitFor(() => expect(screen.getByText(/cannot be canceled/i)).toBeInTheDocument())
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.getByRole('button', { name: /Cancel/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Close' })).toBeDisabled()
    expect(screen.getByText(/^Duplicate$/i).closest('button')).toBeDisabled()
    expect(screen.getByRole('button', { name: /^none$/i })).toBeDisabled()
    expect(onClose).not.toHaveBeenCalled()

    pending.resolve({ data: importResult() })
    await waitFor(() => expect(screen.getByRole('button', { name: /Done/i })).toBeInTheDocument())
    fireEvent.keyDown(window, { key: 'Escape' })
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose).not.toHaveBeenCalled()
    expect(onApplied).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: /Done/i }))
    expect(onApplied).toHaveBeenCalledOnce()
  })

  it('stays mounted through apply failure, then allows close', async () => {
    const pending = deferred<never>()
    const onClose = vi.fn()
    const user = userEvent.setup()
    renderDialog({ onClose })
    await waitFor(() => expect(screen.getByText(/Import mode/i)).toBeInTheDocument())
    vi.mocked(http.post).mockImplementation((url) => {
      if (url === '/api/import/apply') return pending.promise
      throw new Error(`unexpected request: ${url}`)
    })

    await user.click(screen.getByRole('button', { name: /Import \d+ links?/i }))
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).not.toHaveBeenCalled()
    pending.reject(new Error('import failed'))

    await waitFor(() => expect(screen.getByText(/import failed/i)).toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /Cancel/i }))
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('aborts an owned apply upload when the dialog unmounts', async () => {
    let signal: AbortSignal | undefined
    const user = userEvent.setup()
    const view = renderDialog()
    await waitFor(() => expect(screen.getByText(/Import mode/i)).toBeInTheDocument())
    vi.mocked(http.post).mockImplementation((_url, _data, config) => {
      signal = config?.signal as AbortSignal | undefined
      return new Promise(() => {})
    })

    await user.click(screen.getByRole('button', { name: /Import \d+ links?/i }))
    await waitFor(() => expect(signal).toBeDefined())
    view.unmount()

    expect(signal?.aborted).toBe(true)
  })
})
