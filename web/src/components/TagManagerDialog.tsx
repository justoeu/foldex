import { useRef, useState } from 'react'
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
  const { data: tags = [] } = useTags()
  const del = useDeleteTag()
  const confirm = useConfirm()
  const [editing, setEditing] = useState<Tag | null>(null)
  const [creating, setCreating] = useState(false)

  useEscape(onClose, open)
  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, open)
  if (!open) return null

  const askDelete = async (t: Tag) => {
    const linkCount = t.link_count ?? 0
    const ok = await confirm({
      title: `Apagar tag "${t.name}"?`,
      message: (
        <>
          {linkCount > 0 ? (
            <>
              Os <b>{linkCount}</b> link{linkCount === 1 ? '' : 's'} associado
              {linkCount === 1 ? '' : 's'} permanecerão — apenas a associação com essa tag será
              removida.
            </>
          ) : (
            <>Essa tag não tem links associados.</>
          )}
        </>
      ),
      confirmLabel: 'Apagar tag',
      destructive: true,
    })
    if (ok) del.mutate(t.id)
  }

  const sorted = [...tags].sort((a, b) => (b.link_count ?? 0) - (a.link_count ?? 0))

  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label="Manage tags"
    >
      <div className="fx-modal fx-tagmgr">
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker fx-modal-kicker-info">⚙ Tags</div>
            <h2 className="fx-modal-title">Gerenciar tags</h2>
          </div>
          <button className="fx-confirm-x" onClick={onClose} aria-label="close">
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-tagmgr-body">
          {sorted.length === 0 && (
            <div className="fx-tagmgr-empty">Nenhuma tag criada ainda.</div>
          )}
          {sorted.map((t) => (
            <div key={t.id} className="fx-tagmgr-row">
              <span className="fx-tagmgr-dot" style={{ background: t.color }} />
              <div className="fx-tagmgr-text">
                <div className="fx-tagmgr-name">
                  {t.icon && <span style={{ marginRight: 4 }}>{t.icon}</span>}
                  {t.name}
                </div>
                <div className="fx-tagmgr-meta">
                  {t.link_count ?? 0} link{(t.link_count ?? 0) === 1 ? '' : 's'}
                </div>
              </div>
              <button
                className="fx-iconbtn"
                aria-label={`edit ${t.name}`}
                data-tooltip="Editar tag"
                data-tooltip-side="top"
                onClick={() => setEditing(t)}
              >
                <Icon d={I.pen} size={13} />
              </button>
              <button
                className="fx-iconbtn fx-iconbtn-danger"
                aria-label={`delete ${t.name}`}
                data-tooltip="Apagar tag"
                data-tooltip-side="top"
                onClick={() => askDelete(t)}
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
            Nova tag
          </button>
          <button className="fx-confirm-btn" onClick={onClose}>
            Fechar
          </button>
        </footer>
      </div>

      <TagDialog open={!!editing} tag={editing} onClose={() => setEditing(null)} />
      <TagDialog open={creating} onClose={() => setCreating(false)} />
    </div>
  )
}
