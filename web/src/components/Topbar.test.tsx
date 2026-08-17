import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Topbar } from './Topbar'
import { renderWithProviders } from '../test/renderWithProviders'
import { freshState, installAxiosMock } from '../test/server'
import * as webPushHooks from '../hooks/useWebPush'

const base = {
  view: 'home' as const,
  setView: vi.fn(),
  onHome: vi.fn(),
  onOpenMobileSidebar: vi.fn(),
  q: '',
  setQ: vi.fn(),
  onOpenPalette: vi.fn(),
  sort: 'created' as const,
  setSort: vi.fn(),
  viewMode: 'cards' as const,
  setViewMode: vi.fn(),
  gridCols: 5 as const,
  setGridCols: vi.fn(),
  foldersCompact: false,
  setFoldersCompact: vi.fn(),
  onNewLink: vi.fn(),
  onNewFolder: vi.fn(),
  onNewNote: vi.fn(),
  dark: false,
  setDark: vi.fn(),
  onOpenProfile: vi.fn(),
}

beforeEach(() => {
  vi.clearAllMocks()
  installAxiosMock(freshState())
  vi.spyOn(webPushHooks, 'useWebPush').mockReturnValue({
    data: { supported: false },
  } as unknown as ReturnType<typeof webPushHooks.useWebPush>)
  vi.spyOn(webPushHooks, 'useSubscribePush').mockReturnValue({
    mutate: vi.fn(), isPending: false,
  } as unknown as ReturnType<typeof webPushHooks.useSubscribePush>)
  vi.spyOn(webPushHooks, 'useUnsubscribePush').mockReturnValue({
    mutate: vi.fn(), isPending: false,
  } as unknown as ReturnType<typeof webPushHooks.useUnsubscribePush>)
})

describe('Topbar', () => {
  it('navigates home/stats/settings', async () => {
    const setView = vi.fn()
    const onHome = vi.fn()
    renderWithProviders(<Topbar {...base} setView={setView} onHome={onHome} />)
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/^home$/i))
    expect(onHome).toHaveBeenCalled()
    await user.click(screen.getByLabelText(/^stats$/i))
    expect(setView).toHaveBeenCalledWith('stats')
    await user.click(screen.getByLabelText(/settings/i))
    expect(setView).toHaveBeenCalledWith('settings')
  })

  // The settings hub consolidated those surfaces: import/export is a hub tile
  // and administration is the RBAC segment inside it — neither has a topbar
  // button anymore, for admins included.
  it('has no import or admin quicknav buttons', () => {
    renderWithProviders(<Topbar {...base} />)
    expect(screen.queryByLabelText(/import/i)).not.toBeInTheDocument()
    expect(screen.queryByLabelText(/^users$/i)).not.toBeInTheDocument()
  })

  it('exposes the user menu with the profile entry point', async () => {
    const onOpenProfile = vi.fn()
    renderWithProviders(<Topbar {...base} onOpenProfile={onOpenProfile} />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /account menu/i }))
    await user.click(screen.getByRole('menuitem', { name: /profile/i }))
    expect(onOpenProfile).toHaveBeenCalledTimes(1)
  })

  it('opens palette and updates search query', async () => {
    const onOpenPalette = vi.fn()
    const setQ = vi.fn()
    renderWithProviders(<Topbar {...base} onOpenPalette={onOpenPalette} setQ={setQ} q="hi" />)
    fireEvent.click(document.querySelector('.fx-search')!)
    expect(onOpenPalette).toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText(/^search$/i), { target: { value: 'abc' } })
    expect(setQ).toHaveBeenCalledWith('abc')
  })

  it('changes sort modes', async () => {
    const setSort = vi.fn()
    renderWithProviders(<Topbar {...base} setSort={setSort} />)
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/^Top$/i))
    expect(setSort).toHaveBeenCalledWith('clicks')
    await user.click(screen.getByLabelText(/^Recent$/i))
    expect(setSort).toHaveBeenCalledWith('recent')
    await user.click(screen.getByLabelText(/A→Z/i))
    expect(setSort).toHaveBeenCalledWith('alpha')
    await user.click(screen.getByLabelText(/Z→A/i))
    expect(setSort).toHaveBeenCalledWith('alpha_desc')
    await user.click(screen.getByLabelText(/^Newest$/i))
    expect(setSort).toHaveBeenCalledWith('created')
  })

  it('switches view modes, density and folders compact', async () => {
    const setViewMode = vi.fn()
    const setGridCols = vi.fn()
    const setFoldersCompact = vi.fn()
    renderWithProviders(
      <Topbar
        {...base}
        viewMode="cards"
        setViewMode={setViewMode}
        setGridCols={setGridCols}
        setFoldersCompact={setFoldersCompact}
      />,
    )
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/^Compact$/i))
    expect(setViewMode).toHaveBeenCalledWith('compact')
    await user.click(screen.getByLabelText(/^List$/i))
    expect(setViewMode).toHaveBeenCalledWith('list')
    await user.click(screen.getByLabelText(/^Cards$/i))
    expect(setViewMode).toHaveBeenCalledWith('cards')
    await user.click(screen.getByLabelText(/8 Density/i))
    expect(setGridCols).toHaveBeenCalledWith(8)
    await user.click(screen.getByLabelText(/Minimize folders/i))
    expect(setFoldersCompact).toHaveBeenCalledWith(true)
  })

  it('hides density and folders compact in list mode', () => {
    renderWithProviders(<Topbar {...base} viewMode="list" />)
    expect(screen.queryByLabelText(/3 Density/i)).toBeNull()
    expect(screen.queryByLabelText(/Minimize folders/i)).toBeNull()
  })

  it('toggles theme and fires new actions', async () => {
    const setDark = vi.fn()
    const onNewLink = vi.fn()
    const onNewFolder = vi.fn()
    const onNewNote = vi.fn()
    const onOpenMobileSidebar = vi.fn()
    renderWithProviders(
      <Topbar
        {...base}
        setDark={setDark}
        onNewLink={onNewLink}
        onNewFolder={onNewFolder}
        onNewNote={onNewNote}
        onOpenMobileSidebar={onOpenMobileSidebar}
        dark={false}
      />,
    )
    const user = userEvent.setup()
    await user.click(screen.getByLabelText(/theme/i))
    expect(setDark).toHaveBeenCalledWith(true)
    await user.click(screen.getByLabelText(/new link/i))
    expect(onNewLink).toHaveBeenCalled()
    await user.click(screen.getByLabelText(/new folder/i))
    expect(onNewFolder).toHaveBeenCalled()
    await user.click(screen.getByLabelText(/new note/i))
    expect(onNewNote).toHaveBeenCalled()
    await user.click(screen.getByLabelText(/expand/i))
    expect(onOpenMobileSidebar).toHaveBeenCalled()
  })

  it('shows expand folders label when already compact', () => {
    renderWithProviders(<Topbar {...base} viewMode="cards" foldersCompact />)
    expect(screen.getByLabelText(/Expand folders/i)).toBeInTheDocument()
  })
})
