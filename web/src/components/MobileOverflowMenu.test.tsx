import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MobileOverflowMenu } from './MobileOverflowMenu'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock, type MockState } from '../test/server'
import { http } from '../api/client'

let state: MockState

beforeEach(() => {
  state = freshState()
  installAxiosMock(state)
  // The profile PATCH has no route in the shared mock; resolve it so the
  // write-through completes instead of reading as a rejected revert.
  vi.mocked(http.patch).mockImplementation((async (url: string) => {
    if (url === '/api/auth/profile') return { data: {} }
    throw new Error(`unexpected PATCH ${url}`)
  }) as typeof http.patch)
})

function renderMenu() {
  return renderWithProviders(
    <MobileOverflowMenu
      sort="created"
      setSort={vi.fn()}
      viewMode="cards"
      setViewMode={vi.fn()}
      gridCols={3}
      setGridCols={vi.fn()}
      foldersCompact={false}
      setFoldersCompact={vi.fn()}
      onNewFolder={vi.fn()}
      onNewLink={vi.fn()}
      onNewNote={vi.fn()}
      dark={false}
      setDark={vi.fn()}
      view="home"
      setView={vi.fn()}
    />,
  )
}

// Regression (BUG-ART-103): the mobile language pick used to call
// i18n.changeLanguage directly, skipping the account write-through — the
// choice visibly applied and silently reverted on the next load, exactly
// what useLocaleChoice exists to prevent. Every other surface routes the
// same pick through choose(); the overflow menu must too.
describe('MobileOverflowMenu locale pick', () => {
  it('routes a language pick through useLocaleChoice (account write-through)', async () => {
    const user = userEvent.setup()
    renderMenu()

    await user.click(screen.getByRole('button', { name: /more/i }))
    await user.click(screen.getByRole('menuitemradio', { name: /language · en/i }))
    await user.click(screen.getByRole('option', { name: /português/i }))

    await waitFor(() => {
      const call = vi.mocked(http.patch).mock.calls.find(([url]) => url === '/api/auth/profile')
      expect(call).toBeDefined()
      expect(call![1]).toEqual({ locale: 'pt' })
    })
  })
})
