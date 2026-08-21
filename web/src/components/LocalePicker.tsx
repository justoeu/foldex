import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { SUPPORTED_LOCALES, type LocaleCode } from '../i18n'
import { usePortalMenu } from '../hooks/usePortalMenu'
import { useAuth, useCurrentUser } from '../auth/AuthProvider'
import * as auth from '../api/auth'

// Globe button in the topbar that pops a menu of supported languages and
// persists the choice via i18next's detector (localStorage["foldex.locale"]).
//
// The menu is portaled to <body> with fixed positioning derived from the
// button rect: the topbar sets `overflow: hidden` (so its CTAs can't spill out
// of the rounded card), which clipped an absolutely-positioned dropdown — the
// menu rendered behind / cut off by the topbar and its options were unclickable.
// The portal plumbing is shared with the user menu via usePortalMenu.
export function LocalePicker() {
  const { i18n, t } = useTranslation()
  const { open, pos, btnRef, menuRef, toggle, setOpen } = usePortalMenu()
  const user = useCurrentUser()
  const { adopt } = useAuth()

  const current =
    SUPPORTED_LOCALES.find((l) => l.code === (i18n.resolvedLanguage ?? i18n.language)) ??
    SUPPORTED_LOCALES[0]

  const pick = (code: LocaleCode) => {
    const previous = (i18n.resolvedLanguage ?? i18n.language) as LocaleCode
    void i18n.changeLanguage(code)
    setOpen(false)
    // Carry the choice to the account, so the language the screen speaks is
    // also the one the e-mails speak. Without this the account preference —
    // which useAccountLocale re-applies on every load — would silently undo
    // the pick on the next reload.
    if (!user || user.locale === code) return
    void auth
      .updateLocale(code)
      .then(adopt)
      .catch(() => {
        // Put the language back NOW rather than letting it stand. The account
        // is the source of truth and useAccountLocale re-asserts it on the next
        // load, so leaving the failed pick in place would show the new language
        // until some later mount silently reverted it — a change the user
        // believed had stuck, undone with no event they could connect it to.
        // Reverting immediately at least makes the failure visible where the
        // action was taken.
        void i18n.changeLanguage(previous)
      })
  }

  return (
    // The unclassed wrapper is load-bearing: `.fx-topbar > div:not([class])`
    // (foldex.css mobile breakpoint) hides the picker on narrow screens.
    <div style={{ position: 'relative' }}>
      <button
        ref={btnRef}
        type="button"
        className="fx-iconbtn"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t('topbar.language')}
        data-tooltip={t('topbar.language')}
        onClick={toggle}
        style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}
      >
        <Icon d={I.globe} size={14} />
        <span style={{ fontFamily: 'var(--fx-mono)', fontSize: 11, textTransform: 'uppercase' }}>
          {current.code}
        </span>
      </button>
      {open &&
        pos &&
        createPortal(
          <div
            ref={menuRef}
            role="menu"
            aria-label={t('topbar.language')}
            style={{
              position: 'fixed',
              top: pos.top,
              right: pos.right,
              minWidth: 160,
              // --fx-surface-3 is the near-opaque token; --fx-surface (0.6 alpha)
              // only reads solid behind the topbar's backdrop-filter, which the
              // portaled menu no longer sits on — so it carries its own blur.
              background: 'var(--fx-surface-3)',
              backdropFilter: 'blur(20px) saturate(140%)',
              WebkitBackdropFilter: 'blur(20px) saturate(140%)',
              border: '1px solid var(--fx-border)',
              borderRadius: 10,
              boxShadow: '0 8px 24px rgba(0,0,0,0.18)',
              padding: 4,
              zIndex: 1000,
            }}
          >
            {SUPPORTED_LOCALES.map((l) => {
              const active = l.code === current.code
              return (
                <button
                  key={l.code}
                  type="button"
                  role="menuitem"
                  aria-current={active ? 'true' : undefined}
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
                  {active && (
                    <span style={{ color: 'var(--fx-accent)', display: 'inline-flex' }}>
                      <Icon d={I.check} size={13} />
                    </span>
                  )}
                  <span
                    style={{ fontFamily: 'var(--fx-mono)', fontSize: 10, color: 'var(--fx-ink-4)' }}
                  >
                    {l.code.toUpperCase()}
                  </span>
                </button>
              )
            })}
          </div>,
          document.body,
        )}
    </div>
  )
}
