import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TagSidebar } from './TagSidebar'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  state.tags.push(
    { id: 1, name: 'jira', color: '#1f6feb', icon: '🪲', link_count: 3 },
    { id: 2, name: 'docs', color: '#a78bfa', icon: null, link_count: 0 },
  )
  installAxiosMock(state)
})

describe('TagSidebar', () => {
  it('lists tags with their link counts', async () => {
    renderWithProviders(<TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={5} collapsed={false} onToggleCollapsed={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    expect(screen.getByText('3')).toBeInTheDocument()
    expect(screen.getByText('docs')).toBeInTheDocument()
    // docs has link_count: 0; we just verify the tag name is present
    expect(screen.getAllByText('0').length).toBeGreaterThan(0)
  })

  it('toggles a tag when clicked', async () => {
    const onToggle = vi.fn()
    renderWithProviders(<TagSidebar selected={[]} onToggle={onToggle} onClear={vi.fn()} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(screen.getByText('jira'))
    expect(onToggle).toHaveBeenCalledWith(1)
  })

  it('clears selection when "Todos os links" button is clicked', async () => {
    const onClear = vi.fn()
    renderWithProviders(<TagSidebar selected={[1]} onToggle={vi.fn()} onClear={onClear} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByText('All links'))
    expect(onClear).toHaveBeenCalled()
  })

  it('opens the new-tag dialog from the "Nova" button', async () => {
    renderWithProviders(<TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /^New$/i }))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })

  it('opens the tag manager dialog from the "Gerenciar" button', async () => {
    renderWithProviders(<TagSidebar selected={[]} onToggle={vi.fn()} onClear={vi.fn()} totalLinks={0} collapsed={false} onToggleCollapsed={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /Manage/i }))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })
})
