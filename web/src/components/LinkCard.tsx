import { useCallback, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Favicon } from './Favicon'
import { TagChip } from './TagChip'
import { Icon, I } from './icons'
import { useConfirm } from './ConfirmDialog'
import { goHref, useDeleteLink, useRefreshPreview, useUpdateLink } from '../api/links'
import type { Link } from '../api/types'

type Props = {
  link: Link
  onEdit: (l: Link) => void
  density?: 'normal' | 'short' | 'medium' | 'tall'
  // Drag-and-drop: card is the source of `dnd:link/<id>` and accepts a drop
  // FROM another link card (which triggers the link↔link merge — see App.tsx).
  onMergeWith?: (sourceId: number, targetId: number) => void
}

// Decide card height purely from how much content we have. Tall when a real
// og:image was fetched; medium when there's a description; short otherwise.
function densityFor(link: Link): 'tall' | 'medium' | 'short' {
  if (link.og_image_url) return 'tall'
  if (link.description) return 'medium'
  return 'short'
}

export function LinkCard({ link, onEdit, onMergeWith }: Props) {
  const del = useDeleteLink()
  const refresh = useRefreshPreview()
  const update = useUpdateLink()
  const confirm = useConfirm()
  const qc = useQueryClient()
  const density = densityFor(link)
  const togglePin = () => update.mutate({ id: link.id, body: { pinned: !link.pinned } })
  const [dragOver, setDragOver] = useState(false)
  const [dragging, setDragging] = useState(false)

  // Invalidate after a short delay so the backend has time to log the click
  const onGo = useCallback(() => {
    setTimeout(() => qc.invalidateQueries({ queryKey: ['links'] }), 800)
  }, [qc])

  const onDelete = async () => {
    const ok = await confirm({
      title: `Apagar "${link.title}"?`,
      message: (
        <>
          O link <code>{link.url}</code> e seu histórico de cliques serão removidos
          permanentemente. As tags associadas continuam.
        </>
      ),
      confirmLabel: 'Apagar link',
      destructive: true,
    })
    if (ok) del.mutate(link.id)
  }

  return (
    <article
      className={
        'fx-card fx-card-' + density +
        (link.pinned ? ' fx-card-pinned' : '') +
        (dragging ? ' fx-card-dragging' : '') +
        (dragOver ? ' fx-card-drop-over' : '')
      }
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData('application/x-foldex-link', String(link.id))
        e.dataTransfer.effectAllowed = 'move'
        setDragging(true)
      }}
      onDragEnd={() => setDragging(false)}
      onDragOver={(e) => {
        // Accept only OTHER link cards. Folder targets handle their own drop.
        const raw = e.dataTransfer.types
        if (!raw.includes('application/x-foldex-link')) return
        e.preventDefault()
        e.dataTransfer.dropEffect = 'move'
      }}
      onDragEnter={(e) => {
        if (e.dataTransfer.types.includes('application/x-foldex-link')) setDragOver(true)
      }}
      onDragLeave={(e) => {
        // Only clear when the leave is to outside the card (not a child).
        const next = e.relatedTarget as Node | null
        if (!next || !(e.currentTarget as Node).contains(next)) setDragOver(false)
      }}
      onDrop={(e) => {
        setDragOver(false)
        const raw = e.dataTransfer.getData('application/x-foldex-link')
        const sourceId = Number(raw)
        if (!sourceId || sourceId === link.id) return
        e.preventDefault()
        onMergeWith?.(sourceId, link.id)
      }}
    >
      <button
        className={'fx-card-pin-badge' + (link.pinned ? '' : ' fx-card-pin-off')}
        onClick={(e) => {
          e.stopPropagation()
          togglePin()
        }}
        aria-label={link.pinned ? 'unpin' : 'pin'}
        data-tooltip={link.pinned ? 'Desafixar' : 'Fixar no topo'}
        data-tooltip-side="left"
      >
        <Icon d={I.pin} size={13} stroke={2} />
      </button>

      {link.og_image_url && (
        <a className="fx-preview fx-preview-img" href={goHref(link.id)} target="_blank" rel="noopener noreferrer" onClick={onGo}>
          <img
            src={link.og_image_url}
            alt=""
            referrerPolicy="no-referrer"
            style={{ width: '100%', height: '100%', objectFit: 'scale-down', display: 'block' }}
          />
        </a>
      )}
      <div className="fx-card-body">
        <header className="fx-card-head">
          <Favicon link={link} size={link.og_image_url ? 28 : 36} />
          <div className="fx-card-head-text">
            <h3 className="fx-card-title">
              <a href={goHref(link.id)} target="_blank" rel="noopener noreferrer" className="fx-card-title-link" onClick={onGo}>
                {link.title}
              </a>
            </h3>
            <div className="fx-card-host">{hostOf(link.url)}</div>
          </div>
        </header>

        {link.description && <p className="fx-card-desc">{link.description}</p>}

        {link.tags.length > 0 && (
          <div className="fx-card-tags">
            {link.tags.map((t) => (
              <TagChip key={t.id} tag={t} />
            ))}
          </div>
        )}

        <footer className="fx-card-foot">
          <div className="fx-card-meta">
            <span className="fx-meta-stat" data-tooltip="Cliques" aria-label="Cliques">
              <Icon d={I.flame} size={13} /> {link.click_count}
            </span>
            <span className="fx-meta-sep" />
            <span className="fx-meta-stat" data-tooltip="Último clique" aria-label="Último clique">
              <Icon d={I.clock} size={13} /> {lastClick(link)}
            </span>
            {link.preview_status === 'failed' && !link.og_image_url && (
              <>
                <span className="fx-meta-sep" />
                <span className="fx-meta-warn">
                  <Icon d={I.alert} size={13} /> preview falhou
                </span>
              </>
            )}
            {link.preview_status === 'pending' && (
              <>
                <span className="fx-meta-sep" />
                <span className="fx-meta-stat" style={{ color: 'var(--fx-warn)' }}>
                  <Icon d={I.clock} size={13} /> capturando…
                </span>
              </>
            )}
          </div>

          <div className="fx-card-actions">
            {link.preview_status !== 'ok' && (
              <button
                className="fx-iconbtn"
                data-tooltip="Recapturar preview"
                data-tooltip-side="top"
                aria-label="refresh preview"
                onClick={() => refresh.mutate(link.id)}
              >
                <Icon d={I.refresh} size={14} />
              </button>
            )}
            <button
              className="fx-iconbtn"
              data-tooltip="Editar link"
              data-tooltip-side="top"
              aria-label="edit"
              onClick={() => onEdit(link)}
            >
              <Icon d={I.pen} size={14} />
            </button>
            <button
              className="fx-iconbtn fx-iconbtn-danger"
              data-tooltip="Apagar link"
              data-tooltip-side="top"
              aria-label="delete"
              onClick={onDelete}
            >
              <Icon d={I.trash} size={14} />
            </button>
            <a
              className="fx-openbtn"
              href={goHref(link.id)}
              target="_blank"
              rel="noopener noreferrer"
              data-tooltip="Acessar"
              data-tooltip-side="top"
              aria-label={`open ${link.title}`}
              onClick={onGo}
            >
              <span className="fx-openbtn-go">Acessar</span>
              <Icon d={I.arrowR} size={14} />
            </a>
          </div>
        </footer>
      </div>
    </article>
  )
}

function hostOf(u: string) {
  try {
    return new URL(u).hostname.replace(/^www\./, '')
  } catch {
    return u
  }
}

function lastClick(link: Link): string {
  if (!link.last_clicked_at) return 'nunca'
  const ms = Date.now() - new Date(link.last_clicked_at).getTime()
  const min = Math.round(ms / 60000)
  if (min < 1) return 'agora'
  if (min < 60) return `há ${min}min`
  const h = Math.round(min / 60)
  if (h < 24) return `há ${h}h`
  const d = Math.round(h / 24)
  if (d === 1) return 'ontem'
  if (d < 30) return `há ${d}d`
  return new Date(link.last_clicked_at).toLocaleDateString('pt-BR')
}
