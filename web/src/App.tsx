import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import { useHotkeys } from 'react-hotkeys-hook'
import { usePasteUrl } from './hooks/usePasteUrl'
import { usePersistedState, usePersistedMap } from './hooks/usePersistedState'
import { useDarkMode } from './hooks/useDarkMode'
import { useTranslation } from 'react-i18next'
import './styles/foldex.css'
import './styles/overrides.css'

import { Icon, I } from './components/icons'
import { TagSidebar } from './components/TagSidebar'
import { Topbar } from './components/Topbar'
import { LinkDialog } from './components/LinkDialog'
import { FolderDialog } from './components/FolderDialog'
import { CommandPalette } from './components/CommandPalette'
import { TooltipPortal } from './components/TooltipPortal'
import { Home, type Sort, type ViewMode } from './components/HomeView'
// Code-split the two off-hot-path views. Home is by far the most-visited
// view; lazy-loading ImportPage + StatsPage trims the initial JS bundle by
// the chart code, backup card, and dialog plumbing they pull in. The Suspense
// boundary below renders a tiny fallback while the chunk loads.
const ImportPage = lazy(() => import('./pages/ImportPage').then((m) => ({ default: m.ImportPage })))
const StatsPage = lazy(() => import('./pages/StatsPage').then((m) => ({ default: m.StatsPage })))
const SettingsPage = lazy(() => import('./pages/SettingsPage').then((m) => ({ default: m.SettingsPage })))
// NoteDialog pulls in Tiptap/ProseMirror (~140 KB gzip) — unlike LinkDialog/
// FolderDialog it's lazy-loaded so that weight only ships once a user
// actually opens a note, not on every visit to the app.
const NoteDialog = lazy(() => import('./components/NoteDialog').then((m) => ({ default: m.NoteDialog })))
import { useUpdateLink } from './api/links'
import { flattenEntries, useEntries } from './api/entries'
import { useUpdateNote } from './api/notes'
import { useTags } from './api/tags'
import { useFolders, useCreateFolder, useUpdateFolder } from './api/folders'
import { usePasswordPrompt } from './components/PasswordPromptDialog'
import type { Link as LinkT, Folder as FolderT, Entry, MergeSource } from './api/types'

type View = 'home' | 'import' | 'stats' | 'settings'

// Shared expiry check for a folder's cached unlock — used both when
// deciding whether to skip the password prompt and when deciding whether to
// attach the token header to a query, so the two can't drift (previously
// only the prompt-skip path checked expiresAt).
function validUnlockToken(unlocked: { token: string; expiresAt: number } | undefined): string | undefined {
  return unlocked && unlocked.expiresAt > Date.now() ? unlocked.token : undefined
}

