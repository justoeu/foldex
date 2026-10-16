import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { useCommandPaletteSearch, type PaletteAction } from '../hooks/useCommandPaletteSearch'
import { useHasPermission } from '../auth/AuthProvider'
import { PaletteGroups } from './command-palette/PaletteGroups'
import type { Link } from '../api/types'

type Props = Readonly<{
  open: boolean
  onClose: () => void
  onOpenFolder?: (id: number) => void
  onRevealLink?: (link: Link) => void
  onEditLink?: (link: Link) => void
  onViewNote?: (id: number) => void
  onSelectTag?: (tag: { id: number; name: string }) => void
}>

export function CommandPalette({ open, onClose, onOpenFolder, onRevealLink, onEditLink, onViewNote, onSelectTag }: Props) {
  const { t } = useTranslation()
  const canEdit = useHasPermission('content.write')
  const [highlight, setHighlight] = useState(0)
  const search = useCommandPaletteSearch(open)
  const { q, setQ, debounced, entries, actions } = search

  // Every new result set resets the cursor — row 0 is the natural target of
  // the footer's bare ↵ promise.
  useEffect(() => {
    setHighlight(0)
  }, [debounced, open])

  // ESC closes — routed through the global useEscape stack so when the
  // palette sits on top of a folder view, Esc fires THIS handler (not the
  // folder's navigateBack).
  useEscape(onClose, open)

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
    if (action.kind === 'note') {
      onViewNote?.(action.id)
      onClose()
      return
    }
    if (action.kind === 'tag') {
      onSelectTag?.({ id: action.id, name: action.name })
      onClose()
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
    >
      <button
        type="button"
        className="fx-overlay-backdrop"
        aria-label={t('common.close')}
        onClick={onClose}
      />
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

        <PaletteGroups
          search={search}
          highlight={highlight}
          canEdit={canEdit}
          onClose={onClose}
          onOpenFolder={onOpenFolder}
          onRevealLink={onRevealLink}
          onViewNote={onViewNote}
          onSelectTag={onSelectTag}
          onEditLink={onEditLink}
        />

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
