import { describe, it, expect, beforeEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { useMasterPasswordStatus, useSetMasterPassword, useRemoveMasterPassword } from './settings'
import { useResetFolderPassword } from './folders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { makeQueryClient } from '../test/renderWithProviders'

let state: MockState

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={makeQueryClient()}>{children}</QueryClientProvider>
}

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

describe('settings hooks', () => {
  it('reports unconfigured status by default', async () => {
    const { result } = renderHook(() => useMasterPasswordStatus(), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.data?.configured).toBe(false)
  })

  it('sets the master password (first time, no current required)', async () => {
    const { result } = renderHook(() => useSetMasterPassword(), { wrapper })
    const out = await result.current.mutateAsync({ password: 'super-secret' })
    expect(out.configured).toBe(true)
    expect(state.masterPassword).toBe('super-secret')
  })

  it('rejects changing the master without the correct current password', async () => {
    state.masterPassword = 'original-pass'
    const { result } = renderHook(() => useSetMasterPassword(), { wrapper })
    await expect(
      result.current.mutateAsync({ password: 'new-password', currentPassword: 'wrong' }),
    ).rejects.toBeTruthy()
    expect(state.masterPassword).toBe('original-pass')
  })

  it('removes the master password with the correct current password', async () => {
    state.masterPassword = 'to-remove-pass'
    const { result } = renderHook(() => useRemoveMasterPassword(), { wrapper })
    const out = await result.current.mutateAsync({ currentPassword: 'to-remove-pass' })
    expect(out.configured).toBe(false)
    expect(state.masterPassword).toBeUndefined()
  })
})

describe('useResetFolderPassword', () => {
  it('clears a folder password + hint with the correct master', async () => {
    state.masterPassword = 'master-pass'
    state.folders.push({
      id: 3,
      name: 'Secret',
      color: '#abc',
      parent_id: null,
      has_password: true,
      password_hint: 'a clue',
      link_count: 0,
      folder_count: 0,
      preview_links: [],
      preview_folders: [],
    })
    state.folderPasswords[3] = 'folder-pass'

    const { result } = renderHook(() => useResetFolderPassword(), { wrapper })
    await result.current.mutateAsync({ id: 3, masterPassword: 'master-pass' })

    expect(state.folderPasswords[3]).toBeUndefined()
    expect(state.folders[0]?.has_password).toBe(false)
    expect(state.folders[0]?.password_hint).toBeNull()
  })

  it('rejects with wrong_master_password on an incorrect master', async () => {
    state.masterPassword = 'master-pass'
    state.folders.push({
      id: 4,
      name: 'Secret',
      color: '#abc',
      parent_id: null,
      has_password: true,
      link_count: 0,
      folder_count: 0,
      preview_links: [],
      preview_folders: [],
    })
    state.folderPasswords[4] = 'folder-pass'

    const { result } = renderHook(() => useResetFolderPassword(), { wrapper })
    await expect(result.current.mutateAsync({ id: 4, masterPassword: 'nope' })).rejects.toMatchObject({
      response: { data: { error: { code: 'wrong_master_password' } } },
    })
    expect(state.folderPasswords[4]).toBe('folder-pass')
  })
})