export default function App() {
  const { t } = useTranslation()
  const [view, setView] = useState<View>('home')
  const [selectedTags, setSelectedTags] = useState<number[]>([])
  const [q, setQ] = useState('')
  const [sort, setSort] = useState<Sort>('created')
  // viewMode is per-context (home vs each folder) — a Record mapped by
  // `home` or `folder.<id>`. Persisted under `foldex.viewMode.map`. Default
  // is 'cards' for any context without a saved choice.
  // viewModeMap is per-context (home vs each folder), persisted under
  // `foldex.viewMode.map`. Default is 'cards' for any context without a save.
  const viewModeMap = usePersistedMap<ViewMode>('foldex.viewMode.map', 'cards')
  // foldersCompact is per-context (home vs each folder). When true the
  // FolderCard hides its 2x2 preview and enables the RapidView popover.
  const foldersCompactMap = usePersistedMap<boolean>('foldex.foldersCompact.map', false)
  const [linkDialogOpen, setLinkDialogOpen] = useState(false)
  const [editLink, setEditLink] = useState<LinkT | null>(null)
  // Carries a URL the user pasted onto the page so LinkDialog can mount
  // with it pre-filled. Cleared on close so subsequent manual "New link"
  // clicks start empty.
  const [pastedUrl, setPastedUrl] = useState<string | undefined>(undefined)
  const [folderDialogOpen, setFolderDialogOpen] = useState(false)
  const [editFolder, setEditFolder] = useState<FolderT | null>(null)
  // Distinguishes "just-merged" naming flow from normal edit. When true the
  // FolderDialog hides destructive actions and shows naming copy.
  const [folderJustCreated, setFolderJustCreated] = useState(false)
  const [noteDialogOpen, setNoteDialogOpen] = useState(false)
  const [editNoteId, setEditNoteId] = useState<number | null>(null)
  const [paletteOpen, setPaletteOpen] = useState(false)
  // Dark mode lives in a hook so main.tsx can run it ABOVE the auth gate —
  // otherwise a dark-mode user sees a white login screen (see useDarkMode).
  const [dark, setDark] = useDarkMode()
  const [sidebarCollapsed, setSidebarCollapsed] = usePersistedState('foldex.sidebar.collapsed', false)
  // Drawer-style sidebar on mobile (≤768px). Stays in-memory only — phone
  // users almost never want it open by default after navigation. The
  // toggle button on the topbar flips this, and tapping the backdrop or
  // pressing Esc closes it.
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false)
  const [gridCols, setGridCols] = usePersistedState<3 | 5 | 8>('foldex.grid.cols', 5)
  // Folder navigation is a stack of ids — last entry is the currently open
  // folder; each push enters a child, each pop goes back one level. Lives
  // purely in-memory (no URL exposure: internal IDs shouldn't bleed into the
  // address bar). `openFolder` is just the top of the stack.
  const [folderPath, setFolderPath] = useState<number[]>([])
  const openFolder = folderPath.at(-1) ?? null
  // Functional setState so these stay referentially stable across renders —
  // they're threaded down to memoized cards, where a fresh reference every
  // render would defeat the React.memo shallow compare.
  const setOpenFolder = useCallback((id: number | null) => {
    setFolderPath((prev) => (id == null ? [] : [...prev, id]))
  }, [])
  const navigateBack = useCallback(() => setFolderPath((prev) => prev.slice(0, -1)), [])
  // Proof-of-password for protected folders (ADR-28), keyed by folder id.
  // Deliberately in-memory only (no localStorage) — unlock state resets on
  // page reload, same "purely in-memory" precedent as folderPath itself.
  const [unlockedFolders, setUnlockedFolders] = useState<Record<number, { token: string; expiresAt: number }>>({})
  // Mirrors unlockedFolders for requestOpenFolder's read below. A plain
  // dependency on the state itself would recreate that useCallback on every
  // unlock (a new object each time), churning identity through to every
  // memoized FolderCard/FolderRow/CompactFolder on screen via the shared
  // onOpenFolder prop — negligible today (unlocks are rare), but decoupling
  // the read from the callback's identity is cheap insurance against that
  // regression class (CLAUDE.md §5) if unlock ever became more frequent.
  const unlockedFoldersRef = useRef(unlockedFolders)
  // Keep ref in sync for any external setState path; requestOpenFolder also
  // writes the ref synchronously inside the updater (RACE-HER-011) so a
  // double-click before paint cannot re-prompt.
  useEffect(() => {
    unlockedFoldersRef.current = unlockedFolders
  }, [unlockedFolders])
  const passwordPrompt = usePasswordPrompt()


  // Derive the active viewMode + setter from openFolder. Home and each folder
  // get their own slot in the map; switching context surfaces the saved choice.
  const viewModeKey = openFolder !== null ? `folder.${openFolder}` : 'home'
  const viewMode: ViewMode = viewModeMap.get(viewModeKey)
  const setViewMode = (m: ViewMode) => viewModeMap.set(viewModeKey, m)
  const foldersCompact: boolean = foldersCompactMap.get(viewModeKey)
  const setFoldersCompact = (v: boolean) => foldersCompactMap.set(viewModeKey, v)

  // Strip any stale `?folder=N` left over from a previous URL-bookmarked
  // session — internal IDs no longer belong in the address bar.
  useEffect(() => {
    if (typeof window === 'undefined') return
    const url = new URL(window.location.href)
    if (url.searchParams.has('folder')) {
      url.searchParams.delete('folder')
      window.history.replaceState({}, '', url.toString())
    }
  }, [])

  // Tag filter and folder scope compose via AND on the backend — selecting a
  // tag inside a folder narrows that folder's entries by tag. Home view
  // always shows ungrouped entries (folder cards represent the rest).
  // useEntries (GET /api/entries) replaces useLinks as the grid's data
  // source — one paginated, sorted, searched stream spanning both links and
  // notes instead of merging two independently-paginated queries client-side
  // (see ADR-27 in docs/ARCHITECTURE.md).
  // Unlock token for the currently-open folder, if any — attached as a
  // header on the two content-gated queries below (ADR-28). Undefined for
  // an unprotected folder (no header sent, backend never gates it) and for
  // Home itself (openFolder === null, root/ungrouped are never gated).
  const currentUnlockToken = openFolder !== null ? validUnlockToken(unlockedFolders[openFolder]) : undefined
  const entries = useEntries({
    q,
    tagIds: selectedTags,
    sort,
    unlockToken: currentUnlockToken,
    ...(openFolder !== null ? { folderId: openFolder } : { ungrouped: true }),
  })
  // Folder list scope:
  //   home (no openFolder)        → only root folders ({scope: 'root'})
  //   inside folder (openFolder)  → only direct children ({scope: openFolder})
  // LinkDialog still loads the full flat list via a separate hook call so the
  // folder picker can target anything regardless of position.
  const folders = useFolders({ scope: openFolder === null ? 'root' : openFolder, unlockToken: currentUnlockToken })
  // Flat metadata only (has_password, tree) — slim projection, no RapidView
  // previews (N1-NEX-006 / N1-NEX-011). Scoped `folders` keeps full previews.
  const allFolders = useFolders({ scope: null, fields: 'minimal' })
  const { data: allTags = [] } = useTags()

  // Centralized "open a folder" gate (ADR-28) — the single chokepoint every
  // card kind, view (cards/list/compact), and the Command Palette route
  // through instead of calling setOpenFolder directly. Looks the target up
  // in allFolders (the full flat list, guaranteed to be a superset of
  // whatever scoped `folders` list the caller rendered from) rather than
  // requiring every call site to pass the full Folder object.
  const requestOpenFolder = useCallback(async (id: number) => {
    const folder = allFolders.data?.find((f) => f.id === id)
    const isUnlocked = !!validUnlockToken(unlockedFoldersRef.current[id])
    if (!folder?.has_password || isUnlocked) {
      setOpenFolder(id)
      return
    }
    const result = await passwordPrompt(folder)
    if (result) {
      setUnlockedFolders((prev) => {
        const next = { ...prev, [id]: result }
        unlockedFoldersRef.current = next
        return next
      })
      setOpenFolder(id)
    }
  }, [allFolders.data, passwordPrompt, setOpenFolder])

  // Defensive 403 handling: a token can go stale mid-session (password
  // changed/removed in another tab). If the currently-open folder's gated
  // query comes back locked, drop the stale entry and bail out to the
  // parent — re-entering will correctly re-prompt via requestOpenFolder.
  useEffect(() => {
    if (openFolder === null) return
    const err = (entries.error ?? folders.error) as
      | { response?: { status?: number; data?: { error?: { code?: string } } } }
      | null
    if (err?.response?.status !== 403 || err.response?.data?.error?.code !== 'folder_locked') return
    setUnlockedFolders((prev) => {
      if (!(openFolder in prev)) return prev
      const next = { ...prev }
      delete next[openFolder]
      unlockedFoldersRef.current = next
      return next
    })
    navigateBack()
  }, [entries.error, folders.error, openFolder, navigateBack])

  // Self-healing folder navigation: if a folder in the current path no longer
  // exists (e.g. user deleted it from the dialog while inside it), prune it.
  // Effect: deleting "the folder you're inside" automatically pops the stack
  // back to the deepest still-valid ancestor (or home if all gone).
  //
  // Same pass also prunes orphan `folder.<id>` keys from viewModeMap so the
  // localStorage entry doesn't grow monotonically over the app's lifetime.
  const allFoldersData = allFolders.data
  useEffect(() => {
    if (!allFoldersData) return
    const validIds = new Set(allFoldersData.map((f) => f.id))

    if (folderPath.length > 0) {
      const trimmed: number[] = []
      for (const id of folderPath) {
        if (!validIds.has(id)) break
        trimmed.push(id)
      }
      if (trimmed.length !== folderPath.length) {
        setFolderPath(trimmed)
      }
    }

    viewModeMap.setAll((prev) => {
      let mutated = false
      const next: Record<string, ViewMode> = {}
      for (const [key, val] of Object.entries(prev)) {
        if (key === 'home') {
          next[key] = val
          continue
        }
        const m = key.match(/^folder\.(\d+)$/)
        if (m && !validIds.has(Number(m[1]))) {
          mutated = true
          continue
        }
        next[key] = val
      }
      return mutated ? next : prev
    })

    foldersCompactMap.setAll((prev) => {
      let mutated = false
      const next: Record<string, boolean> = {}
      for (const [key, val] of Object.entries(prev)) {
        if (key === 'home') {
          next[key] = val
          continue
        }
        const m = key.match(/^folder\.(\d+)$/)
        if (m && !validIds.has(Number(m[1]))) {
          mutated = true
          continue
        }
        next[key] = val
      }
      return mutated ? next : prev
    })
  }, [allFoldersData, folderPath])
  const updateLink = useUpdateLink()
  const updateNote = useUpdateNote()
  const createFolder = useCreateFolder()
  const updateFolder = useUpdateFolder()

  // Drag-and-drop handlers wired down to FolderCard / LinkCard / NoteCard.
  //
  // Move: PATCH the dragged entry with the target folder's id.
  // Merge: when two cards collide (link↔link, link↔note, note↔note), create
  //   a fresh folder ("Nova pasta") and PATCH both entries into it; open the
  //   FolderDialog in edit mode so the user can immediately rename it.
  //   Sequential calls; race-tolerant for a single-user local app.
  const onMoveLinkToFolder = useCallback((linkId: number, folderId: number) => {
    updateLink.mutate({ id: linkId, body: { folder_id: folderId } })
  }, [updateLink.mutate])
  const onMoveNoteToFolder = useCallback((noteId: number, folderId: number) => {
    updateNote.mutate({ id: noteId, body: { folder_id: folderId } })
  }, [updateNote.mutate])
  // Move folder `sourceId` to be a child of `targetId`. Refuses the move when
  // the target is `sourceId` itself or sits inside `sourceId`'s subtree —
  // that would create a cycle (A → B → A). The backend has its own guard
  // too, but checking client-side keeps the UI snappy and avoids a roundtrip
  // for the obvious bad cases.
  const onMoveFolder = useCallback((sourceId: number, targetId: number) => {
    if (sourceId === targetId) return
    const all = allFolders.data ?? []
    // Walk descendants of sourceId; if we find targetId, the move would
    // create a cycle — bail.
    const childrenOf = (id: number) => all.filter((f) => f.parent_id === id)
    const stack = [sourceId]
    const seen = new Set<number>()
    while (stack.length) {
      const cur = stack.pop() as number
      if (seen.has(cur)) continue
      seen.add(cur)
      if (cur === targetId && cur !== sourceId) return
      for (const c of childrenOf(cur)) {
        if (c.id === targetId) return
        stack.push(c.id)
      }
    }
    updateFolder.mutate({ id: sourceId, body: { parent_id: targetId } })
  }, [allFolders.data, updateFolder.mutate])
  const moveEntryToFolder = useCallback((source: MergeSource, folderId: number) => (
    source.kind === 'link'
      ? updateLink.mutateAsync({ id: source.id, body: { folder_id: folderId } })
      : updateNote.mutateAsync({ id: source.id, body: { folder_id: folderId } })
  ), [updateLink.mutateAsync, updateNote.mutateAsync])
  const onMergeEntries = useCallback(async (a: MergeSource, b: MergeSource) => {
    if (a.kind === b.kind && a.id === b.id) return
    try {
      // If we're already inside a folder, the merged-pair lives under it
      // (subfolder); otherwise it's a root folder.
      const f = await createFolder.mutateAsync({ name: t('home.merge_new_folder_name'), parent_id: openFolder ?? null })
      await Promise.all([moveEntryToFolder(a, f.id), moveEntryToFolder(b, f.id)])
      setEditFolder(f)
      setFolderJustCreated(true)
      setFolderDialogOpen(true)
    } catch {
      // Mutation errors surface via toast/console; non-fatal here.
    }
  }, [createFolder.mutateAsync, moveEntryToFolder, openFolder, t])

  // Stable across renders so the memoized cards they're threaded into don't
  // re-render on every unrelated App state change (search keystroke, sidebar
  // toggle, background refetch).
  const handleEditLink = useCallback((l: LinkT) => {
    setEditLink(l)
    setLinkDialogOpen(true)
  }, [])
  const handleEditNote = useCallback((id: number) => {
    setEditNoteId(id)
    setNoteDialogOpen(true)
  }, [])
  const handleEditFolder = useCallback((f: FolderT) => {
    setEditFolder(f)
    setFolderJustCreated(false)
    setFolderDialogOpen(true)
  }, [])
  // Settings page hands back a folder id after a master-password reset so the
  // user can immediately set a fresh password — resolve it from the flat list.
  const handleEditFolderById = useCallback((id: number) => {
    const f = allFolders.data?.find((x) => x.id === id)
    if (f) handleEditFolder(f)
  }, [allFolders.data, handleEditFolder])

  const totalLinks = useMemo(
    () => allTags.reduce((acc, t) => acc + (t.link_count ?? 0), 0),
    [allTags],
  )

  // All shortcuts are Alt-based for consistency. ⌘K conflicts with the
  // browser's URL-bar focus on some configurations; ⌘N/⌘P are hard-claimed
  // by the browser ("New window" / "Print"). Alt-based shortcuts pass
  // through to the SPA cleanly.
  useHotkeys('alt+k', (e) => {
    e.preventDefault()
    setPaletteOpen(true)
  })
  useHotkeys('alt+n', (e) => {
    e.preventDefault()
    setEditLink(null)
    setPastedUrl(undefined)
    setLinkDialogOpen(true)
  })

  // Paste a URL anywhere on the page → New Link dialog opens with the
  // URL pre-filled. No-ops when typing in a field, when any dialog is
  // already up, or when the clipboard isn't URL-shaped.
  const onPastedUrl = useCallback((url: string) => {
    setEditLink(null)
    setPastedUrl(url)
    setLinkDialogOpen(true)
  }, [])
  usePasteUrl(onPastedUrl)
  // ⌥F — Nova pasta ("F" for Folder). ⌥P collided with other key handlers.
  useHotkeys('alt+f', (e) => {
    e.preventDefault()
    setEditFolder(null)
    setFolderJustCreated(false)
    setFolderDialogOpen(true)
  })
  // ⌥M — Nova nota ("M" for Note — kept the Alt-based convention from
  // ⌥N/⌥F; ⌘M is browser-minimize on macOS, never reaches the SPA).
  useHotkeys('alt+m', (e) => {
    e.preventDefault()
    setEditNoteId(null)
    setNoteDialogOpen(true)
  })

  return (
    <div className={'fx-shell' + (dark ? ' fx-dark-shell' : '')}>
      <div className="fx-aurora" aria-hidden="true">
        <div className="fx-aurora-blob fx-aurora-a" />
        <div className="fx-aurora-blob fx-aurora-b" />
        <div className="fx-aurora-blob fx-aurora-c" />
        <div className="fx-aurora-blob fx-aurora-d" />
        <div className="fx-aurora-grain" />
      </div>

      <div
        className={'fx-frame' + (mobileSidebarOpen ? ' fx-frame-mobile-drawer-open' : '')}
        style={{ '--fx-sidebar-w': sidebarCollapsed ? '64px' : '252px' } as CSSProperties}
      >
        {mobileSidebarOpen && (
          <div
            className="fx-mobile-backdrop"
            aria-hidden="true"
            onClick={() => setMobileSidebarOpen(false)}
          />
        )}
        <TagSidebar
          collapsed={sidebarCollapsed}
          onToggleCollapsed={() => setSidebarCollapsed((v) => !v)}
          mobileOpen={mobileSidebarOpen}
          onMobileClose={() => setMobileSidebarOpen(false)}
          selected={selectedTags}
          onToggle={(id) => {
            setSelectedTags((prev) =>
              prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id],
            )
            setMobileSidebarOpen(false) // collapse drawer after a tap on mobile
          }}
          onClear={() => {
            setSelectedTags([])
            setMobileSidebarOpen(false)
          }}
          totalLinks={Math.max(totalLinks, flattenEntries(entries.data).length)}
        />

        <main className="fx-main">
          <Topbar
            view={view}
            setView={setView}
            onOpenMobileSidebar={() => setMobileSidebarOpen(true)}
            onHome={() => {
              setView('home')
              setOpenFolder(null)
            }}
            q={q}
            setQ={setQ}
            onOpenPalette={() => setPaletteOpen(true)}
            sort={sort}
            setSort={setSort}
            viewMode={viewMode}
            setViewMode={setViewMode}
            gridCols={gridCols}
            setGridCols={setGridCols}
            foldersCompact={foldersCompact}
            setFoldersCompact={setFoldersCompact}
            onNewLink={() => {
              setEditLink(null)
              setLinkDialogOpen(true)
            }}
            onNewFolder={() => {
              setEditFolder(null)
              setFolderJustCreated(false)
              setFolderDialogOpen(true)
            }}
            onNewNote={() => {
              setEditNoteId(null)
              setNoteDialogOpen(true)
            }}
            dark={dark}
            setDark={setDark}
          />

          {view === 'home' && (
            <Home
              entries={flattenEntries(entries.data)}
              folders={folders.data ?? []}
              allFolders={allFolders.data ?? []}
              openFolder={openFolder}
              onOpenFolder={requestOpenFolder}
              onNavigateBack={navigateBack}
              isLoading={entries.isLoading}
              onEdit={handleEditLink}
              onEditNote={handleEditNote}
              onEditFolder={handleEditFolder}
              onNewLink={() => {
                setEditLink(null)
                setLinkDialogOpen(true)
              }}
              onImport={() => setView('import')}
              viewMode={viewMode}
              gridCols={gridCols}
              foldersCompact={foldersCompact}
              sort={sort}
              onReload={() => {
                entries.refetch()
                folders.refetch()
                allFolders.refetch()
              }}
              reloading={entries.isFetching || folders.isFetching || allFolders.isFetching}
              hasMoreLinks={entries.hasNextPage === true}
              loadingMoreLinks={entries.isFetchingNextPage}
              onLoadMoreLinks={() => entries.fetchNextPage()}
              onMoveLinkToFolder={onMoveLinkToFolder}
              onMoveNoteToFolder={onMoveNoteToFolder}
              onMergeEntries={onMergeEntries}
              onMoveFolder={onMoveFolder}
            />
          )}
          {view === 'import' && (
            <div className="fx-mainarea">
              <Suspense fallback={<div className="fx-empty">…</div>}>
                <ImportPage onDone={() => setView('home')} />
              </Suspense>
            </div>
          )}
          {view === 'stats' && (
            <div className="fx-mainarea">
              <Suspense fallback={<div className="fx-empty">…</div>}>
                <StatsPage />
              </Suspense>
            </div>
          )}
          {view === 'settings' && (
            <div className="fx-mainarea">
              <Suspense fallback={<div className="fx-empty">…</div>}>
                <SettingsPage onEditFolder={handleEditFolderById} />
              </Suspense>
            </div>
          )}

          {/* FAB — only visible on mobile (CSS-gated). Anchors to the
              bottom-right safe area so it doesn't fight an open keyboard.
              Single primary action (new link); secondary actions live
              in the topbar overflow menu / hamburger sidebar. */}
          <button
            type="button"
            className="fx-fab"
            aria-label={t('topbar.new_link')}
            data-tooltip={t('topbar.new_link')}
            onClick={() => {
              setEditLink(null)
              setLinkDialogOpen(true)
            }}
          >
            <Icon d={I.plus} size={22} stroke={2.4} />
          </button>
        </main>
      </div>

      <LinkDialog
        open={linkDialogOpen}
        link={editLink}
        initialUrl={pastedUrl}
        defaultFolderId={openFolder}
        onClose={() => {
          setLinkDialogOpen(false)
          setPastedUrl(undefined)
        }}
      />
      <FolderDialog
        open={folderDialogOpen}
        folder={editFolder}
        justCreated={folderJustCreated}
        parentId={editFolder ? null : openFolder}
        onClose={() => {
          setFolderDialogOpen(false)
          setEditFolder(null)
          setFolderJustCreated(false)
        }}
      />
      {noteDialogOpen && (
        <Suspense fallback={<div className="fx-overlay fx-overlay-modal" />}>
          <NoteDialog
            open={noteDialogOpen}
            noteId={editNoteId}
            defaultFolderId={openFolder}
            onClose={() => {
              setNoteDialogOpen(false)
              setEditNoteId(null)
            }}
          />
        </Suspense>
      )}
      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        onOpenFolder={(id) => {
          setPaletteOpen(false)
          void requestOpenFolder(id)
        }}
      />
      <TooltipPortal />
    </div>
  )
}

