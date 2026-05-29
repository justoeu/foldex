import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { TagDialog } from './TagDialog'
import { useConfirm } from './ConfirmDialog'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { useDeleteTag, useTags } from '../api/tags'
import type { Tag } from '../api/types'

type Props = {
  open: boolean
  onClose: () => void
}

// One-stop modal for editing and deleting tags. Each row shows the colored
// dot, the name, the link count and inline edit/delete buttons. Clicking
// "edit" swaps to the TagDialog (in edit mode); deleting goes through the
// shared ConfirmDialog and respects the FK ON DELETE CASCADE on link_tag.
export function TagManagerDialog({ open, onClose }: Props) {
  const { t } = useTranslation()
  const { data: tags = [] } = useTags()
  const del = useDeleteTag()
  const confirm = useConfirm()
  const [editing, setEditing] = useState<Tag | null>(null)
  const [creating, setCreating] = useState(false)

  useEscape(onClose, open)
  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, open)
  if (!open) return null

  const askDelete = async (tag: Tag) => {
    const linkCount = tag.link_count ?? 0
    const ok = await confirm({
      title: t('tag_manager.delete_confirm_title', { name: tag.name }),
      message: linkCount > 0
        ? t('tag_manager.delete_confirm_with_links', { count: linkCount })
        : t('tag_manager.delete_confirm_no_links'),
      confirmLabel: t('tag_manager.delete_confirm_action'),
      destructive: true,
    })
    if (ok) del.mutate(tag.id)
  }

  const sorted = [...tags].sort((a, b) => (b.link_count ?? 0) - (a.link_count ?? 0))

  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={t('tag_manager.title')}
    >
      <div className="fx-modal fx-tagmgr">
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker fx-modal-kicker-info">{t('tag_manager.kicker')}</div>
            <h2 className="fx-modal-title">{t('tag_manager.title')}</h2>
          </div>
          <button className="fx-confirm-x" onClick={onClose} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-tagmgr-body">
          {sorted.length === 0 && (
            <div className="fx-tagmgr-empty">{t('tag_manager.empty')}</div>
          )}
          {sorted.map((tag) => (
            <div key={tag.id} className="fx-tagmgr-row">
              <span className="fx-tagmgr-dot" style={{ background: tag.color }} />
              <div className="fx-tagmgr-text">
                <div className="fx-tagmgr-name">
                  {tag.icon && <span style={{ marginRight: 4 }}>{tag.icon}</span>}
                  {tag.name}
                </div>
                <div className="fx-tagmgr-meta">
                  {t('tag_manager.links_count', { count: tag.link_count ?? 0 })}
                </div>
              </div>
              <button
                className="fx-iconbtn"
                aria-label={t('common.edit_tag_aria', { name: tag.name })}
                data-tooltip={t('tag_manager.edit_tooltip')}
                data-tooltip-side="top"
                onClick={() => setEditing(tag)}
              >
                <Icon d={I.pen} size={13} />
              </button>
              <button
                className="fx-iconbtn fx-iconbtn-danger"
                aria-label={t('common.delete_tag_aria', { name: tag.name })}
                data-tooltip={t('tag_manager.delete_tooltip')}
                data-tooltip-side="top"
                onClick={() => askDelete(tag)}
              >
                <Icon d={I.trash} size={13} />
              </button>
            </div>
          ))}
        </div>

        <footer className="fx-tagmgr-foot">
          <button
            className="fx-confirm-btn fx-confirm-btn-primary"
            onClick={() => setCreating(true)}
          >
            <Icon d={I.plus} size={13} />
            {t('tag_manager.new_button')}
          </button>
          <button className="fx-confirm-btn" onClick={onClose}>
            {t('common.close')}
          </button>
        </footer>
      </div>

      <TagDialog open={!!editing} tag={editing} onClose={() => setEditing(null)} />
      <TagDialog open={creating} onClose={() => setCreating(false)} />
    </div>
  )
}
