import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SettingsPage } from './SettingsPage'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

function lockedFolder(id: number, name: string) {
  state.folders.push({
    id,
    name,
    color: '#6366F1',
    parent_id: null,
    has_password: true,
    password_hint: 'a clue',
    link_count: 0,
    folder_count: 0,
    preview_links: [],
    preview_folders: [],
  })
  state.folderPasswords[id] = 'folder-pass'
}

describe('SettingsPage — master password', () => {
  it('shows the unconfigured state and sets a first master password + hint', async () => {
    renderWithProviders(<SettingsPage />)
    await waitFor(() => expect(screen.getByText(/no master password configured/i)).toBeInTheDocument())

    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/^master password$/i), 'super-secret-master')
    await user.type(screen.getByLabelText(/confirm password/i), 'super-secret-master')
    await user.type(screen.getByLabelText(/reminder \(word or phrase\)/i), 'my old street')
    await user.click(screen.getByRole('button', { name: /set master password/i }))

    await waitFor(() => expect(state.masterPassword).toBe('super-secret-master'))
    expect(state.masterHint).toBe('my old street')
    // After save the form clears — including the reminder field — and the saved
    // hint is surfaced read-only instead.
    await waitFor(() => expect(screen.getByLabelText(/reminder \(word or phrase\)/i)).toHaveValue(''))
    expect(await screen.findByText(/current reminder: my old street/i)).toBeInTheDocument()
  })

  it('keeps the existing hint when changing the password with an empty reminder', async () => {
    state.masterPassword = 'original-master'
    state.masterHint = 'keep me'
    renderWithProviders(<SettingsPage />)
    await waitFor(() => expect(screen.getByText(/a master password is configured/i)).toBeInTheDocument())

    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/current master password/i), 'original-master')
    await user.type(screen.getByLabelText(/new master password/i), 'brand-new-master')
    await user.type(screen.getByLabelText(/confirm password/i), 'brand-new-master')
    // Reminder field left empty on purpose.
    await user.click(screen.getByRole('button', { name: /^change$/i }))

    await waitFor(() => expect(state.masterPassword).toBe('brand-new-master'))
    expect(state.masterHint).toBe('keep me')
  })

  it('shows a strength meter as the password is typed', async () => {
    renderWithProviders(<SettingsPage />)
    await waitFor(() => expect(screen.getByText(/no master password configured/i)).toBeInTheDocument())
    await userEvent.setup().type(screen.getByLabelText(/^master password$/i), 'S0me-Very-Long-Pass!')
    expect(screen.getByText(/^strong$/i)).toBeInTheDocument()
  })

  it('blocks save when confirmation does not match', async () => {
    renderWithProviders(<SettingsPage />)
    await waitFor(() => expect(screen.getByText(/no master password configured/i)).toBeInTheDocument())
    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/^master password$/i), 'super-secret-master')
    await user.type(screen.getByLabelText(/confirm password/i), 'different-value')
    expect(screen.getByRole('button', { name: /set master password/i })).toBeDisabled()
    expect(screen.getByText(/don't match|não coincidem|no coinciden/i)).toBeInTheDocument()
  })

  it('changes an existing master password with the correct current one', async () => {
    state.masterPassword = 'original-master'
    renderWithProviders(<SettingsPage />)
    await waitFor(() => expect(screen.getByText(/a master password is configured/i)).toBeInTheDocument())

    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/current master password/i), 'original-master')
    await user.type(screen.getByLabelText(/new master password/i), 'brand-new-master')
    await user.type(screen.getByLabelText(/confirm password/i), 'brand-new-master')
    await user.click(screen.getByRole('button', { name: /^change$/i }))

    await waitFor(() => expect(state.masterPassword).toBe('brand-new-master'))
  })

  it('removes an existing master password', async () => {
    state.masterPassword = 'original-master'
    renderWithProviders(<SettingsPage />)
    await waitFor(() => expect(screen.getByText(/a master password is configured/i)).toBeInTheDocument())

    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/current master password/i), 'original-master')
    await user.click(screen.getByRole('button', { name: /remove/i }))

    await waitFor(() => expect(state.masterPassword).toBeUndefined())
    expect(await screen.findByText(/master password removed/i)).toBeInTheDocument()
  })

  it('rejects a too-short master password client-side', async () => {
    renderWithProviders(<SettingsPage />)
    await waitFor(() => expect(screen.getByText(/no master password configured/i)).toBeInTheDocument())

    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/^master password$/i), 'short')
    await user.type(screen.getByLabelText(/confirm password/i), 'short')
    await user.click(screen.getByRole('button', { name: /set master password/i }))

    expect(await screen.findByText(/must be at least 8 characters/i)).toBeInTheDocument()
    expect(state.masterPassword).toBeUndefined()
  })
})

describe('SettingsPage — locked folders reset', () => {
  it('lists locked folders and resets one with the master password', async () => {
    state.masterPassword = 'master-pass'
    lockedFolder(7, 'Vault')
    renderWithProviders(<SettingsPage />)

    await waitFor(() => expect(screen.getByText('Vault')).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /reset password/i }))
    await user.type(screen.getByLabelText('Master password'), 'master-pass')
    await user.click(screen.getByRole('button', { name: /^reset$/i }))

    await waitFor(() => expect(state.folderPasswords[7]).toBeUndefined())
    expect(screen.getByText(/password cleared/i)).toBeInTheDocument()
  })

  it('shows the master hint on the reset prompt when set', async () => {
    state.masterPassword = 'master-pass'
    state.masterHint = 'starts with master'
    lockedFolder(11, 'VaultHint')
    renderWithProviders(<SettingsPage />)
    await waitFor(() => expect(screen.getByText('VaultHint')).toBeInTheDocument())
    await userEvent.setup().click(screen.getByRole('button', { name: /reset password/i }))
    // Exact string avoids colliding with the master section's "Current
    // reminder: …" read-only line, which also contains the hint.
    expect(await screen.findByText('Reminder: starts with master')).toBeInTheDocument()
  })

  it('shows an error when the master password is wrong', async () => {
    state.masterPassword = 'master-pass'
    lockedFolder(8, 'Vault8')
    renderWithProviders(<SettingsPage />)

    await waitFor(() => expect(screen.getByText('Vault8')).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /reset password/i }))
    await user.type(screen.getByLabelText('Master password'), 'wrong-one')
    await user.click(screen.getByRole('button', { name: /^reset$/i }))

    expect(await screen.findByText(/incorrect master password/i)).toBeInTheDocument()
    expect(state.folderPasswords[8]).toBe('folder-pass')
  })

  it('shows empty state when no folders are locked', async () => {
    renderWithProviders(<SettingsPage />)
    await waitFor(() => expect(screen.getByText(/no password-protected folders/i)).toBeInTheDocument())
  })

  it('calls onEditFolder after a successful reset', async () => {
    state.masterPassword = 'master-pass'
    lockedFolder(9, 'Vault9')
    const onEditFolder = vi.fn()
    renderWithProviders(<SettingsPage onEditFolder={onEditFolder} />)

    await waitFor(() => expect(screen.getByText('Vault9')).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /reset password/i }))
    await user.type(screen.getByLabelText('Master password'), 'master-pass')
    await user.click(screen.getByRole('button', { name: /^reset$/i }))

    const setNew = await screen.findByRole('button', { name: /set new password/i })
    await user.click(setNew)
    expect(onEditFolder).toHaveBeenCalledWith(9)
  })
})
