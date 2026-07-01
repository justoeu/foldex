import { lazy, Suspense, useCallback, useEffect, useMemo, useState, type CSSProperties } from 'react'
import { useHotkeys } from 'react-hotkeys-hook'
import { usePasteUrl } from './hooks/usePasteUrl'
import { usePersistedState, usePersistedMap } from './hooks/usePersistedState'
import { Trans, useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import './styles/foldex.css'
import './styles/overrides.css'

import { Icon, I } from './components/icons'
import { TagSidebar } from './components/TagSidebar'
import { Topbar } from './components/Topbar'
import { LinkCard } from './components/LinkCard'
import { NoteCard, type MergeSource } from './components/NoteCard'
import { FolderCard } from './components/FolderCard'
import { ListView } from './components/ListView'
import { CompactGrid } from './components/CompactGrid'
import { LinkDialog } from './components/LinkDialog'
import { FolderDialog } from './components/FolderDialog'
import { NoteDialog } from './components/NoteDialog'
import { CommandPalette } from './components/CommandPalette'
import { TooltipPortal } from './components/TooltipPortal'
import { EmptyState } from './components/EmptyState'
// Code-split the two off-hot-path views. Home is by far the most-visited
// view; lazy-loading ImportPage + StatsPage trims the initial JS bundle by
// the chart code, backup card, and dialog plumbing they pull in. The Suspense
// boundary below renders a tiny fallback while the chunk loads.
const ImportPage = lazy(() => import('./pages/ImportPage').then((m) => ({ default: m.ImportPage })))
const StatsPage = lazy(() => import('./pages/StatsPage').then((m) => ({ default: m.StatsPage })))
import { useUpdateLink } from './api/links'
import { flattenEntries, useEntries } from './api/entries'
import { useUpdateNote } from './api/notes'
import { useTags } from './api/tags'
import { useFolders, useCreateFolder, useUpdateFolder } from './api/folders'
import { useEscape } from './hooks/useEscape'
import { mergeAlphaCells } from './lib/mergeAlphaCells'
import type { Link as LinkT, Folder as FolderT, Entry } from './api/types'

type View = 'home' | 'import' | 'stats'
type Sort = 'created' | 'clicks' | 'recent' | 'alpha' | 'alpha_desc'
type ViewMode = 'cards' | 'compact' | 'list'

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
  const [dark, setDark] = usePersistedState('foldex.dark', false)
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

  // Theme toggle drives a class on the shell wrapper so all .fx-* tokens flip.
  useEffect(() => {
    document.documentElement.classList.toggle('fx-dark', dark)
    document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
    return () => {
      document.documentElement.classList.remove('fx-dark')
      document.documentElement.style.colorScheme = ''
    }
  }, [dark])

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
  const entries = useEntries({
    q,
    tagIds: selectedTags,
    sort,
    ...(openFolder !== null ? { folderId: openFolder } : { ungrouped: true }),
  })
  // Folder list scope:
  //   home (no openFolder)        → only root folders ({scope: 'root'})
  //   inside folder (openFolder)  → only direct children ({scope: openFolder})
  // LinkDialog still loads the full flat list via a separate hook call so the
  // folder picker can target anything regardless of position.
  const folders = useFolders({ scope: openFolder === null ? 'root' : openFolder })
  const allFolders = useFolders({ scope: null })
  const { data: allTags = [] } = useTags()

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
  // Bound per card kind so LinkCard/NoteCard each get a stable 2-arg
  // (source, targetId) callback — matching what their onMergeWith prop
  // expects — without re-deriving the target's MergeSource on every render.
  const onMergeIntoLink = useCallback(
    (source: MergeSource, targetId: number) => { void onMergeEntries(source, { kind: 'link', id: targetId }) },
    [onMergeEntries],
  )
  const onMergeIntoNote = useCallback(
    (source: MergeSource, targetId: number) => { void onMergeEntries(source, { kind: 'note', id: targetId }) },
    [onMergeEntries],
  )

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
              onOpenFolder={setOpenFolder}
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
      <NoteDialog
        open={noteDialogOpen}
        noteId={editNoteId}
        defaultFolderId={openFolder}
        onClose={() => {
          setNoteDialogOpen(false)
          setEditNoteId(null)
        }}
      />
      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        onOpenFolder={(id) => {
          setOpenFolder(id)
          setPaletteOpen(false)
        }}
      />
      <TooltipPortal />
    </div>
  )
}

