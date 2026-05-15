import { useCallback, useEffect, useMemo, useState } from 'react'
import { useHotkeys } from 'react-hotkeys-hook'
import { usePasteUrl } from './hooks/usePasteUrl'
import { Trans, useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import './styles/foldex.css'
import './styles/overrides.css'

import { Icon, I } from './components/icons'
import { TagSidebar } from './components/TagSidebar'
import { Topbar } from './components/Topbar'
import { LinkCard } from './components/LinkCard'
import { FolderCard } from './components/FolderCard'
import { ListView } from './components/ListView'
import { CompactGrid } from './components/CompactGrid'
import { LinkDialog } from './components/LinkDialog'
import { FolderDialog } from './components/FolderDialog'
import { CommandPalette } from './components/CommandPalette'
import { TooltipPortal } from './components/TooltipPortal'
import { EmptyState } from './components/EmptyState'
import { ImportPage } from './pages/ImportPage'
import { StatsPage } from './pages/StatsPage'
import { useLinks, useUpdateLink } from './api/links'
import { useTags } from './api/tags'
import { useFolders, useCreateFolder, useUpdateFolder } from './api/folders'
import { useEscape } from './hooks/useEscape'
import type { Link as LinkT, Folder as FolderT } from './api/types'

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
  const [viewModeMap, setViewModeMap] = useState<Record<string, ViewMode>>(() => {
    if (typeof localStorage === 'undefined') return {}
    try {
      const raw = localStorage.getItem('foldex.viewMode.map')
      const parsed = raw ? JSON.parse(raw) : {}
      return typeof parsed === 'object' && parsed !== null ? parsed : {}
    } catch {
      return {}
    }
  })
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
  const [paletteOpen, setPaletteOpen] = useState(false)
  const [dark, setDark] = useState(false)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(
    () => typeof localStorage !== 'undefined' && localStorage.getItem('foldex.sidebar.collapsed') === '1',
  )
  // Drawer-style sidebar on mobile (≤768px). Stays in-memory only — phone
  // users almost never want it open by default after navigation. The
  // toggle button on the topbar flips this, and tapping the backdrop or
  // pressing Esc closes it.
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false)
  const [gridCols, setGridCols] = useState<3 | 5 | 8>(() => {
    if (typeof localStorage === 'undefined') return 5
    const raw = parseInt(localStorage.getItem('foldex.grid.cols') ?? '5', 10)
    return raw === 3 || raw === 8 ? raw : 5
  })
  // Folder navigation is a stack of ids — last entry is the currently open
  // folder; each push enters a child, each pop goes back one level. Lives
  // purely in-memory (no URL exposure: internal IDs shouldn't bleed into the
  // address bar). `openFolder` is just the top of the stack.
  const [folderPath, setFolderPath] = useState<number[]>([])
  const openFolder = folderPath.at(-1) ?? null
  const setOpenFolder = (id: number | null) => {
    setFolderPath(id == null ? [] : [...folderPath, id])
  }
  const navigateBack = () => setFolderPath(folderPath.slice(0, -1))

  // Theme toggle drives a class on the shell wrapper so all .fx-* tokens flip.
  useEffect(() => {
    document.documentElement.classList.toggle('fx-dark', dark)
    document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
  }, [dark])

  // Persist sidebar state across reloads.
  useEffect(() => {
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem('foldex.sidebar.collapsed', sidebarCollapsed ? '1' : '0')
    }
  }, [sidebarCollapsed])

  useEffect(() => {
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem('foldex.grid.cols', String(gridCols))
    }
  }, [gridCols])

  useEffect(() => {
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem('foldex.viewMode.map', JSON.stringify(viewModeMap))
    }
  }, [viewModeMap])

  // Derive the active viewMode + setter from openFolder. Home and each folder
  // get their own slot in the map; switching context surfaces the saved choice.
  const viewModeKey = openFolder !== null ? `folder.${openFolder}` : 'home'
  const viewMode: ViewMode = viewModeMap[viewModeKey] ?? 'cards'
  const setViewMode = (m: ViewMode) => setViewModeMap((prev) => ({ ...prev, [viewModeKey]: m }))

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
  // tag inside a folder narrows that folder's links by tag. Home view always
  // shows ungrouped links (folder cards represent the rest).
  const links = useLinks({
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

    setViewModeMap((prev) => {
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
  }, [allFoldersData, folderPath])
  const updateLink = useUpdateLink()
  const createFolder = useCreateFolder()
  const updateFolder = useUpdateFolder()

  // Drag-and-drop handlers wired down to FolderCard / LinkCard.
  //
  // Move: PATCH the dragged link with the target folder's id.
  // Merge: when two link cards collide, create a fresh folder ("Nova pasta")
  //   and PATCH both links into it; open the FolderDialog in edit mode so the
  //   user can immediately rename it. Sequential calls; race-tolerant for a
  //   single-user local app.
  const onMoveLinkToFolder = (linkId: number, folderId: number) => {
    updateLink.mutate({ id: linkId, body: { folder_id: folderId } })
  }
  // Move folder `sourceId` to be a child of `targetId`. Refuses the move when
  // the target is `sourceId` itself or sits inside `sourceId`'s subtree —
  // that would create a cycle (A → B → A). The backend has its own guard
  // too, but checking client-side keeps the UI snappy and avoids a roundtrip
  // for the obvious bad cases.
  const onMoveFolder = (sourceId: number, targetId: number) => {
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
  }
  const onMergeLinks = async (aId: number, bId: number) => {
    if (aId === bId) return
    try {
      // If we're already inside a folder, the merged-pair lives under it
      // (subfolder); otherwise it's a root folder.
      const f = await createFolder.mutateAsync({ name: t('home.merge_new_folder_name'), parent_id: openFolder ?? null })
      await Promise.all([
        updateLink.mutateAsync({ id: aId, body: { folder_id: f.id } }),
        updateLink.mutateAsync({ id: bId, body: { folder_id: f.id } }),
      ])
      setEditFolder(f)
      setFolderJustCreated(true)
      setFolderDialogOpen(true)
    } catch {
      // Mutation errors surface via toast/console; non-fatal here.
    }
  }

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
        style={{ ['--fx-sidebar-w' as never]: sidebarCollapsed ? '64px' : '252px' }}
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
          totalLinks={Math.max(totalLinks, links.data?.length ?? 0)}
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
            onNewLink={() => {
              setEditLink(null)
              setLinkDialogOpen(true)
            }}
            onNewFolder={() => {
              setEditFolder(null)
              setFolderJustCreated(false)
              setFolderDialogOpen(true)
            }}
            dark={dark}
            setDark={setDark}
          />

          {view === 'home' && (
            <Home
              links={links.data ?? []}
              folders={folders.data ?? []}
              allFolders={allFolders.data ?? []}
              openFolder={openFolder}
              onOpenFolder={setOpenFolder}
              onNavigateBack={navigateBack}
              isLoading={links.isLoading}
              onEdit={(l) => {
                setEditLink(l)
                setLinkDialogOpen(true)
              }}
              onEditFolder={(f) => {
                setEditFolder(f)
                setFolderJustCreated(false)
                setFolderDialogOpen(true)
              }}
              onNewLink={() => {
                setEditLink(null)
                setLinkDialogOpen(true)
              }}
              onImport={() => setView('import')}
              viewMode={viewMode}
              gridCols={gridCols}
              sort={sort}
              onReload={() => {
                links.refetch()
                folders.refetch()
                allFolders.refetch()
              }}
              reloading={links.isFetching || folders.isFetching || allFolders.isFetching}
              onMoveLinkToFolder={onMoveLinkToFolder}
              onMergeLinks={onMergeLinks}
              onMoveFolder={onMoveFolder}
            />
          )}
          {view === 'import' && (
            <div className="fx-mainarea">
              <ImportPage onDone={() => setView('home')} />
            </div>
          )}
          {view === 'stats' && (
            <div className="fx-mainarea">
              <StatsPage />
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
  links: LinkT[]
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
  onEditFolder: (f: FolderT) => void
  onNewLink: () => void
  onImport: () => void
  viewMode: ViewMode
  gridCols: 3 | 5 | 8
  sort: Sort
  onReload: () => void
  reloading: boolean
  onMoveLinkToFolder: (linkId: number, folderId: number) => void
  onMergeLinks: (aId: number, bId: number) => void
  onMoveFolder: (sourceId: number, targetId: number) => void
}

function Home({
  links,
  folders,
  allFolders,
  openFolder,
  onOpenFolder,
  onNavigateBack,
  isLoading,
  onEdit,
  onEditFolder,
  onNewLink,
  onImport,
  viewMode,
  gridCols,
  sort,
  onReload,
  reloading,
  onMoveLinkToFolder,
  onMergeLinks,
  onMoveFolder,
}: HomeProps) {
  const { t } = useTranslation()
  const totalClicks = useMemo(() => links.reduce((acc, l) => acc + l.click_count, 0), [links])
  const { data: tags = [] } = useTags()
  const currentFolder = openFolder !== null ? allFolders.find((f) => f.id === openFolder) : null
  // Esc goes back one level (matches the breadcrumb "← Pastas" affordance).
  useEscape(onNavigateBack, openFolder !== null)

  // Empty only when BOTH links AND folders are empty — inside a nested
  // folder with subfolders but no direct links, the view is NOT empty.
  const isEmpty = !isLoading && links.length === 0 && folders.length === 0
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
    <div className="fx-mainarea" style={{ ['--fx-cols' as never]: String(gridCols) }}>
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
              <div className="fx-stat-num">{links.length + folders.reduce((a, f) => a + f.link_count, 0)}</div>
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
          links={links}
          sort={sort}
          isLoading={isLoading}
          onEdit={onEdit}
          onOpenFolder={onOpenFolder}
          onEditFolder={onEditFolder}
          onMoveLinkToFolder={onMoveLinkToFolder}
          onMergeLinks={onMergeLinks}
          onMoveFolder={onMoveFolder}
          t={t}
        />
      )}
      {viewMode === 'list' && <ListView links={links} onEdit={onEdit} />}
      {viewMode === 'compact' && <CompactGrid links={links} onEdit={onEdit} />}
    </div>
  )
}

