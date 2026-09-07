import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import { Icon, I } from './icons'
import { Favicon } from './Favicon'
import { TagChip } from './TagChip'
import { goHref } from '../api/links'
import { goNoteHref } from '../api/notes'
import { collectCachedEntries, flattenEntries, useEntries } from '../api/entries'
import { useTags } from '../api/tags'
import { useFolders } from '../api/folders'
import { searchFolderTree } from '../lib/folderTree'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { useHasPermission } from '../auth/AuthProvider'
import type { Link } from '../api/types'

type Props = {
  open: boolean
  onClose: () => void
  onOpenFolder?: (id: number) => void
  onRevealLink?: (link: Link) => void
  onEditLink?: (link: Link) => void
}

// Search paints at most 12 links + 12 notes. Fetching 200 rows to fill that
// is the N1-NEX-009 overfetch; 24 mixed rows is enough to fill both lists.
const PALETTE_SEARCH_LIMIT = 24

const PALETTE_DEBOUNCE_MS = 200

// One keyboard-navigable action per activatable row, flattened in render
// order. Tag rows are deliberately absent: they carry no click handler, so
// they cannot be "opened" — highlighting them would promise a dead Enter.
type PaletteAction = { kind: 'link' | 'note'; href: string } | { kind: 'folder'; id: number }

