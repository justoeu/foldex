import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { SUPPORTED_LOCALES, type LocaleCode } from '../i18n'

type Sort = 'created' | 'clicks' | 'recent' | 'alpha' | 'alpha_desc'
type ViewMode = 'cards' | 'compact' | 'list'
type View = 'home' | 'import' | 'stats' | 'settings'

type Props = {
  sort: Sort
  setSort: (s: Sort) => void
  viewMode: ViewMode
  setViewMode: (m: ViewMode) => void
  gridCols: 3 | 5 | 8
  setGridCols: (n: 3 | 5 | 8) => void
  foldersCompact: boolean
  setFoldersCompact: (v: boolean) => void
  onNewFolder: () => void
  onNewLink: () => void
  onNewNote: () => void
  dark: boolean
  setDark: (d: boolean) => void
  view: View
  setView: (v: View) => void
}

// Mobile-only overflow popover. The mobile topbar exposes only the three
// primary affordances the user asked for (search + Home + Stats); every
// other control (sort, view, density, new folder, new link, import/export,
// language, theme) lives here. Hidden on desktop via the `display: none`
// default on `.fx-mobile-more`.
export function MobileOverflowMenu({
  sort,
  setSort,
  viewMode,
  setViewMode,
  gridCols,
  setGridCols,
  foldersCompact,
  setFoldersCompact,
  onNewFolder,
  onNewLink,
  onNewNote,
  dark,
  setDark,
  view,
  setView,
}: Props) {
  const { t, i18n } = useTranslation()
  const [open, setOpen] = useState(false)
  const [langOpen, setLangOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  const currentLocale =
    SUPPORTED_LOCALES.find((l) => l.code === (i18n.resolvedLanguage ?? i18n.language)) ??
    SUPPORTED_LOCALES[0]

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false)
        setLangOpen(false)
      }
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
        setLangOpen(false)
      }
    }
    window.addEventListener('mousedown', onDown)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('mousedown', onDown)
      window.removeEventListener('keydown', onKey)
    }
  }, [open])

  const closeAll = () => { setOpen(false); setLangOpen(false) }

  const pickLocale = (code: LocaleCode) => {
    void i18n.changeLanguage(code)
    closeAll()
  }

  return (
    <div ref={ref} className="fx-mobile-more">
      <button
        type="button"
        className="fx-iconbtn"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t('common.more')}
        data-tooltip={t('common.more')}
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
          {/* Primary actions: New link + New folder + Import / Export.
              "New link" duplicates the FAB but having it here keeps the
              menu self-contained — users who reach for the kebab don't
              have to remember the FAB exists. */}
          <Section>
            <Row
              icon={I.plus}
              label={t('topbar.new_link')}
              accent
              onClick={() => { onNewLink(); closeAll() }}
            />
            <Row
              icon={I.folder}
              label={t('topbar.new_folder')}
              onClick={() => { onNewFolder(); closeAll() }}
            />
            <Row
              icon={I.note}
              label={t('topbar.new_note')}
              onClick={() => { onNewNote(); closeAll() }}
            />
            <Row
              icon={I.upload}
              label={t('topbar.import_export')}
              active={view === 'import'}
              onClick={() => { setView('import'); closeAll() }}
            />
            <Row
              icon={I.gear}
              label={t('topbar.settings')}
              active={view === 'settings'}
              onClick={() => { setView('settings'); closeAll() }}
            />
          </Section>

          <Section label={t('sort.label', { defaultValue: 'Sort' })}>
            <Row
              icon={I.sparkles}
              label={t('sort.new')}
              active={sort === 'created'}
              onClick={() => { setSort('created'); closeAll() }}
            />
            <Row
              icon={I.flame}
              label={t('sort.top')}
              active={sort === 'clicks'}
              onClick={() => { setSort('clicks'); closeAll() }}
            />
            <Row
              icon={I.clock}
              label={t('sort.recent')}
              active={sort === 'recent'}
              onClick={() => { setSort('recent'); closeAll() }}
            />
            <Row
              glyph="A↓"
              label={t('sort.alpha_asc')}
              active={sort === 'alpha'}
              onClick={() => { setSort('alpha'); closeAll() }}
            />
            <Row
              glyph="Z↓"
              label={t('sort.alpha_desc')}
              active={sort === 'alpha_desc'}
              onClick={() => { setSort('alpha_desc'); closeAll() }}
            />
          </Section>

          <Section label={t('view.density', { defaultValue: 'View' })}>
            <Row
              icon={I.layers}
              label={t('view.cards')}
              active={viewMode === 'cards'}
              onClick={() => { setViewMode('cards'); closeAll() }}
            />
            <Row
              glyph="⊞"
              label={t('view.compact')}
              active={viewMode === 'compact'}
              onClick={() => { setViewMode('compact'); closeAll() }}
            />
            <Row
              glyph="≡"
              label={t('view.list')}
              active={viewMode === 'list'}
              onClick={() => { setViewMode('list'); closeAll() }}
            />
            {(viewMode === 'cards' || viewMode === 'compact') && (
              <div className="fx-mobile-more-density">
                {([3, 5, 8] as const).map((n) => (
                  <button
                    key={n}
                    type="button"
                    className={'fx-mobile-more-density-btn' + (gridCols === n ? ' fx-mobile-more-density-active' : '')}
                    onClick={() => { setGridCols(n); closeAll() }}
                    aria-pressed={gridCols === n}
                  >
                    {n}
                  </button>
                ))}
                <span className="fx-mobile-more-density-label">{t('view.density')}</span>
              </div>
            )}
            {viewMode === 'cards' && (
              <Row
                icon={I.folder}
                label={foldersCompact ? t('topbar.folders_compact_off') : t('topbar.folders_compact_on')}
                active={foldersCompact}
                onClick={() => { setFoldersCompact(!foldersCompact); closeAll() }}
              />
            )}
          </Section>

          {/* Preferences row: theme + language. Language opens a nested
              listbox inline (rather than a separate popover) so the menu
              stays self-contained inside one scrim/escape boundary. */}
          <Section>
            <Row
              icon={dark ? I.sun : I.moon}
              label={t('topbar.toggle_theme')}
              onClick={() => { setDark(!dark); closeAll() }}
            />
            <Row
              icon={I.globe}
              label={`${t('topbar.language')} · ${currentLocale.code.toUpperCase()}`}
              onClick={() => setLangOpen((v) => !v)}
              chevron={langOpen ? 'down' : 'right'}
            />
            {langOpen && (
              <div className="fx-mobile-more-sublist" role="listbox" aria-label={t('topbar.language')}>
                {SUPPORTED_LOCALES.map((l) => {
                  const active = l.code === currentLocale.code
                  return (
                    <button
                      key={l.code}
                      type="button"
                      role="option"
                      aria-selected={active}
                      className={'fx-mobile-more-row' + (active ? ' fx-mobile-more-row-active' : '')}
                      onClick={() => pickLocale(l.code)}
                    >
                      <span className="fx-mobile-more-row-icon" aria-hidden="true">{l.flag}</span>
                      <span className="fx-mobile-more-row-label">{l.label}</span>
                      <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10, color: 'var(--fx-ink-4)' }}>
                        {l.code.toUpperCase()}
                      </span>
                    </button>
                  )
                })}
              </div>
            )}
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
  accent,
  chevron,
  onClick,
}: {
  icon?: React.ReactNode
  glyph?: string
  label: string
  active?: boolean
  accent?: boolean
  chevron?: 'right' | 'down'
  onClick: () => void
}) {
  return (
    <button
      type="button"
      role="menuitemradio"
      aria-checked={!!active}
      className={
        'fx-mobile-more-row' +
        (active ? ' fx-mobile-more-row-active' : '') +
        (accent ? ' fx-mobile-more-row-accent' : '')
      }
      onClick={onClick}
    >
      <span className="fx-mobile-more-row-icon" aria-hidden="true">
        {icon ? <Icon d={icon} size={14} /> : <span className="fx-mobile-more-row-glyph">{glyph}</span>}
      </span>
      <span className="fx-mobile-more-row-label">{label}</span>
      {active && !chevron && (
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" aria-hidden="true">
          <path d="M5 12l5 5 9-12" />
        </svg>
      )}
      {chevron === 'right' && (
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" aria-hidden="true">
          <path d="M9 6l6 6-6 6" />
        </svg>
      )}
      {chevron === 'down' && (
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" aria-hidden="true">
          <path d="M6 9l6 6 6-6" />
        </svg>
      )}
    </button>
  )
}
