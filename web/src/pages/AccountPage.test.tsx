import { describe, it, expect, beforeEach } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AccountPage } from './AccountPage'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock } from '../test/server'

let state: ReturnType<typeof freshState>

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})


describe('account page — grouped tabs ("temas")', () => {
  // Two sections per group on one screen: the group is the subject, the
  // sections under it are the two halves of that subject.
  it('stacks Perfil and Acesso under the Conta tab', async () => {
    renderWithProviders(<AccountPage />)
    expect(await screen.findByRole('region', { name: /^profile$/i })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: /^access$/i })).toBeInTheDocument()
    // The other groups' sections are not rendered.
    expect(screen.queryByRole('region', { name: /^two-factor$/i })).not.toBeInTheDocument()
  })

  it('switches the whole stack when the tab changes', async () => {
    renderWithProviders(<AccountPage />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('tab', { name: /^security$/i }))
    expect(await screen.findByRole('region', { name: /^two-factor$/i })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: /^sessions$/i })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: /^profile$/i })).not.toBeInTheDocument()
  })

  // A stale bookmark that deep-links a SECTION lands on its group — the one
  // place that section renders now.
  it('maps a legacy section deep-link onto its group', async () => {
    renderWithProviders(<AccountPage initialTab="sessions" />)
    expect(await screen.findByRole('region', { name: /^sessions$/i })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: /^security$/i })).toHaveAttribute('aria-selected', 'true')
  })
})
