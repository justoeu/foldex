import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SettingsPage, resolveHubView } from './SettingsPage'
import { renderWithProviders, testAdminSession } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { http } from '../api/client'
import type { AuthUser } from '../auth/types'

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

const userSession = {
  ...testAdminSession,
  user: {
    ...(testAdminSession as { user: object }).user,
    role: 'user',
  },
} as typeof testAdminSession

const adminRow: AuthUser = {
  email: 'admin@foldex.test',
  name: 'Test Admin',
  id: 1,
  role: 'admin',
  status: 'active',
  has_password: true,
  totp_enabled: false,
  created_at: '2026-01-01T00:00:00Z',
}

/** Renders the hub and clicks into a tile, so section tests start where the
 *  user now does: one click deep inside the consolidated settings hub. */
async function renderAtSection(section: 'master' | 'locked', onEditFolder?: (folderId: number) => void) {
  renderWithProviders(<SettingsPage onEditFolder={onEditFolder} />)
  const tile = section === 'master' ? /^master password/i : /^locked folders/i
  await userEvent.setup().click(await screen.findByRole('button', { name: tile }))
}

describe('SettingsPage — hub', () => {
  it('shows the personal tiles for a normal user and hides the administration scope (RBAC)', async () => {
    renderWithProviders(<SettingsPage />, { session: userSession })
    expect(await screen.findByRole('button', { name: /^account/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /security & 2fa/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /api tokens/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^master password/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^locked folders/i })).toBeInTheDocument()
    // No scope segment and no admin tile: /api/admin 404s for this session,
    // so the hub must not promise a surface the server denies.
    expect(screen.queryByRole('button', { name: /^administration$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /users & invitations/i })).not.toBeInTheDocument()
  })

  it('shows the RBAC segment for an admin and switches to the administration scope', async () => {
    renderWithProviders(<SettingsPage />)
    expect(await screen.findByRole('button', { name: /^administration$/i })).toBeInTheDocument()
    await userEvent.setup().click(screen.getByRole('button', { name: /^administration$/i }))
    expect(await screen.findByRole('button', { name: /users & invitations/i })).toBeInTheDocument()
    // Personal tiles are replaced, not stacked, in the admin scope.
    expect(screen.queryByRole('button', { name: /api tokens/i })).not.toBeInTheDocument()
  })

  it('opens the admin section from the tile and the back button returns to the hub', async () => {
    const get = vi.spyOn(http, 'get').mockImplementation(async (url: string) => {
      if (url === '/api/admin/users') return { data: { users: [adminRow] } } as never
      return { data: { invites: [] } } as never
    })
    renderWithProviders(<SettingsPage />)
    await userEvent.setup().click(screen.getByRole('button', { name: /^administration$/i }))
    await userEvent.setup().click(screen.getByRole('button', { name: /users & invitations/i }))
    expect(await screen.findByText(adminRow.email)).toBeInTheDocument()

    await userEvent.setup().click(screen.getByRole('button', { name: /^settings$/i }))
    expect(await screen.findByRole('button', { name: /users & invitations/i })).toBeInTheDocument()
    get.mockRestore()
  })

  it('shortcut tiles leave the hub for import/export and stats', async () => {
    const onNavigate = vi.fn()
    renderWithProviders(<SettingsPage onNavigate={onNavigate} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /import \/ export/i }))
    expect(onNavigate).toHaveBeenCalledWith('import')
    await user.click(screen.getByRole('button', { name: /statistics/i }))
    expect(onNavigate).toHaveBeenCalledWith('stats')
  })

  it('opens the account, security and tokens tiles into their sections', async () => {
    renderWithProviders(<SettingsPage />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /^profile/i }))
    expect(await screen.findByRole('heading', { name: /your profile/i })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /settings/i }))

    await user.click(screen.getByRole('button', { name: /^account/i }))
    expect(await screen.findByRole('heading', { name: /sign-in & profile/i })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /settings/i }))

    await user.click(screen.getByRole('button', { name: /security & 2fa/i }))
    expect(await screen.findByRole('heading', { name: /two-factor authentication/i })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /settings/i }))

    await user.click(screen.getByRole('button', { name: /api tokens/i }))
    expect(await screen.findByRole('heading', { name: /^api tokens$/i, level: 1 })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /settings/i }))
    // Every back landed on the hub again.
    expect(screen.getByRole('button', { name: /^profile/i })).toBeInTheDocument()
  })

  // The topbar user menu deep-links here via initialSection (key-remounted by
  // AppShell); an unknown value must fall back to the overview, never crash.
  it('deep-links into the profile section and ignores unknown sections', async () => {
    renderWithProviders(<SettingsPage initialSection="profile" />)
    expect(await screen.findByRole('heading', { name: /your profile/i })).toBeInTheDocument()

    renderWithProviders(<SettingsPage initialSection="bogus" />)
    expect(await screen.findByRole('button', { name: /^profile/i })).toBeInTheDocument()
  })

  it('returns from the administration scope to personal via the segment', async () => {
    renderWithProviders(<SettingsPage />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /^administration$/i }))
    expect(await screen.findByRole('button', { name: /users & invitations/i })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /^personal$/i }))
    expect(await screen.findByRole('button', { name: /api tokens/i })).toBeInTheDocument()
  })

  // Pure-function lock on the demotion fallback: the branch is unreachable
  // through the UI (a non-admin has no admin tile to click), so the resolver
  // is exported and tested directly.
  it('resolveHubView collapses admin surfaces for a demoted session', () => {
    expect(resolveHubView(true, 'admin', 'admin')).toEqual({ scope: 'admin', section: 'admin' })
    expect(resolveHubView(false, 'admin', 'admin')).toEqual({ scope: 'personal', section: 'overview' })
    expect(resolveHubView(false, 'personal', 'admin')).toEqual({ scope: 'personal', section: 'overview' })
    expect(resolveHubView(false, 'personal', 'master')).toEqual({ scope: 'personal', section: 'master' })
    expect(resolveHubView(true, 'personal', 'locked')).toEqual({ scope: 'personal', section: 'locked' })
  })
})

