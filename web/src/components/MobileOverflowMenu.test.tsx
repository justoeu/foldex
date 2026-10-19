import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, waitFor, fireEvent } from '@testing-library/react'
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

function renderMenu(overrides: Partial<Parameters<typeof MobileOverflowMenu>[0]> = {}) {
  const handlers = {
    setSort: vi.fn(), setViewMode: vi.fn(), setGridCols: vi.fn(),
    setFoldersCompact: vi.fn(), onNewFolder: vi.fn(), onNewLink: vi.fn(),
    onNewNote: vi.fn(), setDark: vi.fn(), setView: vi.fn(),
  }
  const utils = renderWithProviders(
    <MobileOverflowMenu
      sort="created"
      viewMode="cards"
      gridCols={3}
      foldersCompact={false}
      dark={false}
      view="home"
      {...handlers}
      {...overrides}
    />,
  )
  return { ...utils, handlers }
}

async function openMenu() {
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: /more/i }))
  return user
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

describe('MobileOverflowMenu actions', () => {
  it.each([
    [/new link/i, 'onNewLink'],
    [/new folder/i, 'onNewFolder'],
    [/new note/i, 'onNewNote'],
  ] as const)('fires %s and closes the menu', async (name, key) => {
    const { handlers } = renderMenu()
    const user = await openMenu()
    await user.click(screen.getByRole('menuitemradio', { name }))
    expect(handlers[key]).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('navigates to settings and marks the row active on that view', async () => {
    const { handlers } = renderMenu({ view: 'settings' })
    const user = await openMenu()
    const row = screen.getByRole('menuitemradio', { name: /settings/i })
    expect(row).toHaveAttribute('aria-checked', 'true')
    await user.click(row)
    expect(handlers.setView).toHaveBeenCalledWith('settings')
  })

  it.each([
    [/newest/i, 'created'], [/^top$/i, 'clicks'], [/recent/i, 'recent'],
    [/a→z/i, 'alpha'], [/z→a/i, 'alpha_desc'],
  ] as const)('sort row %s calls setSort(%s)', async (name, sort) => {
    const { handlers } = renderMenu()
    const user = await openMenu()
    await user.click(screen.getByRole('menuitemradio', { name }))
    expect(handlers.setSort).toHaveBeenCalledWith(sort)
  })

  it('marks the current sort row as checked', async () => {
    renderMenu({ sort: 'clicks' })
    await openMenu()
    expect(screen.getByRole('menuitemradio', { name: /^top$/i })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByRole('menuitemradio', { name: /newest/i })).toHaveAttribute('aria-checked', 'false')
  })

  it.each([
    [/^cards$/i, 'cards'], [/^compact$/i, 'compact'], [/^list$/i, 'list'],
  ] as const)('view row %s calls setViewMode(%s)', async (name, mode) => {
    const { handlers } = renderMenu()
    const user = await openMenu()
    await user.click(screen.getByRole('menuitemradio', { name }))
    expect(handlers.setViewMode).toHaveBeenCalledWith(mode)
  })

  it('picks a grid density and marks the current one pressed', async () => {
    const { handlers } = renderMenu({ gridCols: 5 })
    const user = await openMenu()
    expect(screen.getByRole('button', { name: '5' })).toHaveAttribute('aria-pressed', 'true')
    await user.click(screen.getByRole('button', { name: '8' }))
    expect(handlers.setGridCols).toHaveBeenCalledWith(8)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('shows density in compact mode but hides it and the folders toggle in list mode', async () => {
    const { unmount } = renderMenu({ viewMode: 'compact' })
    await openMenu()
    expect(screen.getByRole('button', { name: '3' })).toBeInTheDocument()
    expect(screen.queryByRole('menuitemradio', { name: /folders/i })).not.toBeInTheDocument()
    unmount()
    renderMenu({ viewMode: 'list' })
    await openMenu()
    expect(screen.queryByRole('button', { name: '3' })).not.toBeInTheDocument()
  })

  it('toggles compact folders with the inverse of the current value', async () => {
    const { handlers } = renderMenu({ foldersCompact: true })
    const user = await openMenu()
    const row = screen.getByRole('menuitemradio', { name: /expand folders/i })
    expect(row).toHaveAttribute('aria-checked', 'true')
    await user.click(row)
    expect(handlers.setFoldersCompact).toHaveBeenCalledWith(false)
  })

  it('toggles the theme', async () => {
    const { handlers } = renderMenu({ dark: true })
    const user = await openMenu()
    await user.click(screen.getByRole('menuitemradio', { name: /toggle theme/i }))
    expect(handlers.setDark).toHaveBeenCalledWith(false)
  })

  it('collapses the language sublist on a second click without closing the menu', async () => {
    renderMenu()
    const user = await openMenu()
    const lang = screen.getByRole('menuitemradio', { name: /language · en/i })
    await user.click(lang)
    expect(screen.getByRole('listbox')).toBeInTheDocument()
    await user.click(lang)
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(screen.getByRole('menu')).toBeInTheDocument()
  })

  it('closes on an outside mousedown but not on one inside', async () => {
    renderMenu()
    await openMenu()
    fireEvent.mouseDown(screen.getByRole('menu'))
    expect(screen.getByRole('menu')).toBeInTheDocument()
    fireEvent.mouseDown(document.body)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('closes on Escape and toggles from the trigger', async () => {
    renderMenu()
    const user = await openMenu()
    const trigger = screen.getByRole('button', { name: /more/i })
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    await user.keyboard('{Escape}')
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    await user.click(trigger)
    expect(screen.getByRole('menu')).toBeInTheDocument()
    await user.click(trigger)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })
})
