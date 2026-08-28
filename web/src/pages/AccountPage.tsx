import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from '../components/icons'
import { useConfirm } from '../components/ConfirmDialog'
import { AccountHero } from '../components/account/AccountHero'
import { ProfileFields } from '../components/account/ProfileFields'
import { AccessSection } from '../components/account/AccessSection'
import { SessionsSection } from '../components/account/SessionsSection'
import { ActivitySection } from '../components/account/ActivitySection'
import { TwoFactorSection } from '../components/TwoFactorSection'
import { ApiTokensSection } from '../components/ApiTokensSection'
import { forgetRememberedEmail } from '../components/auth/LoginScreen'
import { useAuth, useCurrentUser } from '../auth/AuthProvider'
import { http } from '../api/client'

/** The sub-sections of the account page, in the order the rail lists them. */
export const ACCOUNT_TABS = ['profile', 'access', 'security', 'tokens', 'sessions', 'activity'] as const
export type AccountTab = (typeof ACCOUNT_TABS)[number]

export function isAccountTab(value: string | undefined): value is AccountTab {
  return value !== undefined && (ACCOUNT_TABS as readonly string[]).includes(value)
}

const TAB_ICON: Record<AccountTab, ReactNode> = {
  profile: I.user,
  access: I.key,
  security: I.shield,
  tokens: I.link,
  sessions: I.users,
  activity: I.clock,
}

/**
 * Everything the signed-in user manages about their own account.
 *
 * It replaces four hub tiles — profile, sign-in methods, two-factor, API
 * tokens — that were four cards deep and each rendered almost nothing. The
 * sign-in card was the worst: its password form appears only when the account
 * HAS no password and its Google block only when the provider is configured,
 * so an ordinary account saw one line of status and no action at all.
 *
 * One section shows at a time, chosen from a rail. Stacking all five was the
 * first shape and it did not survive contact: the page ran to three screens of
 * scrolling in a 760px column, so the width was wasted AND nothing was findable
 * — the two failures at once. A rail costs one click and makes "where is X"
 * answerable by looking rather than scrolling, which is what the four tiles got
 * right and the stack gave up.
 *
 * The tabs are also what makes the merged section names mean something again:
 * `security` and `tokens` used to collapse into "the account page" and lose
 * which part the caller asked for.
 */
export function AccountPage({ initialTab }: { initialTab?: AccountTab }) {
  const { t } = useTranslation()
  const user = useCurrentUser()
  const { session, signOut } = useAuth()
  const confirmAction = useConfirm()
  const [tab, setTab] = useState<AccountTab>(initialTab ?? 'profile')

  // No provider configured means no row: a "Connect Google" button on an
  // instance with no client would start a flow the server refuses.
  const googleEnabled = session.status !== 'loading' && session.features.google_oauth

  if (!user) return null

  async function signOutEverywhere() {
    const yes = await confirmAction({
      title: t('profile.logout_all_title'),
      message: t('profile.logout_all_message'),
      destructive: true,
    })
    if (!yes) return
    try {
      await http.post('/api/auth/logout-all')
    } catch {
      // Swallowed deliberately, not ignored: the sign-out below happens either
      // way, so there is nothing for the user to do about a failed revoke —
      // and letting it propagate out of an async click handler surfaced as an
      // unhandled rejection with no message anyone could act on.
    } finally {
      // Ending every session is the shared-browser gesture; leaving the
      // remembered address behind would outlive the thing the user just asked
      // to be rid of.
      forgetRememberedEmail()
      await signOut()
    }
  }

  return (
    <div className="fx-acct">
      <AccountHero user={user} />

      <div className="fx-acct-split">
        <nav className="fx-acct-rail" aria-label={t('account.nav_aria')}>
          {ACCOUNT_TABS.map((id) => (
            <button
              key={id}
              type="button"
              className={'fx-acct-railbtn' + (tab === id ? ' fx-acct-railbtn-active' : '')}
              aria-current={tab === id ? 'page' : undefined}
              onClick={() => setTab(id)}
            >
              <Icon d={TAB_ICON[id]} size={14} />
              <span>{t(`account.group_${id}`)}</span>
            </button>
          ))}
        </nav>

        <div className="fx-acct-panel">
          {/* A heading per panel, not just the rail item: the rail says where
              you can go, and a screen reader arriving in the panel needs to be
              told where it landed. */}
          <h3 className="fx-acct-panel-title">{t(`account.group_${tab}`)}</h3>

          {tab === 'profile' && <ProfileFields user={user} />}
          {tab === 'access' && <AccessSection user={user} googleEnabled={googleEnabled} />}
          {tab === 'security' && <TwoFactorSection />}
          {tab === 'tokens' && <ApiTokensSection />}
          {tab === 'activity' && <ActivitySection />}
          {tab === 'sessions' && (
            <SessionsSection
              onSignOut={() => void signOut()}
              onSignOutEverywhere={() => void signOutEverywhere()}
            />
          )}
        </div>
      </div>
    </div>
  )
}
