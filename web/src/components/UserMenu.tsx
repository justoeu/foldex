import { createPortal } from 'react-dom'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { initialsOf } from '../lib/initials'
import { usePortalMenu } from '../hooks/usePortalMenu'
import { useAuth } from '../auth/AuthProvider'

/**
 * The topbar's signed-in user menu: avatar (initials) button that pops a
 * dropdown with the profile entry point and sign-out.
 *
 * Portaled to <body> via usePortalMenu (the topbar's `overflow: hidden` would
 * clip an in-flow dropdown). Sign-out lives here so it is reachable from
 * EVERY view, not only from inside the settings hub.
 */
export function UserMenu({ onOpenProfile }: { onOpenProfile: () => void }) {
  const { t } = useTranslation()
  const { session, signOut } = useAuth()
  const { open, pos, btnRef, menuRef, toggle, close } = usePortalMenu()

  const user = session.status === 'authenticated' ? session.user : null
  if (!user) return null

  const goProfile = () => {
    close()
    onOpenProfile()
  }

  return (
    <>
      <button
        ref={btnRef}
        type="button"
        className={'fx-userbtn' + (open ? ' fx-userbtn-active' : '')}
        aria-label={t('usermenu.aria')}
        aria-expanded={open}
        aria-haspopup="menu"
        data-tooltip={user.name || user.email}
        onClick={toggle}
      >
        <span className="fx-avatar fx-avatar-sm" aria-hidden="true">
          {initialsOf(user.name, user.email)}
        </span>
      </button>
      {open && pos && createPortal(
        <div ref={menuRef} className="fx-portalmenu fx-usermenu" role="menu" style={{ top: pos.top, right: pos.right }}>
          <div className="fx-usermenu-head">
            <span className="fx-avatar fx-avatar-sm" aria-hidden="true">
              {initialsOf(user.name, user.email)}
            </span>
            <span style={{ minWidth: 0 }}>
              <span className="fx-usermenu-name" style={{ display: 'block' }}>{user.name || user.email}</span>
              <span className="fx-usermenu-mail" style={{ display: 'block' }}>{user.email}</span>
            </span>
          </div>
          <button type="button" role="menuitem" className="fx-usermenu-item" onClick={goProfile}>
            <Icon d={I.user} size={14} /> {t('usermenu.profile')}
          </button>
          <button
            type="button"
            role="menuitem"
            className="fx-usermenu-item fx-usermenu-item-danger"
            onClick={() => {
              close()
              void signOut()
            }}
          >
            <Icon d={I.arrowR} size={14} /> {t('auth.sign_out')}
          </button>
        </div>,
        document.body,
      )}
    </>
  )
}
