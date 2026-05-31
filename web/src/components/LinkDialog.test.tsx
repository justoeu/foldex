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
      id: 7, url: 'https://x', title: 'old', slug: 'old', click_count: 0,
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

  // ─── change-detection select (Phase 5) ─────────────────────────────────
  // The select drives link.check_interval — null/empty = opt-out,
  // 'hourly'/'daily'/'weekly' = opt-in. We assert each value lands on the
  // POST/PATCH body so the backend's tri-state DTO receives the explicit
  // value (or null) rather than "field absent".

  it('CREATE: ships check_interval=null when the select stays at "Disabled"', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://x.test/a')
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.links).toHaveLength(1))
    // null in body == backend opt-out (default).
    expect(state.links[0].check_interval ?? null).toBeNull()
  })

  it('CREATE: ships check_interval=daily when the user picks "Every day"', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), 'https://x.test/b')
    const select = screen.getByRole('combobox', { name: /check for changes/i })
    await user.selectOptions(select, 'daily')
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.links).toHaveLength(1))
    expect(state.links[0].check_interval).toBe('daily')
  })

  it.each(['hourly', 'weekly'] as const)('CREATE: ships check_interval=%s', async (interval) => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox', { name: /^URL$/i }), `https://x.test/${interval}`)
    const select = screen.getByRole('combobox', { name: /check for changes/i })
    await user.selectOptions(select, interval)
    await user.click(screen.getByRole('button', { name: /Save link/i }))
    await waitFor(() => expect(state.links).toHaveLength(1))
    expect(state.links[0].check_interval).toBe(interval)
  })

  it('EDIT: setting "Disabled" sends check_interval=null on PATCH', async () => {
    // Seed an opted-in link, open it for edit, switch the select to off.
    state.links = [{
      id: 42,
      url: 'https://x.test/edit',
      title: 'editme',
      slug: 'editme',
      description: null,
      favicon_url: null,
      og_image_url: null,
      click_count: 0,
      preview_status: 'ok',
      preview_error: null,
      last_clicked_at: null,
      pinned: false,
      folder_id: null,
      created_at: '',
      updated_at: '',
      check_interval: 'daily',
      tags: [],
    }]
    renderWithProviders(<LinkDialog open link={state.links[0]} onClose={vi.fn()} />)
    const user = userEvent.setup()
    const select = screen.getByRole('combobox', { name: /check for changes/i })
    expect((select as HTMLSelectElement).value).toBe('daily')
    await user.selectOptions(select, '')
    await user.click(screen.getByRole('button', { name: /Save changes/i }))
    await waitFor(() => expect(state.links[0].check_interval ?? null).toBeNull())
  })

  // Next-check preview hint — locks the conditional render so removing the
  // span fails a test rather than silently dropping the UX.
  it('NEXT-CHECK PREVIEW: hidden when interval stays "Disabled"', () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    expect(screen.queryByTestId('check-next-preview')).not.toBeInTheDocument()
  })

  it('NEXT-CHECK PREVIEW: appears when user picks an interval', async () => {
    renderWithProviders(<LinkDialog open link={null} onClose={vi.fn()} />)
    const user = userEvent.setup()
    const select = screen.getByRole('combobox', { name: /check for changes/i })
    await user.selectOptions(select, 'daily')
    const hint = await screen.findByTestId('check-next-preview')
    // A fresh create (no last_checked_at) always renders the "soon" copy.
    expect(hint.textContent).toMatch(/Next check:/i)
    expect(hint.textContent).toMatch(/soon/i)
  })
})