type HomeProps = {
  entries: Entry[]
  folders: FolderT[]
  // Flat list of every folder (any depth). Used to resolve the current
  // folder's name/parent for the breadcrumb; `folders` itself is scoped
  // (root folders on home, immediate children inside a folder).
  allFolders: FolderT[]
  openFolder: number | null
  onOpenFolder: (id: number | null) => void
  onNavigateBack: () => void
  isLoading: boolean
  onEdit: (l: LinkT) => void
  onEditNote: (id: number) => void
  onEditFolder: (f: FolderT) => void
  onNewLink: () => void
  onImport: () => void
  viewMode: ViewMode
  gridCols: 3 | 5 | 8
  foldersCompact: boolean
  sort: Sort
  onReload: () => void
  reloading: boolean
  onMoveLinkToFolder: (linkId: number, folderId: number) => void
  onMoveNoteToFolder: (noteId: number, folderId: number) => void
  onMergeEntries: (a: MergeSource, b: MergeSource) => void
  onMoveFolder: (sourceId: number, targetId: number) => void
  // Pagination: when the backend reports more pages exist, Home shows a
  // "Load more" button under the grid (all three viewModes). fetchNextPage
  // appends the next page to the InfiniteData cache; flattenEntries above
  // already merges it into the `entries` array passed in.
  hasMoreLinks: boolean
  loadingMoreLinks: boolean
  onLoadMoreLinks: () => void
}

