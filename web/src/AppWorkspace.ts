import { useCallback, useState } from 'react'
import { useHotkeys } from 'react-hotkeys-hook'
import { useDarkMode } from './hooks/useDarkMode'
import { isBoolean, usePersistedState } from './hooks/usePersistedState'
import type { Sort } from './components/HomeView'

// 'admin' is gone as a view: the administration surface lives inside the
// settings hub (RBAC-scoped segment) instead of a topbar destination.
export type AppView = 'home' | 'import' | 'stats' | 'settings'
const SORTS: readonly Sort[] = ['created', 'clicks', 'recent', 'alpha', 'alpha_desc']

function isSort(value: unknown): value is Sort {
  return SORTS.includes(value as Sort)
}

function isGridDensity(value: unknown): value is 3 | 5 | 8 {
  return value === 3 || value === 5 || value === 8
}

export function useAppWorkspaceController() {
  const [view, _setView] = useState<AppView>('home')
  // Which hub section the settings page should land on, plus a counter so
  // clicking "Profile" twice remounts SettingsPage back on the section even
  // when the view is already 'settings' (the page owns its section state).
  const [settingsJump, setSettingsJump] = useState<{ section: string; n: number } | null>(null)
  const [selectedTags, setSelectedTags] = useState<number[]>([])
  const [q, setQ] = useState('')
  const [sort, setSort] = usePersistedState<Sort>('foldex.sort', 'created', isSort)
  const [dark, setDark] = useDarkMode()
  const [sidebarCollapsed, setSidebarCollapsed] = usePersistedState('foldex.sidebar.collapsed', false, isBoolean)
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false)
  const [gridCols, setGridCols] = usePersistedState<3 | 5 | 8>('foldex.grid.cols', 5, isGridDensity)
  const [paletteOpen, setPaletteOpen] = useState(false)

  const toggleSidebar = useCallback(() => setSidebarCollapsed((collapsed) => !collapsed), [setSidebarCollapsed])
  const openMobileSidebar = useCallback(() => setMobileSidebarOpen(true), [])
  const closeMobileSidebar = useCallback(() => setMobileSidebarOpen(false), [])
  const openPalette = useCallback(() => setPaletteOpen(true), [])
  const closePalette = useCallback(() => setPaletteOpen(false), [])
  // Deep link into a settings-hub section (used by the topbar user menu).
  const openSettingsAt = useCallback((section: string) => {
    setSettingsJump((prev) => ({ section, n: (prev?.n ?? 0) + 1 }))
    setView('settings')
  }, [])
  // The plain gear button must land on the OVERVIEW, not on whatever section
  // the last deep link targeted — wrapping setView clears the jump so the
  // hub's remount key falls back to 'overview'.
  const setView = useCallback((v: AppView) => {
    if (v === 'settings') setSettingsJump(null)
    _setView(v)
  }, [])
  const toggleTag = useCallback((id: number) => {
    setSelectedTags((selected) => (
      selected.includes(id) ? selected.filter((candidate) => candidate !== id) : [...selected, id]
    ))
    setMobileSidebarOpen(false)
  }, [])
  const clearTags = useCallback(() => {
    setSelectedTags([])
    setMobileSidebarOpen(false)
  }, [])

  useHotkeys('alt+k', (event) => {
    event.preventDefault()
    openPalette()
  })

  return {
    view,
    setView,
    settingsJump,
    openSettingsAt,
    selectedTags,
    q,
    setQ,
    sort,
    setSort,
    dark,
    setDark,
    sidebarCollapsed,
    toggleSidebar,
    mobileSidebarOpen,
    openMobileSidebar,
    closeMobileSidebar,
    gridCols,
    setGridCols,
    paletteOpen,
    openPalette,
    closePalette,
    toggleTag,
    clearTags,
  }
}

export type AppWorkspaceController = ReturnType<typeof useAppWorkspaceController>
