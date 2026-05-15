import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { LocalePicker } from './LocalePicker'

type View = 'home' | 'import' | 'stats'
type Sort = 'created' | 'clicks' | 'recent' | 'alpha' | 'alpha_desc'
type ViewMode = 'cards' | 'compact' | 'list'

type Props = {
  view: View
  setView: (v: View) => void
  // Home click is special: it not only switches to view='home' but also exits
  // any open folder. Without this, clicking 🏠 from inside a folder is a
  // no-op (view is already 'home', only openFolder needs reset).
  onHome: () => void
  // Hamburger button on mobile — opens the sidebar drawer in App.tsx.
  onOpenMobileSidebar?: () => void
  q: string
  setQ: (v: string) => void
  onOpenPalette: () => void
  sort: Sort
  setSort: (s: Sort) => void
  viewMode: ViewMode
  setViewMode: (m: ViewMode) => void
  gridCols: 3 | 5 | 8
  setGridCols: (n: 3 | 5 | 8) => void
  onNewLink: () => void
  onNewFolder: () => void
  dark: boolean
  setDark: (d: boolean) => void
}

export function Topbar({
  view,
  setView,
  onHome,
  onOpenMobileSidebar,
  q,
  setQ,
  onOpenPalette,
  sort,
  setSort,
  viewMode,
  setViewMode,
  gridCols,
  setGridCols,
  onNewLink,
  onNewFolder,
  dark,
  setDark,
}: Props) {
  const { t } = useTranslation()
  return (
    <header className="fx-topbar">
      {/* Hamburger only paints on ≤768px viewports (CSS-controlled). On
          desktop it's hidden so the existing 9-column grid stays intact. */}
      <button
        className="fx-topbar-hamburger"
        aria-label={t('sidebar.expand')}
        data-tooltip={t('sidebar.expand')}
        onClick={onOpenMobileSidebar}
      >
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
          <line x1="3" y1="6"  x2="21" y2="6"  />
          <line x1="3" y1="12" x2="21" y2="12" />
          <line x1="3" y1="18" x2="21" y2="18" />
        </svg>
      </button>
      <div className="fx-brand">
        <div className="fx-brand-mark">
          <svg viewBox="0 0 24 24" width="26" height="26" aria-hidden="true">
            <defs>
              <linearGradient id="fx-bm" x1="0" y1="0" x2="1" y2="1">
                <stop offset="0" stopColor="#A78BFA" />
                <stop offset="1" stopColor="#6366F1" />
              </linearGradient>
            </defs>
            <rect x="0.5" y="0.5" width="23" height="23" rx="6.5" fill="url(#fx-bm)" />
            <path
              d="M11.6 6.6h-1.4c-1 0-1.7.7-1.7 1.7v1.3H7v2.2h1.5V18h2.3v-6.2h2v-2.2h-2V8.7c0-.3.2-.5.5-.5h1.3V6.6h-1z"
              fill="#fff"
            />
            <path
              d="M18.6 18h-2.2l-1-1.7-1 1.7h-2.2l2.1-3.2-2-3.1h2.2l.9 1.6.9-1.6h2.2l-2 3.1 2.1 3.2z"
              fill="#fff"
            />
          </svg>
        </div>
        <div className="fx-brand-text">
          <div className="fx-brand-name">{t('app.name')}</div>
          <div className="fx-brand-sub">{t('app.tagline')}</div>
        </div>
      </div>

      <nav className="fx-quicknav">
        <button
          className={'fx-qn' + (view === 'home' ? ' fx-qn-active' : '')}
          aria-label={t('topbar.home')}
          data-tooltip={t('topbar.home')}
          data-tooltip-side="bottom"
          onClick={onHome}
        >
          <Icon d={I.home} size={16} />
        </button>
        <button
          className={'fx-qn' + (view === 'stats' ? ' fx-qn-active' : '')}
          aria-label={t('topbar.stats')}
          data-tooltip={t('topbar.stats')}
          onClick={() => setView('stats')}
        >
          <svg
            width="16"
            height="16"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
          >
            <path d="M3 3v18h18" />
            <path d="M7 14l4-5 3 3 5-7" />
          </svg>
        </button>
        <button
          className={'fx-qn' + (view === 'import' ? ' fx-qn-active' : '')}
          aria-label={t('topbar.import_export')}
          data-tooltip={t('topbar.import_export')}
          onClick={() => setView('import')}
        >
          <Icon d={I.upload} size={16} />
        </button>
      </nav>

      <div className="fx-search" onClick={onOpenPalette}>
        <Icon d={I.search} size={16} />
        <input
          placeholder={t('topbar.search_placeholder')}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          aria-label={t('common.search')}
        />
        <kbd className="fx-kbd">⌥K</kbd>
      </div>

      <div className="fx-segment fx-segment-icon" role="group" aria-label={t('sort.label', { defaultValue: 'sort' })}>
        <button
          className={'fx-seg fx-seg-icon' + (sort === 'created' ? ' fx-seg-active' : '')}
          onClick={() => setSort('created')}
          aria-pressed={sort === 'created'}
          aria-label={t('sort.new')}
          data-tooltip={t('sort.new')}
        >
          <Icon d={I.sparkles} size={14} />
        </button>
        <button
          className={'fx-seg fx-seg-icon' + (sort === 'clicks' ? ' fx-seg-active' : '')}
          onClick={() => setSort('clicks')}
          aria-pressed={sort === 'clicks'}
          aria-label={t('sort.top')}
          data-tooltip={t('sort.top')}
        >
          <Icon d={I.flame} size={14} />
        </button>
        <button
          className={'fx-seg fx-seg-icon' + (sort === 'recent' ? ' fx-seg-active' : '')}
          onClick={() => setSort('recent')}
          aria-pressed={sort === 'recent'}
          aria-label={t('sort.recent')}
          data-tooltip={t('sort.recent')}
        >
          <Icon d={I.clock} size={14} />
        </button>
        <button
          className={'fx-seg fx-seg-alpha' + (sort === 'alpha' ? ' fx-seg-active' : '')}
          onClick={() => setSort('alpha')}
          aria-pressed={sort === 'alpha'}
          aria-label={t('sort.alpha_asc')}
          data-tooltip={t('sort.alpha_asc')}
        >
          A↓
        </button>
        <button
          className={'fx-seg fx-seg-alpha' + (sort === 'alpha_desc' ? ' fx-seg-active' : '')}
          onClick={() => setSort('alpha_desc')}
          aria-pressed={sort === 'alpha_desc'}
          aria-label={t('sort.alpha_desc')}
          data-tooltip={t('sort.alpha_desc')}
        >
          Z↓
        </button>
      </div>

      <div className="fx-viewseg" role="group" aria-label="view mode">
        <button
          className={'fx-vs' + (viewMode === 'cards' ? ' fx-vs-active' : '')}
          data-tooltip={t('view.cards')}
          aria-label={t('view.cards')}
          onClick={() => setViewMode('cards')}
        >
          <Icon d={I.layers} size={14} />
        </button>
        <button
          className={'fx-vs' + (viewMode === 'compact' ? ' fx-vs-active' : '')}
          data-tooltip={t('view.compact')}
          aria-label={t('view.compact')}
          onClick={() => setViewMode('compact')}
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <rect x="3" y="4" width="8" height="7" rx="1.5" />
            <rect x="13" y="4" width="8" height="7" rx="1.5" />
            <rect x="3" y="13" width="8" height="7" rx="1.5" />
            <rect x="13" y="13" width="8" height="7" rx="1.5" />
          </svg>
        </button>
        <button
          className={'fx-vs' + (viewMode === 'list' ? ' fx-vs-active' : '')}
          data-tooltip={t('view.list')}
          aria-label={t('view.list')}
          onClick={() => setViewMode('list')}
        >
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <path d="M3 6h18M3 12h18M3 18h18" />
          </svg>
        </button>
        {viewMode === 'cards' && (
          <>
            <span className="fx-viewseg-sep" aria-hidden="true" />
            {([3, 5, 8] as const).map((n) => (
              <button
                key={n}
                className={'fx-vs fx-vs-density' + (gridCols === n ? ' fx-vs-active' : '')}
                data-tooltip={`${n} ${t('view.density')}`}
                aria-label={`${n} ${t('view.density')}`}
                aria-pressed={gridCols === n}
                onClick={() => setGridCols(n)}
              >
                <DensityIcon cols={n} />
              </button>
            ))}
          </>
        )}
      </div>

      <LocalePicker />

      <button
        className="fx-themetoggle"
        aria-label={t('topbar.toggle_theme')}
        data-tooltip={t('topbar.toggle_theme')}
        onClick={() => setDark(!dark)}
      >
        <Icon d={dark ? I.sun : I.moon} size={16} />
      </button>

      <button className="fx-cta fx-cta-folder" onClick={onNewFolder} aria-label={t('topbar.new_folder')}>
        <Icon d={I.folder} size={16} stroke={2.2} /> {t('topbar.new_folder')}
        <kbd className="fx-kbd fx-kbd-cta">⌥F</kbd>
      </button>

      <button className="fx-cta" onClick={onNewLink} aria-label={t('topbar.new_link')}>
        <Icon d={I.plus} size={16} stroke={2.2} /> {t('topbar.new_link')}
        <kbd className="fx-kbd fx-kbd-cta">⌥N</kbd>
      </button>
    </header>
  )
}

// Tiny "N vertical bars in a rounded rect" — visually conveys the column count
// without needing distinct labels. Lines scale to match the requested density.
function DensityIcon({ cols }: { cols: 3 | 5 | 8 }) {
  const pad = 3
  const w = 20
  const inner = w - pad * 2
  const gap = inner / cols / 3
  const barW = (inner - gap * (cols - 1)) / cols
  return (
    <svg width="14" height="14" viewBox="0 0 20 20" aria-hidden="true">
      <rect x="1" y="2.5" width="18" height="15" rx="3" fill="none" stroke="currentColor" strokeWidth="1.4" />
      {Array.from({ length: cols }, (_, i) => (
        <rect
          key={i}
          x={pad + i * (barW + gap)}
          y={5}
          width={barW}
          height={10}
          rx={Math.min(1, barW / 2)}
          fill="currentColor"
          opacity={0.85}
        />
      ))}
    </svg>
  )
}
