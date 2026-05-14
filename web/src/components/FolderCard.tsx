import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { primaryColor } from '../lib/tagColor'
import type { Folder, PreviewTile, PreviewFolderTile } from '../api/types'

// A single tile inside the 2x2 preview grid. Either a link (with image or
// favicon) or a sub-folder (mini iPhone-style folder icon in the parent's
// color), or empty (dashed placeholder).
type Tile =
  | { kind: 'link'; data: PreviewTile }
  | { kind: 'folder'; data: PreviewFolderTile }
  | { kind: 'empty' }

type Props = {
  folder: Folder
  onOpen: (id: number) => void
  onEdit?: (folder: Folder) => void
  // Called when a link card is dropped on this folder. App.tsx handles the
  // PATCH and query invalidation; FolderCard only signals the gesture.
  onDropLink?: (linkId: number, folderId: number) => void
  // Called when ANOTHER folder card is dropped on this one. Source becomes
  // child of target. App.tsx checks for cycles (target descendant of source)
  // before issuing the PATCH — FolderCard only signals the gesture.
  onDropFolder?: (sourceId: number, targetId: number) => void
}

const MIME_LINK = 'application/x-foldex-link'
const MIME_FOLDER = 'application/x-foldex-folder'

// iPhone-style folder card: 2x2 grid of mini-thumbnails (preview_links) inside
// the preview area, folder name + link count in the body. Empty folder shows
// dashed tiles + "Pasta vazia" label.
export function FolderCard({ folder, onOpen, onEdit, onDropLink, onDropFolder }: Props) {
  const { t } = useTranslation()
  const tiles = mixTiles(folder.preview_links, folder.preview_folders)
  const total = folder.link_count + folder.folder_count
  const overflow = Math.max(0, total - 4)
  const accent = primaryColor(folder.color)
  const [dragOver, setDragOver] = useState(false)
  const [dragging, setDragging] = useState(false)

  const acceptsDrop = (types: ReadonlyArray<string>): 'link' | 'folder' | null => {
    if (types.includes(MIME_LINK)) return 'link'
    if (types.includes(MIME_FOLDER)) return 'folder'
    return null
  }

  return (
    <div
      className={
        'fx-card fx-folder-card' +
        (dragOver ? ' fx-card-drop-over' : '') +
        (dragging ? ' fx-card-dragging' : '')
      }
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData(MIME_FOLDER, String(folder.id))
        e.dataTransfer.effectAllowed = 'move'
        setDragging(true)
      }}
      onDragEnd={() => setDragging(false)}
      onDragOver={(e) => {
        const kind = acceptsDrop(Array.from(e.dataTransfer.types))
        if (!kind) return
        // Same-folder drop = no-op (signals the user is dragging the card
        // back to itself); don't show the highlight.
        if (kind === 'folder') {
          const raw = e.dataTransfer.getData(MIME_FOLDER)
          if (raw && Number(raw) === folder.id) return
        }
        e.preventDefault()
        e.dataTransfer.dropEffect = 'move'
      }}
      onDragEnter={(e) => {
        const kind = acceptsDrop(Array.from(e.dataTransfer.types))
        if (!kind) return
        if (kind === 'folder') {
          // dataTransfer.getData is empty during dragenter on some browsers,
          // so we accept the highlight here and re-check on drop.
        }
        setDragOver(true)
      }}
      onDragLeave={(e) => {
        const next = e.relatedTarget as Node | null
        if (!next || !(e.currentTarget as Node).contains(next)) setDragOver(false)
      }}
      onDrop={(e) => {
        setDragOver(false)
        const linkRaw = e.dataTransfer.getData(MIME_LINK)
        const folderRaw = e.dataTransfer.getData(MIME_FOLDER)
        if (linkRaw) {
          const sourceId = Number(linkRaw)
          if (!sourceId) return
          e.preventDefault()
          onDropLink?.(sourceId, folder.id)
          return
        }
        if (folderRaw) {
          const sourceId = Number(folderRaw)
          if (!sourceId || sourceId === folder.id) return
          e.preventDefault()
          onDropFolder?.(sourceId, folder.id)
        }
      }}
    >
      <button
        type="button"
        className="fx-folder-preview"
        onClick={() => onOpen(folder.id)}
        aria-label={t('folder_card.open_folder_aria', { name: folder.name })}
        style={{ ['--fx-folder-accent' as never]: accent, background: folder.color }}
      >
        <div className="fx-folder-tiles">
          {tiles.map((tile, i) => (
            <FolderTile key={i} tile={tile} overflow={i === 3 ? overflow : 0} />
          ))}
        </div>
        {total === 0 && (
          <span className="fx-folder-empty-label">{t('folder_card.empty')}</span>
        )}
      </button>
      <div className="fx-card-body">
        <header className="fx-card-head">
          <div className="fx-card-head-text">
            <h3 className="fx-card-title">
              <button
                type="button"
                className="fx-card-title-link"
                onClick={() => onOpen(folder.id)}
              >
                {folder.name}
              </button>
            </h3>
            <div className="fx-card-host">
              {t('folder_card.links_count', { count: folder.link_count })}
              {folder.folder_count > 0 && (
                <>
                  {' · '}
                  {t('folder_card.folders_count', { count: folder.folder_count })}
                </>
              )}
            </div>
          </div>
        </header>
        <footer className="fx-card-foot">
          <div className="fx-card-meta">
            <span className="fx-meta-stat" data-tooltip={t('folder_card.folder_label_tooltip')}>
              <Icon d={I.folder} size={13} /> {t('folder_card.folder_label')}
            </span>
          </div>
          <div className="fx-card-actions">
            {onEdit && (
              <button
                className="fx-iconbtn"
                data-tooltip={t('folder_card.edit_folder')}
                data-tooltip-side="top"
                aria-label="edit folder"
                onClick={() => onEdit(folder)}
              >
                <Icon d={I.pen} size={14} />
              </button>
            )}
            <button
              className="fx-iconbtn fx-iconbtn-primary"
              data-tooltip={t('folder_card.open_folder')}
              data-tooltip-side="top"
              aria-label="open folder"
              onClick={() => onOpen(folder.id)}
            >
              <Icon d={I.arrowR} size={14} stroke={2.2} />
            </button>
          </div>
        </footer>
      </div>
    </div>
  )
}

