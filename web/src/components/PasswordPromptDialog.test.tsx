import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { usePasswordPrompt, type FolderUnlock } from './PasswordPromptDialog'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import type { Folder } from '../api/types'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

const protectedFolder: Folder = {
  id: 1, name: 'Secret', color: '#6366F1', parent_id: null, has_password: true,
  link_count: 0, folder_count: 0, preview_links: [], preview_folders: [],
}

function TriggerFlow({ folder, onResolved }: { folder: Folder; onResolved: (r: FolderUnlock | null) => void }) {
  const promptPassword = usePasswordPrompt()
  return (
    <button
      data-testid="trigger"
      onClick={async () => {
        const result = await promptPassword(folder)
        onResolved(result)
      }}
    >
      trigger
    </button>
  )
}

describe('PasswordPromptDialog', () => {
  it('opens with the folder name in the title', async () => {
    state.folders.push(protectedFolder)
    state.folderPasswords[1] = 'correct'
    renderWithProviders(<TriggerFlow folder={protectedFolder} onResolved={vi.fn()} />)
    await userEvent.setup().click(screen.getByTestId('trigger'))
    expect(screen.getByText(/Secret/)).toBeInTheDocument()
  })

  it('shows an inline error and stays open on wrong password', async () => {
    state.folders.push(protectedFolder)
    state.folderPasswords[1] = 'correct'
    const onResolved = vi.fn()
    renderWithProviders(<TriggerFlow folder={protectedFolder} onResolved={onResolved} />)
    const user = userEvent.setup()
    await user.click(screen.getByTestId('trigger'))
    await user.type(screen.getByLabelText('folder password'), 'wrong-password')
    await user.click(screen.getByRole('button', { name: /unlock/i }))
    await waitFor(() => expect(screen.getByText(/incorrect password/i)).toBeInTheDocument())
    expect(onResolved).not.toHaveBeenCalled()
    // The dialog must still be open for a retry.
    expect(screen.getByLabelText('folder password')).toBeInTheDocument()
  })

  it('resolves with a token on correct password and closes', async () => {
    state.folders.push(protectedFolder)
    state.folderPasswords[1] = 'correct'
    const onResolved = vi.fn()
    renderWithProviders(<TriggerFlow folder={protectedFolder} onResolved={onResolved} />)
    const user = userEvent.setup()
    await user.click(screen.getByTestId('trigger'))
    await user.type(screen.getByLabelText('folder password'), 'correct')
    await user.click(screen.getByRole('button', { name: /unlock/i }))
    await waitFor(() => expect(onResolved).toHaveBeenCalled())
    const result = onResolved.mock.calls[0][0] as FolderUnlock
    expect(result).not.toBeNull()
    expect(result.token).toContain('1')
    expect(screen.queryByLabelText('folder password')).not.toBeInTheDocument()
  })

  it('resolves null on cancel', async () => {
    state.folders.push(protectedFolder)
    state.folderPasswords[1] = 'correct'
    const onResolved = vi.fn()
    renderWithProviders(<TriggerFlow folder={protectedFolder} onResolved={onResolved} />)
    const user = userEvent.setup()
    await user.click(screen.getByTestId('trigger'))
    await user.click(screen.getByRole('button', { name: /cancel/i }))
    expect(onResolved).toHaveBeenCalledWith(null)
  })

  it('closes on Escape and resolves null', async () => {
    state.folders.push(protectedFolder)
    state.folderPasswords[1] = 'correct'
    const onResolved = vi.fn()
    renderWithProviders(<TriggerFlow folder={protectedFolder} onResolved={onResolved} />)
    const user = userEvent.setup()
    await user.click(screen.getByTestId('trigger'))
    await user.keyboard('{Escape}')
    expect(onResolved).toHaveBeenCalledWith(null)
  })
})
