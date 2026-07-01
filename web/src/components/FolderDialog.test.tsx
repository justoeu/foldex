import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { FolderDialog } from './FolderDialog'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'

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
