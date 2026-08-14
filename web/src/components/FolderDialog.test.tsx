import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { FolderDialog } from './FolderDialog'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { http } from '../api/client'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

describe('FolderDialog', () => {
  it('does not render content when closed', () => {
    renderWithProviders(<FolderDialog open={false} onClose={vi.fn()} />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('submits a new folder and calls onClose', async () => {
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/folder.*name|nome/i), 'My Folder')
    await user.click(screen.getByRole('button', { name: /create|create folder|criar/i }))
    expect(state.folders).toHaveLength(1)
    expect(state.folders[0]?.name).toBe('My Folder')
    expect(onClose).toHaveBeenCalled()
  })

  it('disables submit when name is empty', () => {
    renderWithProviders(<FolderDialog open onClose={vi.fn()} />)
    const submit = screen.getByRole('button', { name: /create|create folder|criar/i })
    expect(submit).toBeDisabled()
  })

  it('cancel calls onClose', async () => {
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} />)
    await userEvent.setup().click(screen.getByRole('button', { name: /cancel/i }))
    expect(onClose).toHaveBeenCalled()
  })

  it('surfaces an unexpected create failure and keeps the dialog open', async () => {
    vi.mocked(http.post).mockRejectedValueOnce({
      response: { status: 500, data: { error: { code: 'server_error' } } },
    })
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/folder.*name|nome/i), 'Unsaved')
    await user.click(screen.getByRole('button', { name: /create folder/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/couldn't save/i)
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('pre-fills name when editing an existing folder', () => {
    const folder = { id: 1, name: 'Existing', color: '#6366F1', parent_id: null, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: false }
    renderWithProviders(<FolderDialog open onClose={vi.fn()} folder={folder} />)
    expect(screen.getByDisplayValue('Existing')).toBeInTheDocument()
  })

  it('shows delete buttons when editing (not justCreated)', () => {
    const folder = { id: 1, name: 'Existing', color: '#6366F1', parent_id: null, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: false }
    renderWithProviders(<FolderDialog open onClose={vi.fn()} folder={folder} />)
    // Two delete buttons exist ("Delete folder" + "Delete everything")
    const buttons = screen.getAllByText(/delete|remover|apagar/i)
    expect(buttons.length).toBeGreaterThanOrEqual(1)
  })

  it('hides delete buttons when justCreated is true', () => {
    const folder = { id: 1, name: 'New', color: '#6366F1', parent_id: null, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: false }
    renderWithProviders(<FolderDialog open onClose={vi.fn()} folder={folder} justCreated />)
    expect(screen.queryByText(/delete|remover|apagar/i)).not.toBeInTheDocument()
  })

  it('sets a password when creating a new folder', async () => {
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/folder.*name|nome/i), 'Secret')
    await user.type(screen.getByLabelText('Password'), 'hunter22')
    await user.click(screen.getByRole('button', { name: /create|create folder|criar/i }))
    expect(state.folders).toHaveLength(1)
    expect(state.folders[0]?.has_password).toBe(true)
    expect(state.folderPasswords[state.folders[0]!.id]).toBe('hunter22')
    expect(onClose).toHaveBeenCalled()
  })

  it('sets a password for the first time when editing an unprotected folder (no current password needed)', async () => {
    const folder = { id: 1, name: 'Open', color: '#6366F1', parent_id: null, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: false }
    state.folders.push(folder)
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} folder={folder} />)
    // The tri-state password field only shows for unprotected folders when
    // NOT editing an already-protected one — same label as create mode.
    await userEvent.setup().type(screen.getByLabelText('Password'), 'newpass1')
    await userEvent.setup().click(screen.getByRole('button', { name: /save|salvar/i }))
    expect(state.folderPasswords[1]).toBe('newpass1')
    expect(onClose).toHaveBeenCalled()
  })

  it('requires the current password to change an existing password, and shows an inline error on mismatch', async () => {
    const folder = { id: 1, name: 'Secret', color: '#6366F1', parent_id: null, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: true }
    state.folders.push(folder)
    state.folderPasswords[1] = 'oldpass1'
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} folder={folder} />)
    const user = userEvent.setup()
    expect(screen.getByText(/password protected/i)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /change password/i }))
    await user.type(screen.getByLabelText('Current password'), 'wrong-current')
    await user.type(screen.getByLabelText('New password'), 'newpass1')
    await user.click(screen.getByRole('button', { name: /save|salvar/i }))
    expect(await screen.findByText(/incorrect/i)).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
    expect(state.folderPasswords[1]).toBe('oldpass1')
  })

  it('changes an existing password when the current password is correct', async () => {
    const folder = { id: 1, name: 'Secret', color: '#6366F1', parent_id: null, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: true }
    state.folders.push(folder)
    state.folderPasswords[1] = 'oldpass1'
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} folder={folder} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /change password/i }))
    await user.type(screen.getByLabelText('Current password'), 'oldpass1')
    await user.type(screen.getByLabelText('New password'), 'newpass1')
    await user.click(screen.getByRole('button', { name: /save|salvar/i }))
    expect(onClose).toHaveBeenCalled()
    expect(state.folderPasswords[1]).toBe('newpass1')
  })

  it('removes password protection with the correct current password', async () => {
    const folder = { id: 1, name: 'Secret', color: '#6366F1', parent_id: null, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: true }
    state.folders.push(folder)
    state.folderPasswords[1] = 'oldpass1'
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} folder={folder} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /change password/i }))
    await user.click(screen.getByLabelText(/remove password protection/i))
    await user.type(screen.getByLabelText('Current password'), 'oldpass1')
    await user.click(screen.getByRole('button', { name: /save|salvar/i }))
    expect(onClose).toHaveBeenCalled()
    expect(state.folderPasswords[1]).toBeUndefined()
    expect(state.folders[0]?.has_password).toBe(false)
  })

  it('resets the change-password sub-flow when the dialog is reopened after a cancel', async () => {
    const folder = { id: 1, name: 'Secret', color: '#6366F1', parent_id: null, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: true }
    state.folders.push(folder)
    state.folderPasswords[1] = 'oldpass1'
    const onClose = vi.fn()
    const { rerender } = renderWithProviders(<FolderDialog open onClose={onClose} folder={folder} />)
    const user = userEvent.setup()
    // Enter the change-password sub-flow, type something, then close
    // WITHOUT saving (cancel — no submit).
    await user.click(screen.getByRole('button', { name: /change password/i }))
    await user.click(screen.getByLabelText(/remove password protection/i))
    await user.type(screen.getByLabelText('Current password'), 'typed-but-not-submitted')
    rerender(<FolderDialog open={false} onClose={onClose} folder={folder} />)
    // Reopen on the SAME folder — the reset effect (deps [open, folder])
    // must clear passwordEditing/currentPassword/removePassword, or a
    // canceled attempt's stale current-password/remove-checkbox state would
    // leak into the next submit.
    rerender(<FolderDialog open onClose={onClose} folder={folder} />)
    expect(screen.getByText(/password protected/i)).toBeInTheDocument()
    expect(screen.queryByLabelText('Current password')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /change password/i }))
    expect(screen.getByLabelText('Current password')).toHaveValue('')
    expect(screen.queryByLabelText(/remove password protection/i)).not.toBeChecked()
  })

  it('does not leak password-flow state when reopened on a different folder', async () => {
    const protectedFolder = { id: 1, name: 'Secret', color: '#6366F1', parent_id: null, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: true }
    const otherFolder = { id: 2, name: 'Public', color: '#6366F1', parent_id: null, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: false }
    state.folders.push(protectedFolder, otherFolder)
    state.folderPasswords[1] = 'oldpass1'
    const onClose = vi.fn()
    const { rerender } = renderWithProviders(<FolderDialog open onClose={onClose} folder={protectedFolder} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /change password/i }))
    await user.type(screen.getByLabelText('Current password'), 'some-value')
    rerender(<FolderDialog open={false} onClose={onClose} folder={protectedFolder} />)
    // Reopen on a DIFFERENT, unprotected folder — must show the plain
    // create/first-time-set field, not the protected folder's leftover
    // change-password sub-flow.
    rerender(<FolderDialog open onClose={onClose} folder={otherFolder} />)
    expect(screen.queryByText(/password protected/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /change password/i })).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Current password')).not.toBeInTheDocument()
    expect(screen.getByLabelText('Password')).toHaveValue('')
  })
})