function FolderTile({ tile, overflow }: { tile: Tile; overflow: number }) {
  if (tile.kind === 'empty') {
    return <div className="fx-folder-tile fx-folder-tile-empty" />
  }
  if (tile.kind === 'folder') {
    return (
      <div className="fx-folder-tile fx-folder-tile-subfolder" style={{ color: tile.data.color }}>
        <Icon d={I.folder} size={22} />
        {overflow > 0 && <span className="fx-folder-tile-more">+{overflow}</span>}
      </div>
    )
  }
  const link = tile.data
  return (
    <div className="fx-folder-tile">
      {link.og_image_url ? (
        <img src={link.og_image_url} alt="" referrerPolicy="no-referrer" />
      ) : link.favicon_url ? (
        <img src={link.favicon_url} alt="" referrerPolicy="no-referrer" className="fx-folder-tile-favicon" />
      ) : (
        <span className="fx-folder-tile-letter">{(link.title[0] ?? '?').toUpperCase()}</span>
      )}
      {overflow > 0 && <span className="fx-folder-tile-more">+{overflow}</span>}
    </div>
  )
}

// Mix link + subfolder previews into 4 slots. Links first (the more visual
// content), then subfolders. Pad with `empty` placeholders if neither fills
// the grid (only happens when both arrays are empty — handled separately
// with the "Pasta vazia" label).
function mixTiles(links: PreviewTile[], folders: PreviewFolderTile[]): Tile[] {
  const tiles: Tile[] = []
  for (const l of links) {
    if (tiles.length >= 4) break
    tiles.push({ kind: 'link', data: l })
  }
  for (const f of folders) {
    if (tiles.length >= 4) break
    tiles.push({ kind: 'folder', data: f })
  }
  while (tiles.length < 4) tiles.push({ kind: 'empty' })
  return tiles
}
