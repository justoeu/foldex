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
    const folder = { id: 1, name: 'Existing', color: '#6366F1', parent_id: null, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [] }
    renderWithProviders(<FolderDialog open onClose={vi.fn()} folder={folder} />)
    expect(screen.getByDisplayValue('Existing')).toBeInTheDocument()
  })

  it('shows delete buttons when editing (not justCreated)', () => {
    const folder = { id: 1, name: 'Existing', color: '#6366F1', parent_id: null, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [] }
    renderWithProviders(<FolderDialog open onClose={vi.fn()} folder={folder} />)
    // Two delete buttons exist ("Delete folder" + "Delete everything")
    const buttons = screen.getAllByText(/delete|remover|apagar/i)
    expect(buttons.length).toBeGreaterThanOrEqual(1)
  })

  it('hides delete buttons when justCreated is true', () => {
    const folder = { id: 1, name: 'New', color: '#6366F1', parent_id: null, link_count: 0, folder_count: 0, preview_links: [], preview_folders: [] }
    renderWithProviders(<FolderDialog open onClose={vi.fn()} folder={folder} justCreated />)
    expect(screen.queryByText(/delete|remover|apagar/i)).not.toBeInTheDocument()
  })
})
