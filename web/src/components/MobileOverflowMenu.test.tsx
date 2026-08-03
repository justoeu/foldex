import { describe, it, expect, vi, beforeEach } from 'vitest'
import { screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '../test/renderWithProviders'
import { MobileOverflowMenu } from './MobileOverflowMenu'
import i18n from '../i18n'

const base = {
  sort: 'created' as const,
  setSort: vi.fn(),
  viewMode: 'cards' as const,
  setViewMode: vi.fn(),
  gridCols: 5 as const,
  setGridCols: vi.fn(),
  foldersCompact: false,
  setFoldersCompact: vi.fn(),
  onNewFolder: vi.fn(),
  onNewLink: vi.fn(),
  onNewNote: vi.fn(),
  dark: false,
  setDark: vi.fn(),
  view: 'home' as const,
  setView: vi.fn(),
}

beforeEach(() => {
  vi.clearAllMocks()
  void i18n.changeLanguage('en')
})

async function openMenu() {
  fireEvent.click(screen.getByLabelText(/more/i))
  await screen.findByRole('menu')
}

describe('MobileOverflowMenu', () => {
  it('opens menu and triggers new link / folder / note', async () => {
    const onNewLink = vi.fn()
    const onNewFolder = vi.fn()
    const onNewNote = vi.fn()
    renderWithProviders(
      <MobileOverflowMenu
        {...base}
        onNewLink={onNewLink}
        onNewFolder={onNewFolder}
        onNewNote={onNewNote}
      />,
    )
    await openMenu()
    fireEvent.click(screen.getByText(/new link/i))
    expect(onNewLink).toHaveBeenCalled()

    fireEvent.click(screen.getByLabelText(/more/i))
    fireEvent.click(await screen.findByText(/new folder/i))
    expect(onNewFolder).toHaveBeenCalled()

    fireEvent.click(screen.getByLabelText(/more/i))
    fireEvent.click(await screen.findByText(/new note/i))
    expect(onNewNote).toHaveBeenCalled()
  })

  it('navigates to import and settings views', async () => {
    const setView = vi.fn()
    renderWithProviders(<MobileOverflowMenu {...base} setView={setView} />)
    await openMenu()
    fireEvent.click(screen.getByText(/import/i))
    expect(setView).toHaveBeenCalledWith('import')

    fireEvent.click(screen.getByLabelText(/more/i))
    fireEvent.click(await screen.findByText(/settings/i))
    expect(setView).toHaveBeenCalledWith('settings')
  })

  it('changes sort when option clicked', async () => {
    const setSort = vi.fn()
    renderWithProviders(<MobileOverflowMenu {...base} setSort={setSort} />)
    await openMenu()
    fireEvent.click(screen.getByText(/^Top$/i))
    expect(setSort).toHaveBeenCalledWith('clicks')

    fireEvent.click(screen.getByLabelText(/more/i))
    fireEvent.click(await screen.findByText(/^Recent$/i))
    expect(setSort).toHaveBeenCalledWith('recent')

    fireEvent.click(screen.getByLabelText(/more/i))
    fireEvent.click(await screen.findByText(/A→Z|A→Z|A→Z/i))
    expect(setSort).toHaveBeenCalledWith('alpha')

    fireEvent.click(screen.getByLabelText(/more/i))
    fireEvent.click(await screen.findByText(/Z→A/i))
    expect(setSort).toHaveBeenCalledWith('alpha_desc')

    fireEvent.click(screen.getByLabelText(/more/i))
    fireEvent.click(await screen.findByText(/^Newest$/i))
    expect(setSort).toHaveBeenCalledWith('created')
  })

  it('switches view modes and density when cards/compact', async () => {
    const setViewMode = vi.fn()
    const setGridCols = vi.fn()
    renderWithProviders(
      <MobileOverflowMenu
        {...base}
        viewMode="cards"
        setViewMode={setViewMode}
        setGridCols={setGridCols}
      />,
    )
    await openMenu()
    fireEvent.click(screen.getByText(/^Compact$/i))
    expect(setViewMode).toHaveBeenCalledWith('compact')

    fireEvent.click(screen.getByLabelText(/more/i))
    fireEvent.click(await screen.findByText(/^List$/i))
    expect(setViewMode).toHaveBeenCalledWith('list')

    fireEvent.click(screen.getByLabelText(/more/i))
    fireEvent.click(await screen.findByText(/^Cards$/i))
    expect(setViewMode).toHaveBeenCalledWith('cards')

    // density buttons visible in cards mode
    fireEvent.click(screen.getByLabelText(/more/i))
    const dens8 = await screen.findByRole('button', { name: '8' })
    fireEvent.click(dens8)
    expect(setGridCols).toHaveBeenCalledWith(8)
  })

  it('hides density when viewMode is list', async () => {
    renderWithProviders(<MobileOverflowMenu {...base} viewMode="list" />)
    await openMenu()
    expect(screen.queryByRole('button', { name: '3' })).toBeNull()
    expect(screen.queryByRole('button', { name: '5' })).toBeNull()
  })

  it('toggles folders compact only in cards mode', async () => {
    const setFoldersCompact = vi.fn()
    renderWithProviders(
      <MobileOverflowMenu
        {...base}
        viewMode="cards"
        foldersCompact={false}
        setFoldersCompact={setFoldersCompact}
      />,
    )
    await openMenu()
    fireEvent.click(screen.getByText(/Minimize folders/i))
    expect(setFoldersCompact).toHaveBeenCalledWith(true)
  })

  it('toggles theme', async () => {
    const setDark = vi.fn()
    renderWithProviders(<MobileOverflowMenu {...base} dark={false} setDark={setDark} />)
    await openMenu()
    fireEvent.click(screen.getByText(/theme|dark|light/i))
    expect(setDark).toHaveBeenCalledWith(true)
  })

  it('opens language sublist and picks a locale', async () => {
    const user = userEvent.setup()
    renderWithProviders(<MobileOverflowMenu {...base} />)
    await openMenu()
    await user.click(screen.getByText(/language/i))
    const listbox = await screen.findByRole('listbox')
    expect(listbox).toBeInTheDocument()
    const opts = screen.getAllByRole('option')
    expect(opts.length).toBeGreaterThanOrEqual(2)
    await user.click(opts.find((o) => o.textContent?.includes('PT') || o.textContent?.includes('Português')) ?? opts[1])
    await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())
  })

  it('closes on Escape', async () => {
    renderWithProviders(<MobileOverflowMenu {...base} />)
    await openMenu()
    fireEvent.keyDown(window, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())
    expect(screen.getByLabelText(/more/i)).toBeInTheDocument()
  })

  it('closes on outside mousedown', async () => {
    renderWithProviders(
      <div>
        <button type="button">outside</button>
        <MobileOverflowMenu {...base} />
      </div>,
    )
    await openMenu()
    fireEvent.mouseDown(screen.getByText('outside'))
    await waitFor(() => expect(screen.queryByRole('menu')).toBeNull())
  })

  it('marks active sort/view rows', async () => {
    renderWithProviders(
      <MobileOverflowMenu {...base} sort="clicks" viewMode="compact" view="import" />,
    )
    await openMenu()
    const top = screen.getByText(/^Top$/i).closest('button')
    expect(top).toHaveAttribute('aria-checked', 'true')
    const compact = screen.getByText(/^Compact$/i).closest('button')
    expect(compact).toHaveAttribute('aria-checked', 'true')
  })
})
