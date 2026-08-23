import { useTranslation } from 'react-i18next'
import { I } from '../icons'
import { SectionBadge, SectionCard } from './SectionCard'
import { EmailRow } from './EmailRow'
import { PasswordRow } from './PasswordCard'
import { GoogleRow } from './GoogleRow'
import type { AuthUser } from '../../auth/types'

/**
 * How this account proves it is itself: the password, and the Google identity.
 *
 * One card holding both rows, rather than two stacked panels, because the two
 * constrain each other — an account converted to Google-only cannot unlink
 * until it has a password again. A boundary between two cards is exactly what
 * hides that, and the ordering is only obvious when they are read together.
 */
export function AccessSection({
  user,
  googleEnabled,
}: {
  user: AuthUser
  googleEnabled: boolean
}) {
  const { t } = useTranslation()
  return (
    <SectionCard
      icon={I.key}
      title={t('account.access_card_title')}
      subtitle={t('account.group_access_hint')}
      badge={
        <SectionBadge tone={user.has_password ? 'on' : 'off'}>
          {user.has_password ? t('account.chip_password_on') : t('account.chip_password_off')}
        </SectionBadge>
      }
    >
      <div className="fx-sec-rows">
        {/* The address leads: it is the login identifier the other two rows
            qualify, and the one row here that is also the recovery channel. */}
        <EmailRow user={user} />
        <PasswordRow user={user} />
        {googleEnabled && <GoogleRow user={user} />}
      </div>
    </SectionCard>
  )
}
