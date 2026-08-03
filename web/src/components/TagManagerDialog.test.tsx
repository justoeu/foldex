import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TagManagerDialog } from './TagManagerDialog'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  state.tags = [
    { id: 1, name: 'jira', color: '#1f6feb', icon: '🪲', link_count: 3 },
    { id: 2, name: 'empty', color: '#a78bfa', icon: null, link_count: 0 },
  ]
  installAxiosMock(state)
})

describe('TagManagerDialog', () => {
  it('renders nothing when closed', () => {
    renderWithProviders(<TagManagerDialog open={false} onClose={vi.fn()} />)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('lists tags sorted by link count and shows empty message when none', async () => {
    renderWithProviders(<TagManagerDialog open onClose={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    expect(screen.getByText('empty')).toBeInTheDocument()
    expect(screen.getByText('🪲')).toBeInTheDocument()

    state.tags = []
    // re-render with empty
    renderWithProviders(<TagManagerDialog open onClose={vi.fn()} />)
    await waitFor(() => expect(screen.getByText(/no tags|nenhuma|sin etiquetas/i)).toBeInTheDocument())
  })

  it('opens edit dialog for a tag', async () => {
    renderWithProviders(<TagManagerDialog open onClose={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    await userEvent.setup().click(screen.getByLabelText(/^edit jira$/i))
    expect((await screen.findAllByRole('dialog')).length).toBeGreaterThan(1)
  })

  it('deletes a tag with links after confirm', async () => {
    renderWithProviders(<TagManagerDialog open onClose={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/^delete jira$/i))
    await user.click(await screen.findByRole('button', { name: /^Delete tag$/i }))
    await waitFor(() => expect(state.tags.find((t) => t.id === 1)).toBeUndefined())
  })

  it('deletes a tag with no links', async () => {
    renderWithProviders(<TagManagerDialog open onClose={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('empty')).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/^delete empty$/i))
    await user.click(await screen.findByRole('button', { name: /^Delete tag$/i }))
    await waitFor(() => expect(state.tags.find((t) => t.id === 2)).toBeUndefined())
  })

  it('cancels delete', async () => {
    renderWithProviders(<TagManagerDialog open onClose={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/^delete jira$/i))
    await user.click(await screen.findByRole('button', { name: /cancel/i }))
    expect(state.tags.find((t) => t.id === 1)).toBeTruthy()
  })

  it('opens create dialog from footer', async () => {
    renderWithProviders(<TagManagerDialog open onClose={vi.fn()} />)
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    await userEvent.setup().click(screen.getByRole('button', { name: /New tag/i }))
    expect((await screen.findAllByRole('dialog')).length).toBeGreaterThan(1)
  })

  it('closes via header X', async () => {
    const onClose = vi.fn()
    renderWithProviders(<TagManagerDialog open onClose={onClose} />)
    await waitFor(() => expect(screen.getByText('jira')).toBeInTheDocument())
    await userEvent.setup().click(screen.getByLabelText(/^close$/i))
    expect(onClose).toHaveBeenCalled()
  })
})
