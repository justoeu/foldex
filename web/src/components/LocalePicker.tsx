import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { SUPPORTED_LOCALES, type LocaleCode } from '../i18n'

// Compact globe button in the topbar that pops a small menu of supported
// languages. Persists the choice via i18next's language detector
// (localStorage["foldex.locale"]).
export function LocalePicker() {
  const { i18n, t } = useTranslation()
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  const current =
    SUPPORTED_LOCALES.find((l) => l.code === (i18n.resolvedLanguage ?? i18n.language)) ??
    SUPPORTED_LOCALES[0]

  // Close on outside click.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    window.addEventListener('mousedown', onDown)
    return () => window.removeEventListener('mousedown', onDown)
  }, [open])

  const pick = (code: LocaleCode) => {
    void i18n.changeLanguage(code)
    setOpen(false)
  }

  return (
    <div ref={ref} style={{ position: 'relative' }}>
      <button
        type="button"
        className="fx-iconbtn"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={t('topbar.language')}
        data-tooltip={t('topbar.language')}
        onClick={() => setOpen((v) => !v)}
        style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}
      >
        <Icon d={I.globe} size={14} />
        <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 11, textTransform: 'uppercase' }}>
          {current.code}
        </span>
      </button>
      {open && (
        <div
          role="listbox"
          aria-label={t('topbar.language')}
          style={{
            position: 'absolute',
            right: 0,
            top: '110%',
            minWidth: 160,
            background: 'var(--fx-surface)',
            border: '1px solid var(--fx-border)',
            borderRadius: 10,
            boxShadow: '0 8px 24px rgba(0,0,0,0.12)',
            padding: 4,
            zIndex: 50,
          }}
        >
          {SUPPORTED_LOCALES.map((l) => {
            const active = l.code === current.code
            return (
              <button
                key={l.code}
                type="button"
                role="option"
                aria-selected={active}
                onClick={() => pick(l.code)}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  width: '100%',
                  padding: '8px 10px',
                  border: 0,
                  background: active ? 'rgba(99,102,241,0.08)' : 'transparent',
                  color: 'var(--fx-ink)',
                  textAlign: 'left',
                  cursor: 'pointer',
                  borderRadius: 6,
                  fontSize: 13,
                }}
              >
                <span aria-hidden="true">{l.flag}</span>
                <span style={{ flex: 1 }}>{l.label}</span>
                <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 10, color: 'var(--fx-ink-4)' }}>
                  {l.code.toUpperCase()}
                </span>
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}
