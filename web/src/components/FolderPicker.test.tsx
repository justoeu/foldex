import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { FolderPicker } from './FolderPicker'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { http } from '../api/client'

let state: MockState

beforeEach(() => {
  state = freshState()
  state.folders = [
    { id: 1, name: 'Work', color: '#6366F1', link_count: 2, folder_count: 0, preview_links: [], preview_folders: [], has_password: false },
    { id: 2, name: 'Secret', color: '#000', link_count: 0, folder_count: 0, preview_links: [], preview_folders: [], has_password: true },
    { id: 3, name: 'Archive', color: '#111', link_count: 1, folder_count: 0, preview_links: [], preview_folders: [], has_password: false },
  ]
  installAxiosMock(state)
})

function inputEl() {
  return document.querySelector('.fx-folderpicker-input') as HTMLInputElement
}

describe('FolderPicker', () => {
  it('opens and lists folders including locked ones', async () => {
    renderWithProviders(<FolderPicker selected={null} onChange={vi.fn()} />)
    await waitFor(() => expect(inputEl()).toBeInTheDocument())
    await userEvent.setup().click(inputEl())
    await waitFor(() => expect(screen.getByText('Work')).toBeInTheDocument())
    expect(screen.getByText('Secret')).toBeInTheDocument()
    expect(document.querySelector('.fx-folder-lock-icon')).toBeTruthy()
    expect(screen.getByText(/No folder/i)).toBeInTheDocument()
  })

  it('selects a folder on click', async () => {
    const onChange = vi.fn()
    renderWithProviders(<FolderPicker selected={null} onChange={onChange} />)
    await waitFor(() => expect(inputEl()).toBeInTheDocument())
    await userEvent.setup().click(inputEl())
    await waitFor(() => expect(screen.getByText('Work')).toBeInTheDocument())
    fireEvent.mouseDown(screen.getByText('Work'))
    expect(onChange).toHaveBeenCalledWith(1)
  })

  it('clears selection via No folder', async () => {
    const onChange = vi.fn()
    renderWithProviders(<FolderPicker selected={1} onChange={onChange} />)
    await waitFor(() => expect(inputEl()).toBeInTheDocument())
    await userEvent.setup().click(inputEl())
    fireEvent.mouseDown(await screen.findByText(/No folder/i))
    expect(onChange).toHaveBeenCalledWith(null)
  })

  it('filters folders by typed text', async () => {
    renderWithProviders(<FolderPicker selected={null} onChange={vi.fn()} />)
    await waitFor(() => expect(inputEl()).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(inputEl())
    await user.type(inputEl(), 'arc')
    await waitFor(() => expect(screen.getByText('Archive')).toBeInTheDocument())
    expect(screen.queryByText('Work')).toBeNull()
  })

  it('creates a folder inline when filter has no exact match', async () => {
    const onChange = vi.fn()
    renderWithProviders(<FolderPicker selected={null} onChange={onChange} parentId={null} />)
    await waitFor(() => expect(inputEl()).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(inputEl())
    await user.type(inputEl(), 'BrandNew')
    await waitFor(() => expect(screen.getByText(/Create folder "BrandNew"/i)).toBeInTheDocument())
    fireEvent.mouseDown(screen.getByText(/Create folder "BrandNew"/i))
    await waitFor(() => expect(onChange).toHaveBeenCalled())
    expect(state.folders.some((f) => f.name === 'BrandNew')).toBe(true)
  })

  it('does not create when empty create row is clicked — focuses input', async () => {
    const onChange = vi.fn()
    renderWithProviders(<FolderPicker selected={null} onChange={onChange} />)
    await waitFor(() => expect(inputEl()).toBeInTheDocument())
    await userEvent.setup().click(inputEl())
    const create = await screen.findByText(/New folder/i)
    fireEvent.mouseDown(create)
    expect(onChange).not.toHaveBeenCalled()
  })

  it('excludes ids from the list', async () => {
    renderWithProviders(
      <FolderPicker selected={null} onChange={vi.fn()} excludeIds={new Set([1, 3])} />,
    )
    await waitFor(() => expect(inputEl()).toBeInTheDocument())
    await userEvent.setup().click(inputEl())
    await waitFor(() => expect(screen.getByText('Secret')).toBeInTheDocument())
    expect(screen.queryByText('Work')).toBeNull()
    expect(screen.queryByText('Archive')).toBeNull()
  })

  it('supports keyboard navigation and Enter to commit', async () => {
    const onChange = vi.fn()
    renderWithProviders(<FolderPicker selected={null} onChange={onChange} />)
    await waitFor(() => expect(inputEl()).toBeInTheDocument())
    await userEvent.setup().click(inputEl())
    await waitFor(() => expect(screen.getByRole('listbox')).toBeInTheDocument())
    fireEvent.keyDown(inputEl(), { key: 'ArrowDown' })
    fireEvent.keyDown(inputEl(), { key: 'ArrowDown' })
    fireEvent.keyDown(inputEl(), { key: 'Enter' })
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(1))
  })

  it('moves the keyboard highlight up without wrapping past the first row', async () => {
    const onChange = vi.fn()
    renderWithProviders(<FolderPicker selected={null} onChange={onChange} />)
    await waitFor(() => expect(inputEl()).toBeInTheDocument())
    await userEvent.setup().click(inputEl())
    await waitFor(() => expect(screen.getByRole('listbox')).toBeInTheDocument())

    fireEvent.keyDown(inputEl(), { key: 'ArrowDown' })
    fireEvent.keyDown(inputEl(), { key: 'ArrowUp' })
    fireEvent.keyDown(inputEl(), { key: 'ArrowUp' })
    fireEvent.keyDown(inputEl(), { key: 'Enter' })

    expect(onChange).not.toHaveBeenCalled()
    expect(inputEl()).toHaveFocus()
  })

  it('clamps an active row when the available folder list shrinks', async () => {
    const onChange = vi.fn()
    const { rerender } = renderWithProviders(<FolderPicker selected={1} onChange={onChange} />)
    await waitFor(() => expect(inputEl()).toBeInTheDocument())
    await userEvent.setup().click(inputEl())

    const archive = await screen.findByRole('option', { name: 'Archive' })
    fireEvent.mouseEnter(archive)
    expect(archive).toHaveClass('fx-folderpicker-row-active')

    rerender(<FolderPicker selected={1} onChange={onChange} excludeIds={new Set([1, 2, 3])} />)
    const noFolder = await screen.findByRole('option', { name: /No folder/i })
    await waitFor(() => expect(noFolder).toHaveClass('fx-folderpicker-row-active'))
    fireEvent.keyDown(inputEl(), { key: 'Enter' })

    expect(onChange).toHaveBeenCalledWith(null)
  })

  it('stays usable and retries after inline folder creation is rejected', async () => {
    vi.mocked(http.post).mockRejectedValueOnce(new Error('create failed'))
    const onChange = vi.fn()
    renderWithProviders(<FolderPicker selected={null} onChange={onChange} />)
    await waitFor(() => expect(inputEl()).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(inputEl())
    await user.type(inputEl(), 'BrandNew')

    fireEvent.mouseDown(await screen.findByText(/Create folder "BrandNew"/i))
    await waitFor(() => expect(inputEl()).toBeEnabled())
    expect(inputEl()).toHaveValue('BrandNew')
    expect(screen.getByRole('listbox')).toBeInTheDocument()
    expect(onChange).not.toHaveBeenCalled()

    fireEvent.mouseDown(screen.getByText(/Create folder "BrandNew"/i))
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(4))
  })

  it('closes on Escape and Tab', async () => {
    renderWithProviders(<FolderPicker selected={null} onChange={vi.fn()} />)
    await waitFor(() => expect(inputEl()).toBeInTheDocument())
    await userEvent.setup().click(inputEl())
    await waitFor(() => expect(screen.getByRole('listbox')).toBeInTheDocument())
    fireEvent.keyDown(inputEl(), { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())

    await userEvent.setup().click(inputEl())
    await waitFor(() => expect(screen.getByRole('listbox')).toBeInTheDocument())
    fireEvent.keyDown(inputEl(), { key: 'Tab' })
    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())
  })

  it('closes on outside mousedown', async () => {
    renderWithProviders(
      <div>
        <button type="button">out</button>
        <FolderPicker selected={null} onChange={vi.fn()} />
      </div>,
    )
    await waitFor(() => expect(inputEl()).toBeInTheDocument())
    await userEvent.setup().click(inputEl())
    await waitFor(() => expect(screen.getByRole('listbox')).toBeInTheDocument())
    fireEvent.mouseDown(screen.getByText('out'))
    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())
  })

  it('toggles open via chevron', async () => {
    renderWithProviders(<FolderPicker selected={null} onChange={vi.fn()} />)
    const toggle = await screen.findByLabelText(/Toggle folder list/i)
    fireEvent.click(toggle)
    await waitFor(() => expect(screen.getByRole('listbox')).toBeInTheDocument())
    fireEvent.click(toggle)
    await waitFor(() => expect(screen.queryByRole('listbox')).toBeNull())
  })
})
