import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { LinkDialog } from './LinkDialog'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import type { Link } from '../api/types'

let state: MockState

beforeEach(() => {
  state = freshState()
  state.tags.push({ id: 1, name: 'jira', color: '#1f6feb', icon: null })
  installAxiosMock(state)
})

describe('LinkDialog', () => {
  it('does not show content when closed', () => {
    renderWithProviders(<LinkDialog open={false} link={null} onClose={vi.fn()} />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('creates a link with selected existing tag', async () => {
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={null} onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://example.com')
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.links).toHaveLength(1))
    expect(state.links[0].url).toBe('https://example.com')
    expect(onClose).toHaveBeenCalled()
  })

  it('edits an existing link', async () => {
    const link: Link = {
      id: 7, url: 'https://x', title: 'old', click_count: 0,
      preview_status: 'ok', pinned: false, created_at: '', updated_at: '', tags: [],
    } as Link
    state.links.push(link)
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={link} onClose={onClose} />)
    const user = userEvent.setup()
    const titleInput = screen.getByRole('textbox', { name: /Title/i }) as HTMLInputElement
    await user.clear(titleInput)
    await user.type(titleInput, 'renamed')
    await user.click(screen.getByRole('button', { name: /Save changes/i }))
    await waitFor(() => expect(state.links[0].title).toBe('renamed'))
    expect(onClose).toHaveBeenCalled()
  })

  it('Cancel closes without saving', async () => {
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={null} onClose={onClose} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /Cancel/i }))
    expect(onClose).toHaveBeenCalled()
    expect(state.links).toHaveLength(0)
  })

  it('uses initialUrl when no link is passed', () => {
    renderWithProviders(<LinkDialog open link={null} initialUrl="https://pre" onClose={vi.fn()} />)
    expect((screen.getByRole('textbox', { name: /^URL$/i }) as HTMLInputElement).value).toBe('https://pre')
  })

  it('disables save when URL is empty', () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    expect(screen.getByRole('button', { name: /Save link/i })).toBeDisabled()
  })

  it('creates a new tag inline via tag filter input', async () => {
    const onClose = vi.fn()
    renderWithProviders(<LinkDialog open link={null} onClose={onClose} />)
    const user = userEvent.setup()

    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://x')
    const tagsInput = screen.getByLabelText('tag filter')
    await user.type(tagsInput, 'brand-new{Enter}')
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.tags.some((t) => t.name === 'brand-new')).toBe(true))
    expect(state.links).toHaveLength(1)
  })

  it('picks an existing tag from the suggestions', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://y')
    const tagsInput = screen.getByLabelText('tag filter')
    await user.type(tagsInput, 'j')
    const jiraChip = await screen.findByText('jira')
    await user.click(jiraChip)
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.links).toHaveLength(1))
    expect(state.links[0].tags[0].name).toBe('jira')
  })
})
