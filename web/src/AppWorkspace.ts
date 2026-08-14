import { useCallback, useState } from 'react'
import { useHotkeys } from 'react-hotkeys-hook'
import { useDarkMode } from './hooks/useDarkMode'
import { usePersistedState } from './hooks/usePersistedState'
import type { Sort } from './components/HomeView'

export type AppView = 'home' | 'import' | 'stats' | 'settings' | 'admin'
const SORTS: readonly Sort[] = ['created', 'clicks', 'recent', 'alpha', 'alpha_desc']

function isSort(value: unknown): value is Sort {
  return SORTS.includes(value as Sort)
}

function isGridDensity(value: unknown): value is 3 | 5 | 8 {
  return value === 3 || value === 5 || value === 8
}

export function useAppWorkspaceController() {
  const [view, setView] = useState<AppView>('home')
  const [selectedTags, setSelectedTags] = useState<number[]>([])
  const [q, setQ] = useState('')
  const [sort, setSort] = usePersistedState<Sort>('foldex.sort', 'created', isSort)
  const [dark, setDark] = useDarkMode()
  const [sidebarCollapsed, setSidebarCollapsed] = usePersistedState('foldex.sidebar.collapsed', false)
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false)
  const [gridCols, setGridCols] = usePersistedState<3 | 5 | 8>('foldex.grid.cols', 5, isGridDensity)
  const [paletteOpen, setPaletteOpen] = useState(false)

  const toggleSidebar = useCallback(() => setSidebarCollapsed((collapsed) => !collapsed), [setSidebarCollapsed])
  const openMobileSidebar = useCallback(() => setMobileSidebarOpen(true), [])
  const closeMobileSidebar = useCallback(() => setMobileSidebarOpen(false), [])
  const openPalette = useCallback(() => setPaletteOpen(true), [])
  const closePalette = useCallback(() => setPaletteOpen(false), [])
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