export function CommandPalette({ open, onClose, onOpenFolder, onRevealLink, onEditLink }: Props) {
  const { t } = useTranslation()
  const canEdit = useHasPermission('content.write')
  const [q, setQ] = useState('')
  const [debounced, setDebounced] = useState('')
  const [highlight, setHighlight] = useState(0)

  useEffect(() => {
    if (!open) {
      setQ('')
      setDebounced('')
      return
    }
    const id = setTimeout(() => setDebounced(q), PALETTE_DEBOUNCE_MS)
    return () => clearTimeout(id)
  }, [open, q])

  // Every new result set resets the cursor — row 0 is the natural target of
  // the footer's bare ↵ promise.
  useEffect(() => {
    setHighlight(0)
  }, [debounced, open])

  // ESC closes — routed through the global useEscape stack so when the
  // palette sits on top of a folder view, Esc fires THIS handler (not the
  // folder's navigateBack).
  useEscape(onClose, open)

  const qc = useQueryClient()
  const searching = debounced.length > 0
  // Empty-open reuses Home's ['entries'] cache (client-side top-clicks).
  // Search is a slim page, still with qSettled so the palette's 200ms
  // debounce is not stacked on useEntries' 500ms Home debounce.
  const searchQuery = useEntries(
    { q: debounced, limit: PALETTE_SEARCH_LIMIT },
    { enabled: open && searching, qSettled: true },
  )
  const cached = !open || searching ? [] : collectCachedEntries(qc)
  const entries = searching ? flattenEntries(searchQuery.data) : cached
  const links = useMemo(() => entries.filter((e) => e.kind === 'link'), [entries])
  const notes = useMemo(() => entries.filter((e) => e.kind === 'note'), [entries])
  const { data: tags = [] } = useTags()
  const { data: folders = [] } = useFolders({ fields: 'minimal' })

  const suggested = useMemo(() => [...links].sort((a, b) => b.click_count - a.click_count).slice(0, 3), [links])
  const matches = useMemo(() => links.slice(0, 12), [links])
  const noteMatches = useMemo(() => notes.slice(0, 12), [notes])
  const tagMatches = useMemo(() => {
    if (!debounced) return []
    const f = debounced.toLowerCase()
    return tags.filter((tag) => tag.name.toLowerCase().includes(f)).slice(0, 4)
  }, [tags, debounced])
  // Hierarchical folder rendering — depth-aware so the user sees the tree
  // shape, with the full ancestor path attached to each match for context
  // when the search query lands deep in the tree.
  const folderMatches = useMemo(() => {
    if (!folders.length) return []
    return searchFolderTree(folders, debounced).slice(0, 8)
  }, [folders, debounced])

  const folderNameById = useMemo(() => {
    const map = new Map<number, string>()
    for (const folder of folders) map.set(folder.id, folder.name)
    return map
  }, [folders])

  const actions = useMemo<PaletteAction[]>(() => [
    ...(!debounced ? suggested.map((l) => ({ kind: 'link' as const, href: goHref(l) })) : []),
    ...matches.map((l) => ({ kind: 'link' as const, href: goHref(l) })),
    ...noteMatches.map((n) => ({ kind: 'note' as const, href: goNoteHref(n) })),
    ...folderMatches.map((f) => ({ kind: 'folder' as const, id: f.id })),
  ], [debounced, suggested, matches, noteMatches, folderMatches])

  // The footer promises ↵ "open via /go" (same tab) and ⌘↵ "open in new
  // tab" — this handler is what makes those keys real. window.open is used
  // for both arms so there is one navigation seam to reason about (and one
  // to spy on in tests); a keydown is a trusted user activation, so the
  // popup blocker does not interfere.
  const activate = (action: PaletteAction | undefined, newTab: boolean) => {
    if (!action) return
    if (action.kind === 'folder') {
      onOpenFolder?.(action.id)
      return
    }
    window.open(action.href, newTab ? '_blank' : '_self')
    onClose()
  }

  const onInputKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const delta = e.key === 'ArrowDown' ? 1 : -1
      setHighlight((h) => Math.min(Math.max(h + delta, 0), Math.max(actions.length - 1, 0)))
      return
    }
    if (e.key === 'Enter') {
      e.preventDefault()
      activate(actions[highlight], e.metaKey || e.ctrlKey)
    }
  }

  // Action offsets in render order, so rows and the keyboard cursor share
  // one numbering (links, then notes, then folders; suggested replaces the
  // links block while the query is empty).
  const suggestedOffset = debounced ? 0 : suggested.length
  const linksOffset = suggestedOffset
  const notesOffset = linksOffset + matches.length
  const foldersOffset = notesOffset + noteMatches.length

  // Derived once so the empty state cannot fall out of sync with the group
  // list — a new group only extends `hasResults`, never a hand-written
  // conjunction at the render site.
  const hasResults =
    matches.length + noteMatches.length + tagMatches.length + folderMatches.length > 0

  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, open)

  if (!open) return null

  return (
    <div
      ref={dialogRef}
      className="fx-overlay"
      role="dialog"
      aria-modal="true"
      aria-label={t('command_palette.dialog_aria')}
      onMouseDown={(e) => {
        // Backdrop click = close. The check `e.target === e.currentTarget`
        // makes sure clicks inside the .fx-cmdk box don't bubble up here.
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className="fx-cmdk">
        <div className="fx-cmdk-input">
          <Icon d={I.search} size={18} />
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={onInputKeyDown}
            placeholder={t('command_palette.placeholder')}
            aria-label={t('command_palette.search_aria')}
            aria-activedescendant={actions.length > 0 ? `fx-cmdk-row-${highlight}` : undefined}
          />
          <span className="fx-cmdk-scope">
            {t('command_palette.scope_label_prefix')} <b>{t('command_palette.scope_label_all_tags')}</b>
          </span>
          <kbd className="fx-kbd">esc</kbd>
          {/* Touch-friendly close button. Esc covers desktop; on a phone
              there is no esc key, so this is the only way out besides the
              backdrop tap. */}
          <button
            type="button"
            className="fx-cmdk-close"
            onClick={onClose}
            aria-label={t('common.close')}
            data-tooltip={t('common.close')}
          >
            <Icon d={I.x} size={14} />
          </button>
        </div>

        <div className="fx-cmdk-results">
          {!debounced && suggested.length > 0 && (
            <div className="fx-cmdk-group">
              <div className="fx-cmdk-grouplabel">{t('command_palette.suggested_section')}</div>
              {suggested.map((l, i) => (
                <PaletteLinkRow
                  key={l.id}
                  id={`fx-cmdk-row-${i}`}
                  link={l}
                  selected={highlight === i}
                  hint={t('command_palette.clicks_count', { count: l.click_count })}
                  canEdit={canEdit}
                  folderName={l.folder_id != null ? folderNameById.get(l.folder_id) : undefined}
                  onClose={onClose}
                  onReveal={onRevealLink}
                  onEdit={onEditLink}
                />
              ))}
            </div>
          )}

          {matches.length > 0 && (
            <div className="fx-cmdk-group">
              <div className="fx-cmdk-grouplabel">{t('command_palette.links_section')}</div>
              {matches.map((l, i) => (
                <PaletteLinkRow
                  key={l.id}
                  id={`fx-cmdk-row-${linksOffset + i}`}
                  link={l}
                  selected={highlight === linksOffset + i}
                  hint={`/go/${l.slug}`}
                  canEdit={canEdit}
                  folderName={l.folder_id != null ? folderNameById.get(l.folder_id) : undefined}
                  onClose={onClose}
                  onReveal={onRevealLink}
                  onEdit={onEditLink}
                />
              ))}
            </div>
          )}

          {noteMatches.length > 0 && (
            <div className="fx-cmdk-group">
              <div className="fx-cmdk-grouplabel">{t('command_palette.notes_section')}</div>
              {noteMatches.map((n, i) => (
                <a
                  key={n.id}
                  id={`fx-cmdk-row-${notesOffset + i}`}
                  className={'fx-cmdk-row' + (highlight === notesOffset + i ? ' fx-cmdk-row-sel' : '')}
                  href={goNoteHref(n)}
                  target="_blank"
                  rel="noopener noreferrer"
                  onClick={onClose}
                >
                  <span className="fx-cmdk-note-icon" aria-hidden="true">
                    <Icon d={I.note} size={16} />
                  </span>
                  <div className="fx-cmdk-main">
                    <div className="fx-cmdk-title">{n.title}</div>
                    {n.body_text_snippet && <div className="fx-cmdk-sub">{n.body_text_snippet}</div>}
                  </div>
                  <div className="fx-cmdk-tags">
                    {n.tags.slice(0, 2).map((tag) => (
                      <TagChip key={tag.id} tag={tag} />
                    ))}
                  </div>
                  <span className="fx-cmdk-hint">/n/{n.slug}</span>
                </a>
              ))}
            </div>
          )}

          {folderMatches.length > 0 && (
            <div className="fx-cmdk-group">
              <div className="fx-cmdk-grouplabel">
                {t('command_palette.folders_section')}
                <span className="fx-cmdk-grouphint">
                  <span className="fx-cmdk-grouphint-unit">L</span> {t('command_palette.folders_hint_links')} ·{' '}
                  <span className="fx-cmdk-grouphint-unit">P</span> {t('command_palette.folders_hint_folders')}
                </span>
              </div>
              {folderMatches.map((f, i) => (
                <PaletteFolderRow
                  key={f.id}
                  id={`fx-cmdk-row-${foldersOffset + i}`}
                  folder={f}
                  selected={highlight === foldersOffset + i}
                  onOpenFolder={onOpenFolder}
                />
              ))}
            </div>
          )}

          {tagMatches.length > 0 && (
            <div className="fx-cmdk-group">
              <div className="fx-cmdk-grouplabel">{t('command_palette.tags_section')}</div>
              {tagMatches.map((tag) => (
                <div key={tag.id} className="fx-cmdk-row">
                  <span className="fx-cmdk-tagdot" style={{ background: tag.color }} />
                  <div className="fx-cmdk-main">
                    <div className="fx-cmdk-title">
                      {t('command_palette.filter_by')} <b>{tag.name}</b>
                    </div>
                    <div className="fx-cmdk-sub">{t('command_palette.tag_links_count', { count: tag.link_count ?? 0 })}</div>
                  </div>
                  <span className="fx-cmdk-hint">{t('command_palette.tag_hint')}</span>
                </div>
              ))}
            </div>
          )}

          {debounced && !hasResults && (
            <div style={{ padding: 24, textAlign: 'center', color: 'var(--fx-ink-4)', fontSize: 13 }}>
              {t('command_palette.no_results')}
            </div>
          )}
        </div>

        <div className="fx-cmdk-foot">
          <span className="fx-cmdk-foot-item">
            <kbd className="fx-kbd">↵</kbd> {t('command_palette.footer_open_go')}
          </span>
          <span className="fx-cmdk-foot-item">
            <kbd className="fx-kbd">⌘↵</kbd> {t('command_palette.footer_open_new_tab')}
          </span>
          <span className="fx-cmdk-foot-grow" />
          <span className="fx-cmdk-foot-item fx-cmdk-foot-stat">
            {t('command_palette.footer_indexed', { count: entries.length })}
          </span>
        </div>
      </div>
    </div>
  )
}

function PaletteLinkRow({  link,
  id,
  selected,
  hint,
  canEdit,
  folderName,
  onClose,
  onReveal,
  onEdit,
}: {
  link: Link
  id?: string
  selected?: boolean
  hint: string
  canEdit: boolean
  folderName?: string
  onClose: () => void
  onReveal?: (link: Link) => void
  onEdit?: (link: Link) => void
}) {
  const { t } = useTranslation()
  const inFolder = link.folder_id != null
  const revealLabel = inFolder
    ? t('command_palette.reveal_in_folder', { name: folderName || t('command_palette.folder_hint') })
    : t('command_palette.reveal_on_home')

  return (
    <div id={id} className={'fx-cmdk-link-row' + (selected ? ' fx-cmdk-row-sel' : '')}>
      <a
        className="fx-cmdk-row-go"
        href={goHref(link)}
        target="_blank"
        rel="noopener noreferrer"
        onClick={onClose}
      >
        <Favicon link={link} size={22} />
        <div className="fx-cmdk-main">
          <div className="fx-cmdk-title">{link.title}</div>
          <div className="fx-cmdk-sub">{link.url}</div>
        </div>
        <div className="fx-cmdk-tags">
          {link.tags.slice(0, 2).map((tag) => (
            <TagChip key={tag.id} tag={tag} />
          ))}
        </div>
        <span className="fx-cmdk-hint">{hint}</span>
      </a>
      <div className="fx-cmdk-row-actions">
        {onReveal && (
          <button
            type="button"
            className="fx-cmdk-icon"
            aria-label={revealLabel}
            data-tooltip={revealLabel}
            data-tooltip-side="top"
            onClick={(event) => {
              event.preventDefault()
              event.stopPropagation()
              onReveal(link)
            }}
          >
            <Icon d={inFolder ? I.folder : I.home} size={14} />
          </button>
        )}
        {canEdit && onEdit && (
          <button
            type="button"
            className="fx-cmdk-icon"
            aria-label={t('command_palette.edit_link')}
            data-tooltip={t('command_palette.edit_link')}
            data-tooltip-side="top"
            onClick={(event) => {
              event.preventDefault()
              event.stopPropagation()
              onEdit(link)
            }}
          >
            <Icon d={I.pen} size={14} />
          </button>
        )}
      </div>
    </div>
  )
}

// The folder row, extracted like PaletteLinkRow: depth guides, lock badge,
// L/P counts. Own component so the palette body stays a flat list of
// groups and the row is testable in isolation as it grows.
function PaletteFolderRow({
  folder: f,
  id,
  selected,
  onOpenFolder,
}: {
  folder: ReturnType<typeof searchFolderTree>[number]
  id?: string
  selected?: boolean
  onOpenFolder?: (id: number) => void
}) {
  const { t } = useTranslation()
  return (
    <button
      type="button"
      id={id}
      className={
        'fx-cmdk-row fx-cmdk-folder-row' + (selected ? ' fx-cmdk-row-sel' : '')
      }
      onClick={() => onOpenFolder?.(f.id)}
      aria-label={t('command_palette.open_folder_aria', { name: f.name })}
      data-tooltip={f.path.length > 1 ? f.path.join(' / ') : f.name}
    >
      {f.depth > 0 && (
        <span className="fx-cmdk-folder-indent" aria-hidden="true">
          {Array.from({ length: f.depth }).map((_, i) => (
            <span
              key={i}
              className={
                'fx-cmdk-folder-guide' +
                (i === f.depth - 1 ? ' fx-cmdk-folder-guide-last' : '')
              }
            />
          ))}
        </span>
      )}
      <span className="fx-cmdk-folder-icon" style={{ color: f.color }}>
        <Icon d={I.folder} size={14} />
      </span>
      <span className="fx-cmdk-folder-counts" aria-hidden="true">
        <span className="fx-cmdk-folder-count">
          {f.link_count}
          <span className="fx-cmdk-folder-unit">L</span>
        </span>
        {f.folder_count > 0 && (
          <span className="fx-cmdk-folder-count">
            {f.folder_count}
            <span className="fx-cmdk-folder-unit">P</span>
          </span>
        )}
      </span>
      <span className="fx-cmdk-folder-name">
        {f.has_password && (
          <span
            className="fx-folder-lock-icon"
            aria-hidden="true"
            data-tooltip={t('folder_card.locked_tooltip')}
            data-tooltip-side="top"
          >
            <Icon d={I.lock} size={12} />
          </span>
        )}
        {f.name}
      </span>
      <span className="fx-cmdk-hint">{t('command_palette.folder_hint')}</span>
    </button>
  )
}
