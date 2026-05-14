import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { TagDialog } from './TagDialog'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

describe('TagDialog', () => {
  it('does not render content when closed', () => {
    renderWithProviders(<TagDialog open={false} onClose={vi.fn()} />)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('submits the form and calls onClose on success', async () => {
    const onClose = vi.fn()
    renderWithProviders(<TagDialog open onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('tag name'), 'mynew')
    await user.click(screen.getByRole('button', { name: /Criar tag/i }))
    expect(state.tags[0]?.name).toBe('mynew')
    expect(onClose).toHaveBeenCalled()
  })

  it('disables submit when name is empty', () => {
    const onClose = vi.fn()
    renderWithProviders(<TagDialog open onClose={onClose} />)
    expect(screen.getByRole('button', { name: /Criar tag/i })).toBeDisabled()
    expect(state.tags).toHaveLength(0)
    expect(onClose).not.toHaveBeenCalled()
  })

  it('Cancel just closes', async () => {
    const onClose = vi.fn()
    renderWithProviders(<TagDialog open onClose={onClose} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /Cancelar/i }))
    expect(onClose).toHaveBeenCalled()
  })

  it('saves a gradient color when gradient mode is selected', async () => {
    const onClose = vi.fn()
    renderWithProviders(<TagDialog open onClose={onClose} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('tag name'), 'rainbow')
    await user.click(screen.getByRole('tab', { name: /Gradiente/i }))
    await user.click(screen.getByRole('button', { name: /Criar tag/i }))
    expect(state.tags[0]?.name).toBe('rainbow')
    expect(state.tags[0]?.color).toMatch(/^linear-gradient\(135deg,\s*#/)
  })

  it('pre-fills gradient stops when editing a gradient tag', () => {
    const tag = {
      id: 7,
      name: 'pink-x',
      color: 'linear-gradient(135deg, #6366F1, #EC4899)',
      icon: null,
      created_at: new Date().toISOString(),
      link_count: 0,
    }
    renderWithProviders(<TagDialog open onClose={vi.fn()} tag={tag} />)
    const gradTab = screen.getByRole('tab', { name: /Gradiente/i })
    expect(gradTab).toHaveAttribute('aria-selected', 'true')
  })

  it('saves a new solid color when a swatch is clicked', async () => {
    renderWithProviders(<TagDialog open onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('tag name'), 'cyan')
    await user.click(screen.getByLabelText('color #0EA5E9'))
    await user.click(screen.getByRole('button', { name: /Criar tag/i }))
    expect(state.tags[0]?.color).toBe('#0EA5E9')
  })

  it('updates the end stop when a gradient swatch is clicked', async () => {
    renderWithProviders(<TagDialog open onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('tag name'), 'sunset')
    await user.click(screen.getByRole('tab', { name: /Gradiente/i }))
    await user.click(screen.getByLabelText(/Fim #F59E0B/))
    await user.click(screen.getByRole('button', { name: /Criar tag/i }))
    expect(state.tags[0]?.color).toContain('#F59E0B')
  })

  it('submits an edit (PATCH path) preserving the gradient choice', async () => {
    state.tags.push({
      id: 12,
      name: 'mix',
      color: '#6366F1',
      icon: null,
      created_at: new Date().toISOString(),
      link_count: 0,
    })
    const tag = state.tags[0]
    renderWithProviders(<TagDialog open onClose={vi.fn()} tag={tag} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: /Gradiente/i }))
    await user.click(screen.getByRole('button', { name: /Salvar/i }))
    expect(state.tags[0]?.color).toMatch(/^linear-gradient/)
  })

  it('switching to solid mode hides the gradient stop labels', async () => {
    renderWithProviders(<TagDialog open onClose={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: /Gradiente/i }))
    expect(screen.getByText(/Início/i)).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: /Sólida/i }))
    expect(screen.queryByText(/Início/i)).not.toBeInTheDocument()
  })
})