function Home({
  entries,
  folders,
  allFolders,
  openFolder,
  onOpenFolder,
  onNavigateBack,
  isLoading,
  onEdit,
  onEditNote,
  onEditFolder,
  onNewLink,
  onImport,
  viewMode,
  gridCols,
  foldersCompact,
  sort,
  onReload,
  reloading,
  onMoveLinkToFolder,
  onMoveNoteToFolder,
  onMergeEntries,
  onMoveFolder,
  hasMoreLinks,
  loadingMoreLinks,
  onLoadMoreLinks,
}: HomeProps) {
  const { t } = useTranslation()
  const totalClicks = useMemo(() => entries.reduce((acc, e) => acc + e.click_count, 0), [entries])
  const { data: tags = [] } = useTags()
  const currentFolder = openFolder !== null ? allFolders.find((f) => f.id === openFolder) : null
  // Esc goes back one level (matches the breadcrumb "← Pastas" affordance).
  useEscape(onNavigateBack, openFolder !== null)

  // Empty only when BOTH entries AND folders are empty — inside a nested
  // folder with subfolders but no direct entries, the view is NOT empty.
  const isEmpty = !isLoading && entries.length === 0 && folders.length === 0
  if (isEmpty) {
    return (
      <div className="fx-mainarea">
        {openFolder !== null && (
          <FolderBreadcrumb
            folder={currentFolder ? { id: currentFolder.id, name: currentFolder.name } : null}
            onBack={onNavigateBack}
            onEdit={() => currentFolder && onEditFolder(currentFolder)}
            onReload={onReload}
            reloading={reloading}
          />
        )}
        <EmptyState onNewLink={onNewLink} onImport={onImport} />
      </div>
    )
  }

  return (
    <div className="fx-mainarea" style={{ '--fx-cols': String(gridCols) } as CSSProperties}>
      {openFolder !== null ? (
        <FolderBreadcrumb
          folder={currentFolder ? { id: currentFolder.id, name: currentFolder.name } : null}
          onBack={onNavigateBack}
          onEdit={() => currentFolder && onEditFolder(currentFolder)}
          onReload={onReload}
          reloading={reloading}
        />
      ) : (
        <div className="fx-pagehead">
          <div>
            <div className="fx-pagehead-kicker">{t('home.page_kicker')}</div>
            <h1 className="fx-pagehead-h">{t('home.page_title')}</h1>
          </div>
          <div className="fx-pagehead-stats">
            <div className="fx-stat">
              <div className="fx-stat-num">{entries.length + folders.reduce((a, f) => a + f.link_count, 0)}</div>
              <div className="fx-stat-cap">{t('home.stat_links')}</div>
            </div>
            <div className="fx-stat">
              <div className="fx-stat-num">{tags.length}</div>
              <div className="fx-stat-cap">{t('home.stat_tags')}</div>
            </div>
            <div className="fx-stat">
              <div className="fx-stat-num fx-stat-num-accent">{totalClicks}</div>
              <div className="fx-stat-cap">{t('home.stat_clicks')}</div>
            </div>
          </div>
        </div>
      )}

      {viewMode === 'cards' && (
        <CardsView
          folders={folders}
          entries={entries}
          sort={sort}
          isLoading={isLoading}
          foldersCompact={foldersCompact}
          onEdit={onEdit}
          onEditNote={onEditNote}
          onOpenFolder={onOpenFolder}
          onEditFolder={onEditFolder}
          onMoveLinkToFolder={onMoveLinkToFolder}
          onMoveNoteToFolder={onMoveNoteToFolder}
          onMergeEntries={onMergeEntries}
          onMoveFolder={onMoveFolder}
          t={t}
        />
      )}
      {viewMode === 'list' && (
        <ListView
          folders={folders}
          entries={entries}
          sort={sort}
          onEdit={onEdit}
          onEditNote={onEditNote}
          onOpenFolder={onOpenFolder}
          onEditFolder={onEditFolder}
        />
      )}
      {viewMode === 'compact' && (
        <CompactGrid
          folders={folders}
          entries={entries}
          sort={sort}
          onEdit={onEdit}
          onEditNote={onEditNote}
          onOpenFolder={onOpenFolder}
          onEditFolder={onEditFolder}
        />
      )}
      {/* "Load more" — only shown when the backend reported additional
          pages exist. Hidden during initial load (then isEmpty handles the
          empty state above) and when the user is mid-fetch on this click.
          The button is full-width with subtle padding so it doesn't compete
          with the grid but stays reachable on mobile. */}
      {hasMoreLinks && (
        <div className="fx-loadmore">
          <button
            className="fx-loadmore-btn"
            onClick={onLoadMoreLinks}
            disabled={loadingMoreLinks}
            aria-label={t('links.load_more_aria')}
          >
            {loadingMoreLinks ? t('links.loading_more') : t('links.load_more')}
          </button>
        </div>
      )}
    </div>
  )
}

