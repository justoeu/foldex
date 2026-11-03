import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { FoldersPanel } from './FoldersPanel'
import { renderWithProviders } from '../../test/renderWithProviders'
import type { Folder } from '../../api/types'

function folder(id: number, name: string, parent_id: number | null, extra: Partial<Folder> = {}): Folder {
  return {
    id,
    name,
    color: '#6366F1',
    parent_id,
    has_password: false,
    link_count: id,
    folder_count: 0,
    preview_links: [],
    preview_folders: [],
    ...extra,
  } as Folder
}

beforeEach(() => {
  localStorage.removeItem('foldex.sidebar.tab')
})

describe('FoldersPanel', () => {
  it('groups children under their parent inside the indent container', () => {
    const { container } = renderWithProviders(
      <FoldersPanel
        folders={[
          folder(1, 'Dev', null),
          folder(2, 'Frontend', 1),
          folder(3, 'Backend', 1),
          folder(4, 'Leitura', null),
        ]}
        openFolderId={null}
        onOpenFolder={vi.fn()}
        onNewFolder={vi.fn()}
      />,
    )
    const groups = container.querySelectorAll('.fx-folder-children')
    expect(groups).toHaveLength(1)
    expect(within(groups[0] as HTMLElement).getByText('Frontend')).toBeInTheDocument()
    expect(within(groups[0] as HTMLElement).getByText('Backend')).toBeInTheDocument()
    expect(screen.getByText('Dev')).toBeInTheDocument()
    expect(screen.getByText('Leitura')).toBeInTheDocument()
  })

  it('hides password-protected folders and their whole subtree', () => {
    renderWithProviders(
      <FoldersPanel
        folders={[
          folder(1, 'Dev', null, { has_password: true }),
          folder(2, 'Frontend', 1),
          folder(3, 'Backend', 1),
          folder(4, 'Leitura', null),
        ]}
        openFolderId={null}
        onOpenFolder={vi.fn()}
        onNewFolder={vi.fn()}
      />,
    )
    expect(screen.queryByText('Dev')).not.toBeInTheDocument()
    expect(screen.queryByText('Frontend')).not.toBeInTheDocument()
    expect(screen.queryByText('Backend')).not.toBeInTheDocument()
    expect(screen.getByText('Leitura')).toBeInTheDocument()
  })

  it('hides a protected child but keeps its unprotected parent and sibling', () => {
    renderWithProviders(
      <FoldersPanel
        folders={[
          folder(1, 'Dev', null),
          folder(2, 'Frontend', 1, { has_password: true }),
          folder(3, 'Backend', 1),
        ]}
        openFolderId={null}
        onOpenFolder={vi.fn()}
        onNewFolder={vi.fn()}
      />,
    )
    expect(screen.getByText('Dev')).toBeInTheDocument()
    expect(screen.queryByText('Frontend')).not.toBeInTheDocument()
    expect(screen.getByText('Backend')).toBeInTheDocument()
  })

  it('hides a grandchild under a protected child, keeping deeper unprotected branches', () => {
    renderWithProviders(
      <FoldersPanel
        folders={[
          folder(1, 'Dev', null),
          folder(2, 'Frontend', 1, { has_password: true }),
          folder(5, 'Hooks', 2),
          folder(3, 'Backend', 1),
        ]}
        openFolderId={null}
        onOpenFolder={vi.fn()}
        onNewFolder={vi.fn()}
      />,
    )
    expect(screen.queryByText('Frontend')).not.toBeInTheDocument()
    expect(screen.queryByText('Hooks')).not.toBeInTheDocument()
    expect(screen.getByText('Backend')).toBeInTheDocument()
  })

  it('renders an orphan folder at the root instead of dropping it', () => {
    renderWithProviders(
      <FoldersPanel
        folders={[folder(1, 'Dev', null), folder(9, 'Órfã', 999)]}
        openFolderId={null}
        onOpenFolder={vi.fn()}
        onNewFolder={vi.fn()}
      />,
    )
    expect(screen.getByText('Dev')).toBeInTheDocument()
    expect(screen.getByText('Órfã')).toBeInTheDocument()
  })

  it('highlights the currently open folder', () => {
    renderWithProviders(
      <FoldersPanel
        folders={[folder(1, 'Dev', null), folder(2, 'Frontend', 1)]}
        openFolderId={2}
        onOpenFolder={vi.fn()}
        onNewFolder={vi.fn()}
      />,
    )
    const active = screen.getByText('Frontend').closest('button')
    expect(active).not.toBeNull()
    expect(active!.className).toContain('fx-side-row-active')
    const inactive = screen.getByText('Dev').closest('button')
    expect(inactive!.className).not.toContain('fx-side-row-active')
  })

  it('calls onOpenFolder with the folder id on click', async () => {
    const onOpenFolder = vi.fn()
    renderWithProviders(
      <FoldersPanel folders={[folder(1, 'Dev', null)]} openFolderId={null} onOpenFolder={onOpenFolder} onNewFolder={vi.fn()} />,
    )
    const user = userEvent.setup()
    await user.click(screen.getByText('Dev'))
    expect(onOpenFolder).toHaveBeenCalledWith(1)
  })

  it('calls onNewFolder from the panel action', async () => {
    const onNewFolder = vi.fn()
    renderWithProviders(
      <FoldersPanel folders={[folder(1, 'Dev', null)]} openFolderId={null} onOpenFolder={vi.fn()} onNewFolder={onNewFolder} />,
    )
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /New folder/i }))
    expect(onNewFolder).toHaveBeenCalledOnce()
  })

  it('shows the empty state when no visible folder exists', () => {
    renderWithProviders(
      <FoldersPanel
        folders={[folder(1, 'Privado', null, { has_password: true })]}
        openFolderId={null}
        onOpenFolder={vi.fn()}
        onNewFolder={vi.fn()}
      />,
    )
    expect(screen.getByText('No folders yet')).toBeInTheDocument()
    expect(screen.queryByText('Privado')).not.toBeInTheDocument()
  })
})
