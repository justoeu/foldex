import { lazy, Suspense, type CSSProperties } from 'react'
import { useTranslation } from 'react-i18next'
import { TagSidebar } from './components/TagSidebar'
import { DepStatusBar } from './components/DepStatusBar'
import { Topbar } from './components/Topbar'
import { Home } from './components/HomeView'
import { LinkDialog } from './components/LinkDialog'
import { FolderDialog } from './components/FolderDialog'
import { CommandPalette } from './components/CommandPalette'
import { TooltipPortal } from './components/TooltipPortal'
import { Icon, I } from './components/icons'
import { useAccountLocale } from './i18n/useAccountLocale'
import type { Entry, Folder } from './api/types'
import type { AppWorkspaceController, AppView } from './AppWorkspace'
import type { AppNavigationController } from './AppNavigation'
import type { AppDialogController } from './AppDialogs'
import type { AppDndController } from './hooks/useAppDndController'

const ImportPage = lazy(() => import('./pages/ImportPage').then((module) => ({ default: module.ImportPage })))
const StatsPage = lazy(() => import('./pages/StatsPage').then((module) => ({ default: module.StatsPage })))
const SettingsPage = lazy(() => import('./pages/SettingsPage').then((module) => ({ default: module.SettingsPage })))
const NoteDialog = lazy(() => import('./components/NoteDialog').then((module) => ({ default: module.NoteDialog })))

export type AppContentState = {
  entries: Entry[]
  folders: Folder[]
  allFolders: Folder[]
  entriesLoading: boolean
  entriesFetching: boolean
  foldersFetching: boolean
  allFoldersFetching: boolean
  hasMoreEntries: boolean
  fetchingMoreEntries: boolean
  refetchEntries: () => unknown
  refetchFolders: () => unknown
  refetchAllFolders: () => unknown
  fetchMoreEntries: () => unknown
}

type Props = {
  workspace: AppWorkspaceController
  navigation: AppNavigationController
  dialogs: AppDialogController
  dnd: AppDndController
  content: AppContentState
  totalLinks: number
}

export function AppShell(props: Props) {
  const { workspace } = props
  useAccountLocale()
  return (
    <div className={workspace.dark ? 'fx-shell fx-dark-shell' : 'fx-shell'}>
      <Aurora />
      <div
        className={workspace.mobileSidebarOpen ? 'fx-frame fx-frame-mobile-drawer-open' : 'fx-frame'}
        style={{ '--fx-sidebar-w': workspace.sidebarCollapsed ? '64px' : '252px' } as CSSProperties}
      >
        <MobileBackdrop workspace={workspace} />
        <AppSidebar {...props} />
        <MainWorkspace {...props} />
      </div>
      <DepStatusBar />
      <AppOverlays {...props} />
    </div>
  )
}

function Aurora() {
  return (
    <div className="fx-aurora" aria-hidden="true">
      <div className="fx-aurora-blob fx-aurora-a" />
      <div className="fx-aurora-blob fx-aurora-b" />
      <div className="fx-aurora-blob fx-aurora-c" />
      <div className="fx-aurora-blob fx-aurora-d" />
      <div className="fx-aurora-grain" />
    </div>
  )
}

function MobileBackdrop({ workspace }: { workspace: AppWorkspaceController }) {
  if (!workspace.mobileSidebarOpen) return null
  return (
    <div
      className="fx-mobile-backdrop"
      aria-hidden="true"
      onClick={workspace.closeMobileSidebar}
    />
  )
}

function AppSidebar({ workspace, totalLinks }: Props) {
  return (
    <TagSidebar
      collapsed={workspace.sidebarCollapsed}
      onToggleCollapsed={workspace.toggleSidebar}
      mobileOpen={workspace.mobileSidebarOpen}
      onMobileClose={workspace.closeMobileSidebar}
      selected={workspace.selectedTags}
      onToggle={workspace.toggleTag}
      onClear={workspace.clearTags}
      totalLinks={totalLinks}
    />
  )
}

function MainWorkspace(props: Props) {
  const { t } = useTranslation()
  const { workspace, navigation, dialogs } = props
  const onHome = () => {
    workspace.setView('home')
    navigation.goHome()
  }

  return (
    <main className="fx-main">
      <Topbar
        view={workspace.view}
        setView={workspace.setView}
        onHome={onHome}
        onOpenMobileSidebar={workspace.openMobileSidebar}
        q={workspace.q}
        setQ={workspace.setQ}
        onOpenPalette={workspace.openPalette}
        onOpenProfile={() => workspace.openSettingsAt('profile')}
        sort={workspace.sort}
        setSort={workspace.setSort}
        viewMode={navigation.viewMode}
        setViewMode={navigation.setViewMode}
        gridCols={workspace.gridCols}
        setGridCols={workspace.setGridCols}
        foldersCompact={navigation.foldersCompact}
        setFoldersCompact={navigation.setFoldersCompact}
        onNewLink={dialogs.openNewLink}
        onNewFolder={dialogs.openNewFolder}
        onNewNote={dialogs.openNewNote}
        dark={workspace.dark}
        setDark={workspace.setDark}
      />
      <ActivePage {...props} />
      <button
        type="button"
        className="fx-fab"
        aria-label={t('topbar.new_link')}
        data-tooltip={t('topbar.new_link')}
        onClick={() => dialogs.openNewLink()}
      >
        <Icon d={I.plus} size={22} stroke={2.4} />
      </button>
    </main>
  )
}

