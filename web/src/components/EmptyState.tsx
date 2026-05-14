import { Trans, useTranslation } from 'react-i18next'
import { Icon, I } from './icons'

type Props = {
  onNewLink: () => void
  onImport: () => void
}

export function EmptyState({ onNewLink, onImport }: Props) {
  const { t } = useTranslation()
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
        <h2 className="fx-empty-h">{t('home.empty_title')}</h2>
        <p className="fx-empty-p">
          <Trans
            i18nKey="home.empty_body"
            values={{ file: 'bookmarks.html' }}
            components={{ code: <code /> }}
          />
          {' '}
          {t('home.empty_hint')}
        </p>

        <div className="fx-empty-actions">
          <button className="fx-cta fx-cta-fill" onClick={onNewLink}>
            <Icon d={I.plus} size={15} stroke={2.2} /> {t('home.add_first')}
            <kbd className="fx-kbd fx-kbd-cta">⌥N</kbd>
          </button>
          <button className="fx-btn-ghost-lg" onClick={onImport}>
            <Icon d={I.upload} size={15} /> {t('home.import_bookmarks')}
          </button>
        </div>
      </div>
    </div>
  )
}
