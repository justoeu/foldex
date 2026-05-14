import { Icon, I } from './icons'

type Props = {
  onNewLink: () => void
  onImport: () => void
}

export function EmptyState({ onNewLink, onImport }: Props) {
  return (
    <div className="fx-empty">
      <div className="fx-empty-card">
        <div className="fx-empty-rocket">
          <svg viewBox="0 0 80 80" width="80" height="80" aria-hidden="true">
            <defs>
              <linearGradient id="er" x1="0" y1="0" x2="1" y2="1">
                <stop offset="0" stopColor="#A78BFA" />
                <stop offset="1" stopColor="#6366F1" />
              </linearGradient>
            </defs>
            <path
              d="M40 8c10 6 16 18 16 30 0 6-3 12-8 16H32c-5-4-8-10-8-16 0-12 6-24 16-30z"
              fill="url(#er)"
            />
            <circle cx="40" cy="32" r="6" fill="#fff" opacity="0.92" />
            <circle cx="40" cy="32" r="3" fill="#6366F1" />
            <path d="M24 54l-6 10 12-4M56 54l6 10-12-4" fill="#A78BFA" opacity="0.7" />
            <path d="M34 64h12l-2 8h-8z" fill="#F59E0B" />
          </svg>
        </div>
        <div className="fx-empty-kicker">3 · 2 · 1 …</div>
        <h2 className="fx-empty-h">Sua base ainda está vazia.</h2>
        <p className="fx-empty-p">
          Cole uma URL, importe seu <code>bookmarks.html</code> ou instale a extensão. Foldex resolve
          título, og:image e favicon enquanto você organiza por tags.
        </p>

        <div className="fx-empty-actions">
          <button className="fx-cta fx-cta-fill" onClick={onNewLink}>
            <Icon d={I.plus} size={15} stroke={2.2} /> Adicionar primeiro link
            <kbd className="fx-kbd fx-kbd-cta">⌥N</kbd>
          </button>
          <button className="fx-btn-ghost-lg" onClick={onImport}>
            <Icon d={I.upload} size={15} /> Importar bookmarks.html
          </button>
        </div>
      </div>
    </div>
  )
}
