import { useCallback, useMemo, useState, type CSSProperties } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Trans, useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import { Icon, I } from './icons'
import { LinkCard } from './LinkCard'
import { NoteCard, type NoteEntry } from './NoteCard'
import { NoteViewDialog } from './NoteViewDialog'
import { FolderCard } from './FolderCard'
import { ListView } from './ListView'
import { CompactGrid } from './CompactGrid'
import { EmptyState } from './EmptyState'
import { useConfirm } from './ConfirmDialog'
import { useTags } from '../api/tags'
import {
  useDeleteLink,
  useMarkChangeSeen,
  usePinLink,
  useRefreshPreview,
} from '../api/links'
import { useDeleteNote, usePinNote } from '../api/notes'
import { useEscape } from '../hooks/useEscape'
import { mergeAlphaCells } from '../lib/mergeAlphaCells'
import type { Link as LinkT, Folder as FolderT, Entry, MergeSource } from '../api/types'

export type Sort = 'created' | 'clicks' | 'recent' | 'alpha' | 'alpha_desc'
export type ViewMode = 'cards' | 'compact' | 'list'

export type HomeProps = {
  entries: Entry[]
  totalLinks: number
  folders: FolderT[]
  // Flat list of every folder (any depth). Used to resolve the current
  // folder's name/parent for the breadcrumb; `folders` itself is scoped
  // (root folders on home, immediate children inside a folder).
  allFolders: FolderT[]
  openFolder: number | null
  // Gated open request (ADR-28) — routes through App.tsx's requestOpenFolder,
  // which prompts for a password first when the target folder is protected.
  // Never called with null; "go home" is a separate action (Topbar's onHome
  // calls setOpenFolder(null) directly, bypassing this gate entirely since
  // leaving a folder never needs to prove anything).
  onOpenFolder: (id: number) => void
  onNavigateBack: () => void
  isLoading: boolean
  onEdit: (l: LinkT) => void
  onAddImage: (l: LinkT) => void
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

export function Home({
  entries,
  totalLinks,
  folders,
  allFolders,
  openFolder,
  onOpenFolder,
  onNavigateBack,
  isLoading,
  onEdit,
  onAddImage,
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
              <div className="fx-stat-num">{totalLinks}</div>
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
          onAddImage={onAddImage}
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

export function CardsView({
  folders,
  entries,
  sort,
  isLoading,
  foldersCompact,
  onEdit,
  onAddImage,
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
  onAddImage: (l: LinkT) => void
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

  // Card mutation observers live once at grid level so memoized siblings keep
  // stable callback props while one card changes.
  const { mutate: deleteLink } = useDeleteLink()
  const { mutate: pinLink } = usePinLink()
  const { mutate: refreshPreview } = useRefreshPreview()
  const { mutate: markChangeSeen } = useMarkChangeSeen()
  const { mutate: deleteNote } = useDeleteNote()
  const { mutate: pinNote } = usePinNote()
  const confirm = useConfirm()
  const queryClient = useQueryClient()
  const onDeleteLink = useCallback(
    async (l: LinkT) => {
      const ok = await confirm({
        title: t('link_card.delete_confirm_title', { title: l.title }),
        message: t('link_card.delete_confirm_body', { url: l.url }),
        confirmLabel: t('link_card.delete_confirm_action'),
        destructive: true,
      })
      if (ok) deleteLink(l.id)
    },
    [confirm, deleteLink, t],
  )
  const onPinLink = useCallback(
    (l: LinkT, pinned: boolean) => pinLink({ id: l.id, pinned }),
    [pinLink],
  )
  const onRefreshPreview = useCallback((id: number) => refreshPreview(id), [refreshPreview])
  const onMarkSeen = useCallback((id: number) => markChangeSeen(id), [markChangeSeen])
  const onDeleteNote = useCallback(
    async (note: NoteEntry) => {
      const ok = await confirm({
        title: t('note_card.delete_confirm_title', { title: note.title }),
        message: t('note_card.delete_confirm_body'),
        confirmLabel: t('note_card.delete_confirm_action'),
        destructive: true,
      })
      if (ok) deleteNote(note.id)
    },
    [confirm, deleteNote, t],
  )
  const onPinNote = useCallback(
    (note: NoteEntry, pinned: boolean) => pinNote({ id: note.id, pinned }),
    [pinNote],
  )
  // Reading a note now opens a modal instead of navigating to the public page.
  //
  // The optimistic click bump went with it, deliberately: `click_log` is the
  // single source of truth for views and the public `/n/` route is the only
  // path that inserts into it, so an in-app read records nothing. Leaving the
  // bump would show a count the server never took, which the next refetch
  // silently corrects — a number that goes up and then back down on its own.
  const [viewingNoteId, setViewingNoteId] = useState<number | null>(null)
  const onOpenNote = useCallback((note: NoteEntry) => setViewingNoteId(note.id), [])

  // Default order: folders first (rule from CLAUDE.md), then entries in the
  // order the backend already returned them (pinned-first + active sort,
  // links and notes interleaved server-side — see internal/entries). Alpha
  // sort breaks the "folders first" rule on purpose — when the user picks
  // A→Z / Z→A, folders and entries interleave by name/title via
  // mergeAlphaCells so the alphabetical order is honest. It does NOT break
  // "pinned first": that rule holds in every sort mode, so mergeAlphaCells
  // interleaves within two blocks, pinned entries and then everything else.
  const isAlpha = sort === 'alpha' || sort === 'alpha_desc'
  const dir = sort === 'alpha' ? 1 : -1
  // Hooks must run unconditionally every render (isLoading/empty-state below
  // return early), so this memo always computes — it just skips the actual
  // interleave work when the active sort isn't alpha, since that result is
  // unused in that branch anyway.
  const alphaCells = useMemo(
    () => (isAlpha ? mergeAlphaCells(folders, entries, dir) : []),
    [isAlpha, folders, entries, dir],
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
  if (isAlpha) {
    const cells = alphaCells
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
            return (
              <LinkCard
                key={`link-${c.entry.id}`}
                link={c.entry}
                onEdit={onEdit}
                onMergeWith={onMergeIntoLink}
                onDelete={onDeleteLink}
                onPin={onPinLink}
                onRefreshPreview={onRefreshPreview}
                onAddImage={onAddImage}
                onMarkSeen={onMarkSeen}
              />
            )
          }
          return (
            <NoteCard
              key={`note-${c.entry.id}`}
              note={c.entry}
              onEdit={onEditNote}
              onMergeWith={onMergeIntoNote}
              onDelete={onDeleteNote}
              onPin={onPinNote}
              onOpen={onOpenNote}
            />
          )
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
          <LinkCard
            key={`link-${e.id}`}
            link={e}
            onEdit={onEdit}
            onMergeWith={onMergeIntoLink}
            onDelete={onDeleteLink}
            onPin={onPinLink}
            onRefreshPreview={onRefreshPreview}
            onAddImage={onAddImage}
            onMarkSeen={onMarkSeen}
          />
        ) : (
          <NoteCard
            key={`note-${e.id}`}
            note={e}
            onEdit={onEditNote}
            onMergeWith={onMergeIntoNote}
            onDelete={onDeleteNote}
            onPin={onPinNote}
            onOpen={onOpenNote}
          />
        ),
      )}
      {viewingNoteId !== null && (
        <NoteViewDialog
          noteId={viewingNoteId}
          onClose={() => setViewingNoteId(null)}
          onEdit={(note) => {
            setViewingNoteId(null)
            onEditNote(note.id)
          }}
        />
      )}
    </div>
  )
}

export function FolderBreadcrumb({
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