describe('FolderDialog — password hint (ADR-29)', () => {
  it('sends password_hint when creating with a password + hint', async () => {
    renderWithProviders(<FolderDialog open onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/folder.*name|nome/i), 'Secret')
    await user.type(screen.getByLabelText('Password'), 'folder-pass')
    // Hint field appears only once a password is entered.
    await user.type(screen.getByLabelText('Reminder hint'), 'rhymes with force')
    await user.click(screen.getByRole('button', { name: /create folder/i }))
    expect(state.folders).toHaveLength(1)
    expect(state.folders[0]?.password_hint).toBe('rhymes with force')
  })

  it('blocks a hint equal to the password client-side', async () => {
    renderWithProviders(<FolderDialog open onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/folder.*name|nome/i), 'Secret')
    await user.type(screen.getByLabelText('Password'), 'samevalue')
    await user.type(screen.getByLabelText('Reminder hint'), 'samevalue')
    await user.click(screen.getByRole('button', { name: /create folder/i }))
    expect(await screen.findByText(/must not be the same as the password/i)).toBeInTheDocument()
    expect(state.folders).toHaveLength(0)
  })

  it('edits the hint of an already-protected folder', async () => {
    const folder = {
      id: 2,
      name: 'Locked',
      color: '#6366F1',
      parent_id: null,
      has_password: true,
      password_hint: 'old clue',
      link_count: 0,
      folder_count: 0,
      preview_links: [],
      preview_folders: [],
    }
    state.folders.push({ ...folder })
    state.folderPasswords[2] = 'folder-pass'
    renderWithProviders(<FolderDialog open onClose={vi.fn()} folder={folder} />)
    const user = userEvent.setup()
    const hintInput = screen.getByLabelText('Reminder hint')
    expect(hintInput).toHaveValue('old clue')
    await user.clear(hintInput)
    await user.type(hintInput, 'new clue')
    await user.click(screen.getByRole('button', { name: /^save$/i }))
    expect(state.folders[0]?.password_hint).toBe('new clue')
  })
})

