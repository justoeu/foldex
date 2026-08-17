import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { UserMenu } from './UserMenu'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
})

describe('UserMenu', () => {
  it('opens the menu from the avatar button and offers profile + sign out', async () => {
    const onOpenProfile = vi.fn()
    renderWithProviders(<UserMenu onOpenProfile={onOpenProfile} />)
    const user = userEvent.setup()

    // Closed: the dropdown items are not rendered.
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /account menu/i }))
    expect(screen.getByRole('menu')).toBeInTheDocument()
    expect(screen.getAllByText('Test Admin').length).toBeGreaterThan(0)
    expect(screen.getAllByText('admin@foldex.test').length).toBeGreaterThan(0)

    await user.click(screen.getByRole('menuitem', { name: /profile/i }))
    expect(onOpenProfile).toHaveBeenCalledTimes(1)
    // Choosing an item closes the menu.
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('closes on Escape', async () => {
    renderWithProviders(<UserMenu onOpenProfile={vi.fn()} />)
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /account menu/i }))
    expect(screen.getByRole('menu')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })
})
