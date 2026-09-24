import { describe, it, expect, beforeEach } from 'vitest'
import { screen, within, fireEvent } from '@testing-library/react'
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

describe('account page — copy contracts', () => {
  // The token scope sentence is a security contract in words: it tells the
  // owner what a leaked token can and cannot do before they create one.
  it('states what an API token cannot do on the tokens section', async () => {
    renderWithProviders(<AccountPage initialTab="tokens" />)
    expect(await screen.findByText(/does not change your password/i)).toBeInTheDocument()
  })
})

describe('account page — tablist keyboard model', () => {
  // The tab roles promise the APG keyboard contract; without the test the
  // handler is silent a11y breakage waiting for a refactor.
  it('moves selection and focus with the arrow keys, wrapping around', async () => {
    renderWithProviders(<AccountPage />)
    const list = await screen.findByRole('tablist')
    const tabs = within(list).getAllByRole('tab')
    tabs[0].focus()

    fireEvent.keyDown(list, { key: 'ArrowRight' })
    expect(tabs[1]).toHaveFocus()
    expect(tabs[1]).toHaveAttribute('aria-selected', 'true')

    fireEvent.keyDown(list, { key: 'ArrowLeft' })
    expect(tabs[0]).toHaveFocus()

    fireEvent.keyDown(list, { key: 'End' })
    expect(tabs.at(-1)).toHaveFocus()
    fireEvent.keyDown(list, { key: 'Home' })
    expect(tabs[0]).toHaveFocus()
  })
})
