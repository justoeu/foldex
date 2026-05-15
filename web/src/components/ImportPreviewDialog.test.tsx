import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ImportPreviewDialog } from './ImportPreviewDialog'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

function makeFile() {
  return new File(['<DL></DL>'], 'bookmarks.html', { type: 'text/html' })
}

describe('ImportPreviewDialog', () => {
  it('shows counts + duplicates after validate resolves', async () => {
    render(
      <ImportPreviewDialog
        file={makeFile()}
        format="netscape"
        onClose={vi.fn()}
        onApplied={vi.fn()}
      />,
    )
    await waitFor(() => expect(screen.getByText(/4 links · 2 folders/)).toBeInTheDocument())
    expect(screen.getByText(/1 links · 0 tags/)).toBeInTheDocument()
  })

  it('lists folders with checkboxes; toggling reflects in the effective count', async () => {
    const user = userEvent.setup()
    render(
      <ImportPreviewDialog
        file={makeFile()}
        format="netscape"
        onClose={vi.fn()}
        onApplied={vi.fn()}
      />,
    )
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
    render(
      <ImportPreviewDialog
        file={makeFile()}
        format="netscape"
        onClose={vi.fn()}
        onApplied={onApplied}
      />,
    )
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
    render(
      <ImportPreviewDialog
        file={makeFile()}
        format="netscape"
        onClose={vi.fn()}
        onApplied={vi.fn()}
      />,
    )
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
    render(
      <ImportPreviewDialog
        file={makeFile()}
        format="netscape"
        onClose={onClose}
        onApplied={vi.fn()}
      />,
    )
    await waitFor(() => expect(screen.getByText(/Import mode/i)).toBeInTheDocument())
    await userEvent.setup().keyboard('{Escape}')
    expect(onClose).toHaveBeenCalledOnce()
  })
})
