import { useTranslation } from 'react-i18next'
import { Icon, I } from '../icons'
import { initialsOf } from '../../lib/initials'
import { hasSecondFactor } from '../../auth/types'
import type { AuthUser } from '../../auth/types'

/**
 * Who you are, at the top of the account page.
 *
 * The chips answer the two questions the page exists to act on — is there a
 * password, is there a second factor — WITHOUT scrolling to the sections that
 * change them. The old layout buried both inside cards that rendered a single
 * line of status each, so the state of the account was the one thing the
 * account screen did not show.
 */
export function AccountHero({ user }: { user: AuthUser }) {
  const { t } = useTranslation()
  const hasPassword = user.has_password
  const hasFactor = hasSecondFactor(user)

  return (
    <header className="fx-acct-hero">
      <span className="fx-avatar fx-avatar-lg" aria-hidden="true">
        {initialsOf(user.name, user.email)}
      </span>
      <div className="fx-acct-hero-id">
        <h2 className="fx-acct-hero-name">{user.name || user.email}</h2>
        <p className="fx-acct-hero-mail">{user.email}</p>
        <div className="fx-acct-chips">
          <span className="fx-acct-chip fx-acct-chip-role">{t(`admin.role_${user.role}`)}</span>
          <span className={'fx-acct-chip ' + (hasPassword ? 'fx-acct-chip-ok' : 'fx-acct-chip-warn')}>
            <Icon d={hasPassword ? I.check : I.alert} size={12} />
            {hasPassword ? t('account.chip_password_on') : t('account.chip_password_off')}
          </span>
          <span className={'fx-acct-chip ' + (hasFactor ? 'fx-acct-chip-ok' : 'fx-acct-chip-warn')}>
            <Icon d={hasFactor ? I.shield : I.alert} size={12} />
            {hasFactor ? t('account.chip_2fa_on') : t('account.chip_2fa_off')}
          </span>
        </div>
      </div>
    </header>
  )
}