function CardsView({
  folders,
  links,
  sort,
  isLoading,
  onEdit,
  onOpenFolder,
  onEditFolder,
  onMoveLinkToFolder,
  onMergeLinks,
  onMoveFolder,
  t,
}: {
  folders: FolderT[]
  links: LinkT[]
  sort: Sort
  isLoading: boolean
  onEdit: (l: LinkT) => void
  onOpenFolder: (id: number) => void
  onEditFolder: (f: FolderT) => void
  onMoveLinkToFolder: (linkId: number, folderId: number) => void
  onMergeLinks: (aId: number, bId: number) => void
  onMoveFolder: (sourceId: number, targetId: number) => void
  t: TFunction
}) {
  if (isLoading) {
    return <div style={{ padding: 48, color: 'var(--fx-ink-4)' }}>{t('home.loading')}</div>
  }
  if (folders.length === 0 && links.length === 0) {
    return (
      <div style={{ padding: '48px 6px', color: 'var(--fx-ink-4)' }}>
        <Trans i18nKey="home.cards_empty_html" components={{ kbd: <kbd className="fx-kbd" /> }} />
      </div>
    )
  }
  // Default order: folders first (rule from CLAUDE.md), then links. Alpha sort
  // breaks that rule on purpose — when the user picks A→Z / Z→A, folders and
  // links are interleaved by name/title so the alphabetical order is honest.
  // Pinned links still sort first within the link group (server-side prefix).
  const isAlpha = sort === 'alpha' || sort === 'alpha_desc'
  if (isAlpha) {
    type Cell =
      | { kind: 'folder'; name: string; folder: FolderT }
      | { kind: 'link'; name: string; link: LinkT }
    const cells: Cell[] = [
      ...folders.map<Cell>((f) => ({ kind: 'folder', name: f.name, folder: f })),
      ...links.map<Cell>((l) => ({ kind: 'link', name: l.title, link: l })),
    ]
    const dir = sort === 'alpha' ? 1 : -1
    cells.sort((a, b) => dir * a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }))
    return (
      <div className="fx-grid">
        {cells.map((c) =>
          c.kind === 'folder' ? (
            <FolderCard
              key={`folder-${c.folder.id}`}
              folder={c.folder}
              onOpen={onOpenFolder}
              onEdit={onEditFolder}
              onDropLink={onMoveLinkToFolder}
              onDropFolder={onMoveFolder}
            />
          ) : (
            <LinkCard
              key={`link-${c.link.id}`}
              link={c.link}
              onEdit={onEdit}
              onMergeWith={onMergeLinks}
            />
          ),
        )}
      </div>
    )
  }
  return (
    <div className="fx-grid">
      {folders.map((f) => (
        <FolderCard
          key={`folder-${f.id}`}
          folder={f}
          onOpen={onOpenFolder}
          onEdit={onEditFolder}
          onDropLink={onMoveLinkToFolder}
          onDropFolder={onMoveFolder}
        />
      ))}
      {links.map((l) => (
        <LinkCard key={l.id} link={l} onEdit={onEdit} onMergeWith={onMergeLinks} />
      ))}
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
            aria-label="reload folder"
            data-tooltip={t('home.breadcrumb_reload_tooltip')}
          >
            <Icon d={I.refresh} size={14} stroke={2} />
          </button>
          <button
            className="fx-confirm-btn"
            onClick={onEdit}
            aria-label="edit folder"
            data-tooltip={t('home.breadcrumb_edit_tooltip')}
          >
            {t('home.breadcrumb_edit_label')}
          </button>
        </div>
      )}
    </div>
  )
}