describe('FolderDialog — delete + color + parent', () => {
  it('deletes folder keeping links after confirm', async () => {
    const folder = {
      id: 10, name: 'Gone', color: '#6366F1', parent_id: null, link_count: 2,
      folder_count: 0, preview_links: [], preview_folders: [], has_password: false,
    }
    state.folders.push(folder)
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} folder={folder} />)
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/delete folder, keep links/i))
    await user.click(await screen.findByRole('button', { name: /^Delete folder$/i }))
    await waitFor(() => expect(state.folders.find((f) => f.id === 10)).toBeUndefined())
    expect(onClose).toHaveBeenCalled()
  })

  it('cascade-deletes folder after confirm', async () => {
    const folder = {
      id: 11, name: 'Nuke', color: '#6366F1', parent_id: null, link_count: 0,
      folder_count: 0, preview_links: [], preview_folders: [], has_password: false,
    }
    state.folders.push(folder)
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} folder={folder} />)
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/delete folder and links/i))
    await user.click(await screen.findByRole('button', { name: /^Delete everything$/i }))
    await waitFor(() => expect(state.folders.find((f) => f.id === 11)).toBeUndefined())
    expect(onClose).toHaveBeenCalled()
  })

  it('keeps the subtree and shows the protected-descendant count when cascade is refused', async () => {
    const root = {
      id: 20, name: 'Root', color: '#6366F1', parent_id: null, link_count: 0,
      folder_count: 1, preview_links: [], preview_folders: [], has_password: false,
    }
    const child = {
      id: 21, name: 'Vault', color: '#6366F1', parent_id: 20, link_count: 0,
      folder_count: 0, preview_links: [], preview_folders: [], has_password: true,
    }
    state.folders.push(root, child)
    state.folderPasswords[21] = 'child-secret'
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} folder={root} />)
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/delete folder and links/i))
    await user.click(await screen.findByRole('button', { name: /^Delete everything$/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/1 protected subfolder/i)
    expect(state.folders.map((f) => f.id)).toEqual([20, 21])
    expect(onClose).not.toHaveBeenCalled()
  })

  it('shows a handled error when the retried protected-folder delete is still locked', async () => {
    const folder = {
      id: 22, name: 'Vault', color: '#6366F1', parent_id: null, link_count: 0,
      folder_count: 0, preview_links: [], preview_folders: [], has_password: true,
    }
    state.folders.push(folder)
    state.folderPasswords[22] = 'vault-secret'
    vi.spyOn(http, 'delete').mockRejectedValue({
      response: { status: 403, data: { error: { code: 'folder_locked' } } },
    })
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} folder={folder} unlockToken="stale-token" />)
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/delete folder, keep links/i))
    await user.click(await screen.findByRole('button', { name: /^Delete folder$/i }))
    await user.type(await screen.findByLabelText('folder password'), 'vault-secret')
    await user.click(screen.getByRole('button', { name: /unlock/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/folder is locked/i)
    expect(state.folders.some((f) => f.id === 22)).toBe(true)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('unlocks a protected folder and retries deletion with the fresh token', async () => {
    const folder = {
      id: 23, name: 'Vault', color: '#6366F1', parent_id: null, link_count: 0,
      folder_count: 0, preview_links: [], preview_folders: [], has_password: true,
    }
    state.folders.push(folder)
    state.folderPasswords[23] = 'vault-secret'
    const onClose = vi.fn()
    const onUnlocked = vi.fn()
    renderWithProviders(
      <FolderDialog open onClose={onClose} folder={folder} unlockToken="stale-token" onUnlocked={onUnlocked} />,
    )
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/delete folder, keep links/i))
    await user.click(await screen.findByRole('button', { name: /^Delete folder$/i }))
    await user.type(await screen.findByLabelText('folder password'), 'vault-secret')
    await user.click(screen.getByRole('button', { name: /unlock/i }))

    await waitFor(() => expect(state.folders.some((item) => item.id === 23)).toBe(false))
    expect(onUnlocked).toHaveBeenCalledWith(expect.objectContaining({ token: 'mock-unlock:23:vault-secret' }))
    expect(onClose).toHaveBeenCalledOnce()
    expect(vi.mocked(http.delete).mock.calls[1]?.[1]).toEqual(expect.objectContaining({
      headers: { 'X-Foldex-Folder-Unlock': 'mock-unlock:23:vault-secret' },
    }))
  })

  it('leaves a protected folder untouched when the unlock retry is canceled', async () => {
    const folder = {
      id: 24, name: 'Vault', color: '#6366F1', parent_id: null, link_count: 0,
      folder_count: 0, preview_links: [], preview_folders: [], has_password: true,
    }
    state.folders.push(folder)
    state.folderPasswords[24] = 'vault-secret'
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} folder={folder} />)
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/delete folder, keep links/i))
    await user.click(await screen.findByRole('button', { name: /^Delete folder$/i }))
    const unlockDialog = await screen.findByRole('dialog', { name: /enter password for Vault/i })
    await user.click(within(unlockDialog).getByRole('button', { name: /cancel/i }))

    expect(state.folders.some((item) => item.id === 24)).toBe(true)
    expect(onClose).not.toHaveBeenCalled()
    expect(vi.mocked(http.delete)).toHaveBeenCalledOnce()
  })

  it('cancels delete when confirm is dismissed', async () => {
    const folder = {
      id: 12, name: 'Stay', color: '#6366F1', parent_id: null, link_count: 0,
      folder_count: 0, preview_links: [], preview_folders: [], has_password: false,
    }
    state.folders.push(folder)
    renderWithProviders(<FolderDialog open onClose={vi.fn()} folder={folder} />)
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/delete folder, keep links/i))
    const cancels = screen.getAllByRole('button', { name: /cancel/i })
    await user.click(cancels[cancels.length - 1])
    expect(state.folders.find((f) => f.id === 12)).toBeDefined()
  })

  it('shows naming kicker when justCreated and hides password field', () => {
    const folder = {
      id: 13, name: 'Nova pasta', color: '#6366F1', parent_id: null, link_count: 0,
      folder_count: 0, preview_links: [], preview_folders: [], has_password: false,
    }
    renderWithProviders(<FolderDialog open onClose={vi.fn()} folder={folder} justCreated />)
    expect(screen.queryByLabelText('Password')).not.toBeInTheDocument()
    expect(screen.getByDisplayValue('Nova pasta')).toBeInTheDocument()
  })

  it('switches to gradient color mode and saves', async () => {
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/folder.*name|nome/i), 'Grad')
    await user.click(screen.getByRole('tab', { name: /gradient/i }))
    await user.click(screen.getByRole('button', { name: /create|create folder|criar/i }))
    await waitFor(() => expect(state.folders).toHaveLength(1))
    expect(state.folders[0]?.color).toMatch(/^linear-gradient/)
    expect(onClose).toHaveBeenCalled()
  })

  it('closes via X button', async () => {
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} />)
    await userEvent.setup().click(screen.getByRole('button', { name: /close/i }))
    expect(onClose).toHaveBeenCalled()
  })

  it('edits name of existing folder', async () => {
    const folder = {
      id: 14, name: 'Old', color: '#6366F1', parent_id: null, link_count: 0,
      folder_count: 0, preview_links: [], preview_folders: [], has_password: false,
    }
    state.folders.push(folder)
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} folder={folder} />)
    const user = userEvent.setup()
    const name = screen.getByDisplayValue('Old')
    await user.clear(name)
    await user.type(name, 'Renamed')
    await user.click(screen.getByRole('button', { name: /save|salvar/i }))
    await waitFor(() => expect(state.folders[0]?.name).toBe('Renamed'))
    expect(onClose).toHaveBeenCalled()
  })

  it('loads gradient folder color into gradient mode', () => {
    const folder = {
      id: 15,
      name: 'Pretty',
      color: 'linear-gradient(135deg, #6366F1, #EC4899)',
      parent_id: null,
      link_count: 0,
      folder_count: 0,
      preview_links: [],
      preview_folders: [],
      has_password: false,
    }
    renderWithProviders(<FolderDialog open onClose={vi.fn()} folder={folder} />)
    expect(screen.getByDisplayValue('Pretty')).toBeInTheDocument()
  })

  it('creates with parentId when provided', async () => {
    state.folders.push({
      id: 1, name: 'Parent', color: '#6366F1', parent_id: null, link_count: 0,
      folder_count: 0, preview_links: [], preview_folders: [], has_password: false, created_at: '',
    })
    const onClose = vi.fn()
    renderWithProviders(<FolderDialog open onClose={onClose} parentId={1} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/folder.*name|nome/i), 'Child')
    await user.click(screen.getByRole('button', { name: /create|create folder|criar/i }))
    await waitFor(() => expect(state.folders.some((f) => f.name === 'Child' && f.parent_id === 1)).toBe(true))
  })
})
