import { useEffect, useMemo, useRef, useState } from 'react'
import { Icon, I } from './icons'
import { Favicon } from './Favicon'
import { TagChip } from './TagChip'
import { goHref, useLinks } from '../api/links'
import { useTags } from '../api/tags'
import { useFolders } from '../api/folders'
import { searchFolderTree } from '../lib/folderTree'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'

type Props = {
  open: boolean
  onClose: () => void
  onOpenFolder?: (id: number) => void
}

export function CommandPalette({ open, onClose, onOpenFolder }: Props) {
  const [q, setQ] = useState('')
  const [debounced, setDebounced] = useState('')

  useEffect(() => {
    if (!open) {
      setQ('')
      setDebounced('')
      return
    }
    const id = setTimeout(() => setDebounced(q), 200)
    return () => clearTimeout(id)
  }, [open, q])

  // ESC closes — routed through the global useEscape stack so when the
  // palette sits on top of a folder view, Esc fires THIS handler (not the
  // folder's navigateBack).
  useEscape(onClose, open)

  const { data: links = [] } = useLinks({ q: debounced }, { enabled: open })
  const { data: tags = [] } = useTags()
  const { data: folders = [] } = useFolders()

  const suggested = useMemo(() => [...links].sort((a, b) => b.click_count - a.click_count).slice(0, 3), [links])
  const matches = useMemo(() => links.slice(0, 12), [links])
  const tagMatches = useMemo(() => {
    if (!debounced) return []
    const f = debounced.toLowerCase()
    return tags.filter((t) => t.name.toLowerCase().includes(f)).slice(0, 4)
  }, [tags, debounced])
  // Hierarchical folder rendering — depth-aware so the user sees the tree
  // shape, with the full ancestor path attached to each match for context
  // when the search query lands deep in the tree.
  const folderMatches = useMemo(() => {
    if (!folders.length) return []
    return searchFolderTree(folders, debounced).slice(0, 8)
  }, [folders, debounced])

  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, open)

  if (!open) return null

  return (
    <div ref={dialogRef} className="fx-overlay" role="dialog" aria-modal="true" aria-label="Command palette">
      <div className="fx-cmdk">
        <div className="fx-cmdk-input">
          <Icon d={I.search} size={18} />
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Buscar por título, URL, tag, ação…"
            aria-label="search"
          />
          <span className="fx-cmdk-scope">
            em <b>todas as tags</b>
          </span>
          <kbd className="fx-kbd">esc</kbd>
        </div>

        <div className="fx-cmdk-results">
          {!debounced && suggested.length > 0 && (
            <div className="fx-cmdk-group">
              <div className="fx-cmdk-grouplabel">Sugeridos · mais clicados</div>
              {suggested.map((l, i) => (
                <a
                  key={l.id}
                  className={'fx-cmdk-row' + (i === 0 ? ' fx-cmdk-row-sel' : '')}
                  href={goHref(l.id)}
                  target="_blank"
                  rel="noopener noreferrer"
                  onClick={onClose}
                >
                  <Favicon link={l} size={22} />
                  <div className="fx-cmdk-main">
                    <div className="fx-cmdk-title">{l.title}</div>
                    <div className="fx-cmdk-sub">{l.url}</div>
                  </div>
                  <div className="fx-cmdk-tags">
                    {l.tags.slice(0, 2).map((t) => (
                      <TagChip key={t.id} tag={t} />
                    ))}
                  </div>
                  <span className="fx-cmdk-hint">{l.click_count} cliques</span>
                </a>
              ))}
            </div>
          )}

          {matches.length > 0 && (
            <div className="fx-cmdk-group">
              <div className="fx-cmdk-grouplabel">Links</div>
              {matches.map((l) => (
                <a
                  key={l.id}
                  className="fx-cmdk-row"
                  href={goHref(l.id)}
                  target="_blank"
                  rel="noopener noreferrer"
                  onClick={onClose}
                >
                  <Favicon link={l} size={22} />
                  <div className="fx-cmdk-main">
                    <div className="fx-cmdk-title">{l.title}</div>
                    <div className="fx-cmdk-sub">{l.url}</div>
                  </div>
                  <div className="fx-cmdk-tags">
                    {l.tags.slice(0, 2).map((t) => (
                      <TagChip key={t.id} tag={t} />
                    ))}
                  </div>
                  <span className="fx-cmdk-hint">/go/{l.id}</span>
                </a>
              ))}
            </div>
          )}

          {folderMatches.length > 0 && (
            <div className="fx-cmdk-group">
              <div className="fx-cmdk-grouplabel">
                Pastas
                <span className="fx-cmdk-grouphint">
                  <span className="fx-cmdk-grouphint-unit">L</span> links ·{' '}
                  <span className="fx-cmdk-grouphint-unit">P</span> pastas
                </span>
              </div>
              {folderMatches.map((f) => (
                <button
                  key={f.id}
                  type="button"
                  className="fx-cmdk-row fx-cmdk-folder-row"
                  onClick={() => onOpenFolder?.(f.id)}
                  aria-label={`abrir pasta ${f.name}`}
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
                  <span className="fx-cmdk-folder-name">{f.name}</span>
                  <span className="fx-cmdk-hint">Pasta</span>
                </button>
              ))}
            </div>
          )}

          {tagMatches.length > 0 && (
            <div className="fx-cmdk-group">
              <div className="fx-cmdk-grouplabel">Tags</div>
              {tagMatches.map((t) => (
                <div key={t.id} className="fx-cmdk-row">
                  <span className="fx-cmdk-tagdot" style={{ background: t.color }} />
                  <div className="fx-cmdk-main">
                    <div className="fx-cmdk-title">
                      Filtrar por <b>{t.name}</b>
                    </div>
                    <div className="fx-cmdk-sub">{t.link_count ?? 0} links</div>
                  </div>
                  <span className="fx-cmdk-hint">Tag</span>
                </div>
              ))}
            </div>
          )}

          {debounced && matches.length === 0 && tagMatches.length === 0 && folderMatches.length === 0 && (
            <div style={{ padding: 24, textAlign: 'center', color: 'var(--fx-ink-4)', fontSize: 13 }}>
              no matches
            </div>
          )}
        </div>

        <div className="fx-cmdk-foot">
          <span className="fx-cmdk-foot-item">
            <kbd className="fx-kbd">↵</kbd> abrir via /go
          </span>
          <span className="fx-cmdk-foot-item">
            <kbd className="fx-kbd">⌘↵</kbd> abrir em nova aba
          </span>
          <span className="fx-cmdk-foot-grow" />
          <span className="fx-cmdk-foot-item fx-cmdk-foot-stat">
            ⌥K · {links.length} links indexados
          </span>
        </div>
      </div>
    </div>
  )
}
