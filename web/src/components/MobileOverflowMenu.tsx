import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'

type Sort = 'created' | 'clicks' | 'recent' | 'alpha' | 'alpha_desc'
type ViewMode = 'cards' | 'compact' | 'list'

type Props = {
  sort: Sort
  setSort: (s: Sort) => void
  viewMode: ViewMode
  setViewMode: (m: ViewMode) => void
  gridCols: 3 | 5 | 8
  setGridCols: (n: 3 | 5 | 8) => void
  onNewFolder: () => void
}

// "More" overflow menu shown only on ≤768px (CSS-gated). Hosts sort + view
// + density + new-folder so the mobile topbar can keep just the primary
// row (hamburger + brand + search) plus the locale picker, theme toggle
// and the FAB for the New Link CTA. Replaces the previously hidden
// `.fx-segment` / `.fx-viewseg` segments on mobile.
export function MobileOverflowMenu({
  sort,
  setSort,
  viewMode,
  setViewMode,
  gridCols,
  setGridCols,
  onNewFolder,
}: Props) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('mousedown', onDown)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('mousedown', onDown)
      window.removeEventListener('keydown', onKey)
    }
  }, [open])

  return (
    <div ref={ref} className="fx-mobile-more">
      <button
        type="button"
        className="fx-iconbtn"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t('common.next', { defaultValue: 'More' })}
        data-tooltip="More"
        onClick={() => setOpen((v) => !v)}
      >
        <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
          <circle cx="5" cy="12" r="1.6" />
          <circle cx="12" cy="12" r="1.6" />
          <circle cx="19" cy="12" r="1.6" />
        </svg>
      </button>

      {open && (
        <div className="fx-mobile-more-popover" role="menu">
          <Section label={t('sort.label', { defaultValue: 'Sort' })}>
            <Row
              icon={I.sparkles}
              label={t('sort.new')}
              active={sort === 'created'}
              onClick={() => { setSort('created'); setOpen(false) }}
            />
            <Row
              icon={I.flame}
              label={t('sort.top')}
              active={sort === 'clicks'}
              onClick={() => { setSort('clicks'); setOpen(false) }}
            />
            <Row
              icon={I.clock}
              label={t('sort.recent')}
              active={sort === 'recent'}
              onClick={() => { setSort('recent'); setOpen(false) }}
            />
            <Row
              glyph="A↓"
              label={t('sort.alpha_asc')}
              active={sort === 'alpha'}
              onClick={() => { setSort('alpha'); setOpen(false) }}
            />
            <Row
              glyph="Z↓"
              label={t('sort.alpha_desc')}
              active={sort === 'alpha_desc'}
              onClick={() => { setSort('alpha_desc'); setOpen(false) }}
            />
          </Section>

          <Section label={t('view.density', { defaultValue: 'View' })}>
            <Row
              icon={I.layers}
              label={t('view.cards')}
              active={viewMode === 'cards'}
              onClick={() => { setViewMode('cards'); setOpen(false) }}
            />
            <Row
              glyph="⊞"
              label={t('view.compact')}
              active={viewMode === 'compact'}
              onClick={() => { setViewMode('compact'); setOpen(false) }}
            />
            <Row
              glyph="≡"
              label={t('view.list')}
              active={viewMode === 'list'}
              onClick={() => { setViewMode('list'); setOpen(false) }}
            />
            {viewMode === 'cards' && (
              <div className="fx-mobile-more-density">
                {([3, 5, 8] as const).map((n) => (
                  <button
                    key={n}
                    type="button"
                    className={'fx-mobile-more-density-btn' + (gridCols === n ? ' fx-mobile-more-density-active' : '')}
                    onClick={() => { setGridCols(n); setOpen(false) }}
                    aria-pressed={gridCols === n}
                  >
                    {n}
                  </button>
                ))}
                <span className="fx-mobile-more-density-label">{t('view.density')}</span>
              </div>
            )}
          </Section>

          <Section>
            <Row
              icon={I.folder}
              label={t('topbar.new_folder')}
              onClick={() => { onNewFolder(); setOpen(false) }}
            />
          </Section>
        </div>
      )}
    </div>
  )
}

function Section({ label, children }: { label?: string; children: React.ReactNode }) {
  return (
    <div className="fx-mobile-more-section">
      {label && <div className="fx-mobile-more-section-label">{label}</div>}
      {children}
    </div>
  )
}

function Row({
  icon,
  glyph,
  label,
  active,
  onClick,
}: {
  icon?: React.ReactNode
  glyph?: string
  label: string
  active?: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      role="menuitemradio"
      aria-checked={!!active}
      className={'fx-mobile-more-row' + (active ? ' fx-mobile-more-row-active' : '')}
      onClick={onClick}
    >
      <span className="fx-mobile-more-row-icon" aria-hidden="true">
        {icon ? <Icon d={icon} size={14} /> : <span className="fx-mobile-more-row-glyph">{glyph}</span>}
      </span>
      <span className="fx-mobile-more-row-label">{label}</span>
      {active && (
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" aria-hidden="true">
          <path d="M5 12l5 5 9-12" />
        </svg>
      )}
    </button>
  )
}