describe('SettingsPage — master password', () => {
  it('shows the unconfigured state and sets a first master password + hint', async () => {
    await renderAtSection('master')
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
    await renderAtSection('master')
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
    await renderAtSection('master')
    await waitFor(() => expect(screen.getByText(/no master password configured/i)).toBeInTheDocument())
    await userEvent.setup().type(screen.getByLabelText(/^master password$/i), 'S0me-Very-Long-Pass!')
    expect(screen.getByText(/^strong$/i)).toBeInTheDocument()
  })

  it('blocks save when confirmation does not match', async () => {
    await renderAtSection('master')
    await waitFor(() => expect(screen.getByText(/no master password configured/i)).toBeInTheDocument())
    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/^master password$/i), 'super-secret-master')
    await user.type(screen.getByLabelText(/confirm password/i), 'different-value')
    expect(screen.getByRole('button', { name: /set master password/i })).toBeDisabled()
    expect(screen.getByText(/don't match|não coincidem|no coinciden/i)).toBeInTheDocument()
  })

  it('changes an existing master password with the correct current one', async () => {
    state.masterPassword = 'original-master'
    await renderAtSection('master')
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
    await renderAtSection('master')
    await waitFor(() => expect(screen.getByText(/a master password is configured/i)).toBeInTheDocument())

    const user = userEvent.setup()
    await user.type(screen.getByLabelText(/current master password/i), 'original-master')
    await user.click(screen.getByRole('button', { name: /remove/i }))

    await waitFor(() => expect(state.masterPassword).toBeUndefined())
    expect(await screen.findByText(/master password removed/i)).toBeInTheDocument()
  })

  it('rejects a too-short master password client-side', async () => {
    await renderAtSection('master')
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
    await renderAtSection('locked')

    await waitFor(() => expect(screen.getByText('Vault')).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /reset password/i }))
    await user.type(screen.getByLabelText('Master password'), 'master-pass')
    await user.click(screen.getByRole('button', { name: /^reset$/i }))

    await waitFor(() => expect(state.folderPasswords[7]).toBeUndefined())
    expect(screen.getByText(/password cleared/i)).toBeInTheDocument()
  })

  it('toggles between the reset and remove prompts on the same row', async () => {
    state.masterPassword = 'master-pass'
    lockedFolder(13, 'VaultToggle')
    await renderAtSection('locked')
    await waitFor(() => expect(screen.getByText('VaultToggle')).toBeInTheDocument())
    const user = userEvent.setup()
    const row = within(screen.getByText('VaultToggle').closest('li') as HTMLElement)

    await user.click(row.getByRole('button', { name: /reset password/i }))
    expect(row.getByText(/then set a new one/i)).toBeInTheDocument()

    // Switching to remove swaps the prompt (single shared input, new copy).
    await user.click(row.getByRole('button', { name: /remove password/i }))
    expect(row.getByText(/left with no password/i)).toBeInTheDocument()
    expect(row.queryByText(/then set a new one/i)).not.toBeInTheDocument()

    // Re-clicking the same action collapses it.
    await user.click(row.getByRole('button', { name: /remove password/i }))
    expect(row.queryByText(/left with no password/i)).not.toBeInTheDocument()
  })

  it('removes a folder password with the master and leaves it unprotected', async () => {
    state.masterPassword = 'master-pass'
    lockedFolder(12, 'VaultRemove')
    const onEditFolder = vi.fn()
    await renderAtSection('locked', onEditFolder)

    await waitFor(() => expect(screen.getByText('VaultRemove')).toBeInTheDocument())
    const user = userEvent.setup()
    // Scope to the folder's row — the master section also has a "Remove" button.
    const row = within(screen.getByText('VaultRemove').closest('li') as HTMLElement)
    await user.click(row.getByRole('button', { name: /remove password/i }))
    await user.type(row.getByLabelText('Master password'), 'master-pass')
    await user.click(row.getByRole('button', { name: /^remove$/i }))

    await waitFor(() => expect(state.folderPasswords[12]).toBeUndefined())
    expect(screen.getByText(/folder unlocked/i)).toBeInTheDocument()
    // Remove flow does NOT offer to set a new password.
    expect(screen.queryByRole('button', { name: /set new password/i })).not.toBeInTheDocument()
    expect(onEditFolder).not.toHaveBeenCalled()
  })

  it('shows the master hint on the reset prompt when set', async () => {
    state.masterPassword = 'master-pass'
    state.masterHint = 'starts with master'
    lockedFolder(11, 'VaultHint')
    await renderAtSection('locked')
    await waitFor(() => expect(screen.getByText('VaultHint')).toBeInTheDocument())
    await userEvent.setup().click(screen.getByRole('button', { name: /reset password/i }))
    // Exact string avoids colliding with the master section's "Current
    // reminder: …" read-only line, which also contains the hint.
    expect(await screen.findByText('Reminder: starts with master')).toBeInTheDocument()
  })

  it('shows an error when the master password is wrong', async () => {
    state.masterPassword = 'master-pass'
    lockedFolder(8, 'Vault8')
    await renderAtSection('locked')

    await waitFor(() => expect(screen.getByText('Vault8')).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /reset password/i }))
    await user.type(screen.getByLabelText('Master password'), 'wrong-one')
    await user.click(screen.getByRole('button', { name: /^reset$/i }))

    expect(await screen.findByText(/incorrect master password/i)).toBeInTheDocument()
    expect(state.folderPasswords[8]).toBe('folder-pass')
  })

  it('shows empty state when no folders are locked', async () => {
    await renderAtSection('locked')
    await waitFor(() => expect(screen.getByText(/no password-protected folders/i)).toBeInTheDocument())
  })

  it('calls onEditFolder after a successful reset', async () => {
    state.masterPassword = 'master-pass'
    lockedFolder(9, 'Vault9')
    const onEditFolder = vi.fn()
    await renderAtSection('locked', onEditFolder)

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

