import { useCallback, useEffect, useRef, useState } from 'react'
import { usePersistedMap } from './hooks/usePersistedState'
import { usePasswordPrompt, type FolderUnlock } from './components/PasswordPromptDialog'
import type { Folder } from './api/types'
import type { ViewMode } from './components/HomeView'

type UnlockMap = Record<number, FolderUnlock>
const VIEW_MODES: readonly ViewMode[] = ['cards', 'compact', 'list']

function isViewMode(value: unknown): value is ViewMode {
  return VIEW_MODES.includes(value as ViewMode)
}

type FolderError = {
  response?: {
    status?: number
    data?: { error?: { code?: string } }
  }
}

export function validUnlockToken(
  unlocked: FolderUnlock | undefined,
  now = Date.now(),
): string | undefined {
  return unlocked && unlocked.expiresAt > now ? unlocked.token : undefined
}

export function pruneFolderPath(path: number[], validIds: Set<number>): number[] {
  const firstInvalid = path.findIndex((id) => !validIds.has(id))
  return firstInvalid === -1 ? path : path.slice(0, firstInvalid)
}

export function pruneFolderContextMap<T>(
  map: Record<string, T>,
  validIds: Set<number>,
): Record<string, T> {
  let changed = false
  const next: Record<string, T> = {}
  for (const [key, value] of Object.entries(map)) {
    const match = key.match(/^folder\.(\d+)$/)
    if (match && !validIds.has(Number(match[1]))) {
      changed = true
      continue
    }
    next[key] = value
  }
  return changed ? next : map
}

export function pruneFolderUnlocks(unlocks: UnlockMap, validIds: Set<number>): UnlockMap {
  let changed = false
  const next: UnlockMap = {}
  for (const [rawId, unlock] of Object.entries(unlocks)) {
    const id = Number(rawId)
    if (!validIds.has(id)) {
      changed = true
      continue
    }
    next[id] = unlock
  }
  return changed ? next : unlocks
}

export function isFolderLockedError(error: unknown): boolean {
  const candidate = error as FolderError | null
  return candidate?.response?.status === 403 &&
    candidate.response.data?.error?.code === 'folder_locked'
}

export function useAppNavigationController(allFolders: Folder[] | undefined) {
  const [folderPath, setFolderPath] = useState<number[]>([])
  const [unlockedFolders, setUnlockedFolders] = useState<UnlockMap>({})
  const unlockedFoldersRef = useRef(unlockedFolders)
  const passwordPrompt = usePasswordPrompt()
  const viewModes = usePersistedMap<ViewMode>('foldex.viewMode.map', 'cards', isViewMode)
  const compactFolders = usePersistedMap<boolean>('foldex.foldersCompact.map', false)
  const openFolder = folderPath.at(-1) ?? null
  const viewModeKey = openFolder === null ? 'home' : `folder.${openFolder}`

  useEffect(() => {
    unlockedFoldersRef.current = unlockedFolders
  }, [unlockedFolders])

  useEffect(() => {
    if (typeof window === 'undefined') return
    const url = new URL(window.location.href)
    if (!url.searchParams.has('folder')) return
    url.searchParams.delete('folder')
    window.history.replaceState({}, '', url.toString())
  }, [])

  useEffect(() => {
    if (!allFolders) return
    const validIds = new Set(allFolders.map((folder) => folder.id))
    setFolderPath((path) => pruneFolderPath(path, validIds))
    viewModes.setAll((map) => pruneFolderContextMap(map, validIds))
    compactFolders.setAll((map) => pruneFolderContextMap(map, validIds))
    setUnlockedFolders((unlocks) => {
      const next = pruneFolderUnlocks(unlocks, validIds)
      unlockedFoldersRef.current = next
      return next
    })
  }, [allFolders, compactFolders.setAll, viewModes.setAll])

  const openFolderDirectly = useCallback((id: number) => {
    setFolderPath((path) => [...path, id])
  }, [])
  const goHome = useCallback(() => setFolderPath([]), [])
  const navigateBack = useCallback(() => setFolderPath((path) => path.slice(0, -1)), [])

  const rememberFolderUnlock = useCallback((id: number, result: FolderUnlock) => {
    setUnlockedFolders((unlocks) => {
      const next = { ...unlocks, [id]: result }
      unlockedFoldersRef.current = next
      return next
    })
  }, [])

  const forgetFolderUnlock = useCallback((id: number) => {
    setUnlockedFolders((unlocks) => {
      if (!(id in unlocks)) return unlocks
      const next = { ...unlocks }
      delete next[id]
      unlockedFoldersRef.current = next
      return next
    })
  }, [])

  const requestOpenFolder = useCallback(async (id: number) => {
    const folder = allFolders?.find((candidate) => candidate.id === id)
    const token = validUnlockToken(unlockedFoldersRef.current[id])
    if (!folder?.has_password || token) {
      openFolderDirectly(id)
      return
    }
    const result = await passwordPrompt(folder)
    if (!result) return
    rememberFolderUnlock(id, result)
    openFolderDirectly(id)
  }, [allFolders, openFolderDirectly, passwordPrompt, rememberFolderUnlock])

  const unlockTokenFor = useCallback((id: number | null) => (
    id === null ? undefined : validUnlockToken(unlockedFolders[id])
  ), [unlockedFolders])

  return {
    openFolder,
    goHome,
    navigateBack,
    requestOpenFolder,
    rememberFolderUnlock,
    forgetFolderUnlock,
    unlockTokenFor,
    currentUnlockToken: unlockTokenFor(openFolder),
    viewMode: viewModes.get(viewModeKey),
    setViewMode: (mode: ViewMode) => viewModes.set(viewModeKey, mode),
    foldersCompact: compactFolders.get(viewModeKey),
    setFoldersCompact: (compact: boolean) => compactFolders.set(viewModeKey, compact),
  }
}

export type AppNavigationController = ReturnType<typeof useAppNavigationController>

export function useFolderLockRecovery({
  entriesError,
  foldersError,
  navigation,
}: {
  entriesError: unknown
  foldersError: unknown
  navigation: AppNavigationController
}) {
  useEffect(() => {
    if (navigation.openFolder === null) return
    if (!isFolderLockedError(entriesError) && !isFolderLockedError(foldersError)) return
    navigation.forgetFolderUnlock(navigation.openFolder)
    navigation.navigateBack()
  }, [entriesError, foldersError, navigation.forgetFolderUnlock, navigation.navigateBack, navigation.openFolder])
}
