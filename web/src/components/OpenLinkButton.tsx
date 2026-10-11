import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { showCopyToast } from './CopyToast'
import { useCopy } from '../hooks/useCopy'
import { goHref } from '../api/links'
import type { Link } from '../api/types'

/**
 * The copy affordance next to every "Acessar".
 *
 * Success is told by the icon flipping to a check — a toast here would be
 * noise for a gesture whose result is already visible in the toolbar; the
 * toast belongs to the incognito flow, where the user still has steps left.
 */
export function CopyLinkButton({ link, small = false }: Readonly<{ link: Link; small?: boolean }>) {
  const { t } = useTranslation()
  // copied() is derived from the value: a second copy of ANOTHER link can
  // never leave this button claiming the wrong thing.
  const { copied, copy } = useCopy()
  const isCopied = copied(link.url)
  const icon = isCopied ? I.check : I.copy
  const iconSize = small ? 12 : 14

  return (
    <button
      type="button"
      className={'fx-iconbtn fx-copybtn' + (small ? ' fx-copybtn-small' : '')}
      data-tooltip={isCopied ? t('link_card.copied') : t('link_card.copy_link')}
      data-tooltip-side="top"
      aria-label={isCopied
        ? t('link_card.copied_aria', { title: link.title })
        : t('link_card.copy_link_aria', { title: link.title })}
      onClick={() => void copy(link.url)}
    >
      <Icon d={icon} size={iconSize} />
    </button>
  )
}

/**
 * "Acessar" with a chevron menu.
 *
 * The browser has no API to open an incognito window from a page, so the
 * honest version of "open anonymously" is the fastest path the platform
 * allows: copy the link and tell the user the two keystrokes left. The menu
 * also repeats the plain copy so the anonymous flow never needs the toolbar.
 */
export function OpenLinkButton({
  link,
  onGo,
  small = false,
}: Readonly<{ link: Link; onGo?: () => void; small?: boolean }>) {
  const { t } = useTranslation()
  const { copy } = useCopy()
  const [open, setOpen] = useState(false)
  const wrap = useRef<HTMLSpanElement>(null)

  useEffect(() => {
    if (!open) return
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') setOpen(false)
    }
    function onDown(e: MouseEvent) {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('keydown', onKey)
    document.addEventListener('mousedown', onDown)
    return () => {
      document.removeEventListener('keydown', onKey)
      document.removeEventListener('mousedown', onDown)
    }
  }, [open])

  async function copyForIncognito() {
    setOpen(false)
    const ok = await copy(link.url)
    // Failure is told too: silence here would read as "incognito opens
    // later", which it never will.
    showCopyToast(ok ? t('link_card.incognito_hint') : t('link_card.copy_failed'))
  }

  return (
    <span className="fx-openwrap" ref={wrap}>
      <a
        className={'fx-openbtn' + (small ? ' fx-openbtn-list' : '')}
        href={goHref(link)}
        target="_blank"
        rel="noopener noreferrer"
        data-tooltip={t('link_card.open_action')}
        data-tooltip-side="top"
        aria-label={t('common.open_link_aria', { title: link.title })}
        onClick={onGo}
      >
        {!small && <span className="fx-openbtn-go">{t('link_card.open_action')}</span>}
        {small && <Icon d={I.open} size={12} />}
        {!small && <Icon d={I.arrowR} size={14} />}
      </a>
      <button
        type="button"
        className={'fx-openchev' + (open ? ' fx-openchev-open' : '')}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={t('link_card.open_menu_aria', { title: link.title })}
        onClick={() => setOpen((v) => !v)}
      >
        <Icon d={I.chevronDown} size={12} />
      </button>
      {open && (
        <span className="fx-openmenu" role="menu">
          <button
            type="button"
            role="menuitem"
            className="fx-openmenu-item"
            onClick={() => {
              setOpen(false)
              void copy(link.url)
            }}
          >
            <Icon d={I.copy} size={13} /> {t('link_card.copy_link')}
          </button>
          <button type="button" role="menuitem" className="fx-openmenu-item" onClick={() => void copyForIncognito()}>
            <Icon d={I.incognito} size={13} /> {t('link_card.open_incognito')}
          </button>
        </span>
      )}
    </span>
  )
}