function CardsView({
  folders,
  entries,
  sort,
  isLoading,
  foldersCompact,
  onEdit,
  onEditNote,
  onOpenFolder,
  onEditFolder,
  onMoveLinkToFolder,
  onMoveNoteToFolder,
  onMergeEntries,
  onMoveFolder,
  t,
}: {
  folders: FolderT[]
  entries: Entry[]
  sort: Sort
  isLoading: boolean
  foldersCompact: boolean
  onEdit: (l: LinkT) => void
  onEditNote: (id: number) => void
  onOpenFolder: (id: number) => void
  onEditFolder: (f: FolderT) => void
  onMoveLinkToFolder: (linkId: number, folderId: number) => void
  onMoveNoteToFolder: (noteId: number, folderId: number) => void
  onMergeEntries: (a: MergeSource, b: MergeSource) => void
  onMoveFolder: (sourceId: number, targetId: number) => void
  t: TFunction
}) {
  const onMergeIntoLink = useCallback(
    (source: MergeSource, targetId: number) => onMergeEntries(source, { kind: 'link', id: targetId }),
    [onMergeEntries],
  )
  const onMergeIntoNote = useCallback(
    (source: MergeSource, targetId: number) => onMergeEntries(source, { kind: 'note', id: targetId }),
    [onMergeEntries],
  )

  if (isLoading) {
    return <div style={{ padding: 48, color: 'var(--fx-ink-4)' }}>{t('home.loading')}</div>
  }
  if (folders.length === 0 && entries.length === 0) {
    return (
      <div style={{ padding: '48px 6px', color: 'var(--fx-ink-4)' }}>
        <Trans i18nKey="home.cards_empty_html" components={{ kbd: <kbd className="fx-kbd" /> }} />
      </div>
    )
  }
  // Default order: folders first (rule from CLAUDE.md), then entries in the
  // order the backend already returned them (pinned-first + active sort,
  // links and notes interleaved server-side — see internal/entries). Alpha
  // sort breaks the "folders first" rule on purpose — when the user picks
  // A→Z / Z→A, folders and entries interleave by name/title via
  // mergeAlphaCells so the alphabetical order is honest.
  const isAlpha = sort === 'alpha' || sort === 'alpha_desc'
  if (isAlpha) {
    const dir = sort === 'alpha' ? 1 : -1
    const cells = mergeAlphaCells(folders, entries, dir)
    return (
      <div className="fx-grid">
        {cells.map((c) => {
          if (c.kind === 'folder') {
            return (
              <FolderCard
                key={`folder-${c.folder.id}`}
                folder={c.folder}
                compact={foldersCompact}
                onOpen={onOpenFolder}
                onEdit={onEditFolder}
                onDropLink={onMoveLinkToFolder}
                onDropNote={onMoveNoteToFolder}
                onDropFolder={onMoveFolder}
              />
            )
          }
          if (c.kind === 'link') {
            return <LinkCard key={`link-${c.entry.id}`} link={c.entry} onEdit={onEdit} onMergeWith={onMergeIntoLink} />
          }
          return <NoteCard key={`note-${c.entry.id}`} note={c.entry} onEdit={onEditNote} onMergeWith={onMergeIntoNote} />
        })}
      </div>
    )
  }
  return (
    <div className="fx-grid">
      {folders.map((f) => (
        <FolderCard
          key={`folder-${f.id}`}
          folder={f}
          compact={foldersCompact}
          onOpen={onOpenFolder}
          onEdit={onEditFolder}
          onDropLink={onMoveLinkToFolder}
          onDropNote={onMoveNoteToFolder}
          onDropFolder={onMoveFolder}
        />
      ))}
      {entries.map((e) =>
        e.kind === 'link' ? (
          <LinkCard key={`link-${e.id}`} link={e} onEdit={onEdit} onMergeWith={onMergeIntoLink} />
        ) : (
          <NoteCard key={`note-${e.id}`} note={e} onEdit={onEditNote} onMergeWith={onMergeIntoNote} />
        ),
      )}
    </div>
  )
}

function FolderBreadcrumb({
  folder,
  onBack,
  onEdit,
  onReload,
  reloading,
}: {
  folder: { id: number; name: string } | null
  onBack: () => void
  onEdit: () => void
  onReload: () => void
  reloading: boolean
}) {
  const { t } = useTranslation()
  return (
    <div className="fx-pagehead fx-pagehead-folder">
      <div>
        <div className="fx-pagehead-kicker">
          <button type="button" className="fx-breadcrumb-back" onClick={onBack}>
            {t('home.breadcrumb_back')}
          </button>
        </div>
        <h1 className="fx-pagehead-h">{folder?.name ?? t('home.breadcrumb_default')}</h1>
      </div>
      {folder && (
        <div style={{ display: 'flex', gap: 8 }}>
          <button
            className={'fx-confirm-btn fx-confirm-btn-icon' + (reloading ? ' fx-confirm-btn-spinning' : '')}
            onClick={onReload}
            disabled={reloading}
            aria-label={t('common.reload_folder_aria')}
            data-tooltip={t('home.breadcrumb_reload_tooltip')}
          >
            <Icon d={I.refresh} size={14} stroke={2} />
          </button>
          <button
            className="fx-confirm-btn"
            onClick={onEdit}
            aria-label={t('common.edit_folder_aria', { name: folder?.name ?? '' })}
            data-tooltip={t('home.breadcrumb_edit_tooltip')}
          >
            {t('home.breadcrumb_edit_label')}
          </button>
        </div>
      )}
    </div>
  )
}

