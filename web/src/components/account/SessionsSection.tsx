import { useTranslation } from 'react-i18next'
import { I } from '../icons'
import { Notice, SectionCard, SectionRow } from './SectionCard'

/**
 * The two ways out of the account, told apart before they are clicked.
 *
 * They were a sentence and two buttons of nearly equal weight, one of which
 * ends every session this account has anywhere. Two rows now, each stating what
 * it does to THIS browser and to the others — the distinction the panel exists
 * to make, and the one a label alone cannot carry.
 *
 * `GET /api/auth/sessions` lists live sessions and `DELETE /api/auth/sessions/{id}`
 * revokes one. This panel currently offers sign-out of this browser and of
 * every session, not the list itself — a list of devices is the thing people
 * expect here, and shipping the two bulk actions without it is an explicit
 * product choice, not a missing route.
 */
export function SessionsSection({
  onSignOut,
  onSignOutEverywhere,
}: {
  onSignOut: () => void
  onSignOutEverywhere: () => void
}) {
  const { t } = useTranslation()
  return (
    <SectionCard
      icon={I.users}
      title={t('account.sessions_card_title')}
      subtitle={t('account.sessions_card_subtitle')}
    >
      <div className="fx-sec-rows">
        <SectionRow
          icon={I.logout}
          name={t('auth.sign_out')}
          hint={t('account.sign_out_hint')}
          action={
            <button className="fx-btn" onClick={onSignOut}>
              {t('auth.sign_out')}
            </button>
          }
        />
        {/*
          Tinted before the click, not only in the confirmation that follows.
          The dialog is the last chance to stop; the row is where the user
          decides which of the two they meant.
        */}
        <SectionRow
          icon={I.users}
          tone="danger"
          name={t('profile.logout_all_action')}
          hint={t('account.sign_out_all_hint')}
          action={
            <button className="fx-btn fx-btn-danger" onClick={onSignOutEverywhere}>
              {t('profile.logout_all_action')}
            </button>
          }
        />
      </div>
      {/* API tokens are NOT sessions and survive both actions. Stated here
          because "sign out everywhere" reads like it covers everything, and an
          extension that keeps working afterwards is otherwise a surprise. */}
      <Notice tone="info">{t('account.group_sessions_hint')}</Notice>
    </SectionCard>
  )
}