function ActivePage(props: Props) {
  switch (props.workspace.view) {
    case 'home':
      return <HomePage {...props} />
    case 'import':
      return <LazyPage view="import" workspace={props.workspace} />
    case 'stats':
      return <LazyPage view="stats" workspace={props.workspace} />
    case 'settings':
      return <LazyPage
        view="settings"
        workspace={props.workspace}
        dialogs={props.dialogs}
        // key remounts the hub when a deep link (user menu → Profile) targets
        // a section: SettingsPage owns its section state, so a fresh jump —
        // even to the SAME section — needs a fresh mount. `n` disambiguates
        // repeats; the gear clears the jump entirely (falls back to overview).
        key={props.workspace.settingsJump
          ? `${props.workspace.settingsJump.section}:${props.workspace.settingsJump.n}`
          : 'overview'}
        initialSection={props.workspace.settingsJump?.section}
      />
  }
}

function HomePage({ workspace, navigation, dialogs, dnd, content, totalLinks }: Props) {
  const reload = () => {
    void content.refetchEntries()
    void content.refetchFolders()
    void content.refetchAllFolders()
  }
  return (
    <Home
      entries={content.entries}
      totalLinks={totalLinks}
      folders={content.folders}
      allFolders={content.allFolders}
      openFolder={navigation.openFolder}
      onOpenFolder={navigation.requestOpenFolder}
      onNavigateBack={navigation.navigateBack}
      isLoading={content.entriesLoading}
      onEdit={dialogs.openEditLink}
      onAddImage={dialogs.openLinkImage}
      onEditNote={dialogs.openEditNote}
      onEditFolder={dialogs.openEditFolder}
      onNewLink={dialogs.openNewLink}
      onImport={() => workspace.setView('import')}
      viewMode={navigation.viewMode}
      gridCols={workspace.gridCols}
      foldersCompact={navigation.foldersCompact}
      sort={workspace.sort}
      onReload={reload}
      reloading={content.entriesFetching || content.foldersFetching || content.allFoldersFetching}
      hasMoreLinks={content.hasMoreEntries}
      loadingMoreLinks={content.fetchingMoreEntries}
      onLoadMoreLinks={() => void content.fetchMoreEntries()}
      onMoveLinkToFolder={dnd.onMoveLinkToFolder}
      onMoveNoteToFolder={dnd.onMoveNoteToFolder}
      onMergeEntries={dnd.onMergeEntries}
      onMoveFolder={dnd.onMoveFolder}
    />
  )
}

function LazyPage({
  view,
  workspace,
  dialogs,
  initialSection,
}: {
  view: Exclude<AppView, 'home'>
  workspace: AppWorkspaceController
  dialogs?: AppDialogController
  initialSection?: string
}) {
  return (
    <div className="fx-mainarea">
      <Suspense fallback={<div className="fx-empty">...</div>}>
        {view === 'import' && <ImportPage onDone={() => workspace.setView('home')} />}
        {view === 'stats' && <StatsPage />}
        {view === 'settings' && (
          <SettingsPage
            onEditFolder={dialogs?.openEditFolderById}
            onNavigate={(v) => workspace.setView(v)}
            initialSection={initialSection}
          />
        )}
      </Suspense>
    </div>
  )
}

function AppOverlays({ workspace, navigation, dialogs }: Props) {
  const editedFolderUnlock = dialogs.editFolder
    ? navigation.unlockTokenFor(dialogs.editFolder.id)
    : undefined
  const rememberEditedFolderUnlock = dialogs.editFolder
    ? (result: Parameters<AppNavigationController['rememberFolderUnlock']>[1]) => {
        navigation.rememberFolderUnlock(dialogs.editFolder!.id, result)
      }
    : undefined
  const openPaletteFolder = (id: number) => {
    workspace.closePalette()
    void navigation.requestOpenFolder(id)
  }

  return (
    <>
      <LinkDialog
        open={dialogs.linkDialogOpen}
        link={dialogs.editLink}
        initialUrl={dialogs.pastedUrl}
        focus={dialogs.linkFocus}
        defaultFolderId={navigation.openFolder}
        onClose={dialogs.closeLink}
      />
      <FolderDialog
        open={dialogs.folderDialogOpen}
        folder={dialogs.editFolder}
        justCreated={dialogs.folderJustCreated}
        parentId={dialogs.editFolder ? null : navigation.openFolder}
        unlockToken={editedFolderUnlock}
        onUnlocked={rememberEditedFolderUnlock}
        onClose={dialogs.closeFolder}
      />
      {dialogs.noteDialogOpen && (
        <Suspense fallback={<div className="fx-overlay fx-overlay-modal" />}>
          <NoteDialog
            open={dialogs.noteDialogOpen}
            noteId={dialogs.editNoteId}
            defaultFolderId={navigation.openFolder}
            onClose={dialogs.closeNote}
          />
        </Suspense>
      )}
      <CommandPalette
        open={workspace.paletteOpen}
        onClose={workspace.closePalette}
        onOpenFolder={openPaletteFolder}
      />
      <TooltipPortal />
    </>
  )
}
