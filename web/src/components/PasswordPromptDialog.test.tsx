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

  // Types the wrong password and clicks unlock, `times` in a row.
  async function failUnlock(user: ReturnType<typeof userEvent.setup>, times: number) {
    for (let i = 0; i < times; i++) {
      const input = screen.getByLabelText('folder password')
      await user.clear(input)
      await user.type(input, 'wrong-password')
      await user.click(screen.getByRole('button', { name: /unlock/i }))
      // wait for the field to clear (submit resolved) before the next attempt
      await waitFor(() => expect(screen.getByLabelText('folder password')).toHaveValue(''))
    }
  }

  it('shows an inline error with attempts remaining and stays open on wrong password', async () => {
    state.folders.push(protectedFolder)
    state.folderPasswords[1] = 'correct'
    const onResolved = vi.fn()
    renderWithProviders(<TriggerFlow folder={protectedFolder} onResolved={onResolved} />)
    const user = userEvent.setup()
    await user.click(screen.getByTestId('trigger'))
    await failUnlock(user, 1)
    await waitFor(() => expect(screen.getByText(/attempts left before lockout/i)).toBeInTheDocument())
    expect(onResolved).not.toHaveBeenCalled()
    expect(screen.getByLabelText('folder password')).toBeInTheDocument()
  })

  it('reveals the hint only after 3 failed attempts (ADR-28)', async () => {
    const withHint: Folder = { ...protectedFolder, id: 3, password_hint: 'my first pet' }
    state.folders.push(withHint)
    state.folderPasswords[3] = 'correct'
    renderWithProviders(<TriggerFlow folder={withHint} onResolved={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByTestId('trigger'))
    // Hidden before the 3rd failure.
    await failUnlock(user, 2)
    expect(screen.queryByText(/my first pet/i)).not.toBeInTheDocument()
    // Revealed on/after the 3rd.
    await failUnlock(user, 1)
    await waitFor(() => expect(screen.getByText(/my first pet/i)).toBeInTheDocument())
  })

  it('locks the folder after 5 wrong attempts and disables unlock', async () => {
    const f: Folder = { ...protectedFolder, id: 4 }
    state.folders.push(f)
    state.folderPasswords[4] = 'correct'
    renderWithProviders(<TriggerFlow folder={f} onResolved={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByTestId('trigger'))
    await failUnlock(user, 4)
    // 5th attempt trips the lockout.
    const input = screen.getByLabelText('folder password')
    await user.clear(input)
    await user.type(input, 'wrong-password')
    await user.click(screen.getByRole('button', { name: /unlock/i }))
    await waitFor(() => expect(screen.getByText(/too many attempts/i)).toBeInTheDocument())
    // Input is gone (replaced by the lockout banner) and unlock is disabled.
    expect(screen.queryByLabelText('folder password')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /unlock/i })).toBeDisabled()
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

  it('omits the hint line when the folder has none', async () => {
    state.folders.push(protectedFolder)
    state.folderPasswords[1] = 'correct'
    renderWithProviders(<TriggerFlow folder={protectedFolder} onResolved={vi.fn()} />)
    await userEvent.setup().click(screen.getByTestId('trigger'))
    expect(screen.queryByText(/hint:/i)).not.toBeInTheDocument()
  })
})
