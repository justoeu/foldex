import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useConfirm } from '../components/ConfirmDialog'
import { ProfileFields } from '../components/account/ProfileFields'
import { AccessSection } from '../components/account/AccessSection'
import { SessionsSection } from '../components/account/SessionsSection'
import { ActivitySection } from '../components/account/ActivitySection'
import { TwoFactorSection } from '../components/TwoFactorSection'
import { ApiTokensSection } from '../components/ApiTokensSection'
import { forgetRememberedEmail } from '../components/auth/LoginScreen'
import { useAuth, useCurrentUser } from '../auth/AuthProvider'
import { http } from '../api/client'
import { initialsOf } from '../lib/initials'
import { hasSecondFactor, type AuthUser } from '../auth/types'

/** The sub-sections of the account page, in the order a group lists them. */
export const ACCOUNT_TABS = ['profile', 'access', 'security', 'tokens', 'sessions', 'activity'] as const
export type AccountTab = (typeof ACCOUNT_TABS)[number]

export function isAccountTab(value: string | undefined): value is AccountTab {
  return value !== undefined && (ACCOUNT_TABS as readonly string[]).includes(value)
}

/**
 * The "temas" navigation: four grouped tabs, two of which hold two sections
 * stacked. Perfil+Acesso are one subject (who the account is / how it signs
 * in), 2FA+Sessões another (how it stays safe); Tokens and Atividade stand
 * alone. The deep-link tab an old bookmark carries still lands on the right
 * group — the section list is derived, not re-routed.
 */
const GROUPS: ReadonlyArray<{ id: AccountGroup; tabs: readonly AccountTab[] }> = [
  { id: 'conta', tabs: ['profile', 'access'] },
  { id: 'seguranca', tabs: ['security', 'sessions'] },
  { id: 'tokens', tabs: ['tokens'] },
  { id: 'atividade', tabs: ['activity'] },
]
export type AccountGroup = 'conta' | 'seguranca' | 'tokens' | 'atividade'

const GROUP_OF_TAB: Record<AccountTab, AccountGroup> = Object.fromEntries(
  GROUPS.flatMap((g) => g.tabs.map((tab) => [tab, g.id])),
) as Record<AccountTab, AccountGroup>

const SECTION_TITLE: Record<AccountTab, { title: string; lede: string }> = {
  profile: { title: 'account.section_perfil_title', lede: 'account.section_perfil_lede' },
  access: { title: 'account.section_acesso_title', lede: 'account.section_acesso_lede' },
  security: { title: 'account.section_2fa_title', lede: 'account.section_2fa_lede' },
  sessions: { title: 'account.section_sessoes_title', lede: 'account.section_sessoes_lede' },
  tokens: { title: 'account.section_tokens_title', lede: 'account.section_tokens_lede' },
  activity: { title: 'account.section_atividade_title', lede: 'account.section_atividade_lede' },
}

/**
 * Everything the signed-in user manages about their own account (INV-146).
 *
 * The redesign keeps the six sections and their wiring untouched; what changed
 * is the shell — a grouped tab bar over stacked sections with their own
 * headings, replacing the rail+panel split (the rail wasted a 220px column to
 * save one scroll, and the panel hid the section's own name behind the rail
 * item that selected it).
 */
export function AccountPage({ initialTab }: Readonly<{ initialTab?: AccountTab }>) {
  const { t } = useTranslation()
  const user = useCurrentUser()
  const { session, signOut } = useAuth()
  const confirmAction = useConfirm()
  const [group, setGroup] = useState<AccountGroup>(
    initialTab ? GROUP_OF_TAB[initialTab] : 'conta',
  )

  // No provider configured means no row: a "Connect Google" button on an
  // instance without a client would start a flow the server refuses.
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

  const active = GROUPS.find((g) => g.id === group) ?? GROUPS[0]

  return (
    <div className="fx-account">
      <AccountHead user={user} />

      <nav className="fx-acc2-tabs-nav" aria-label={t('account.nav_aria')}>
        <div className="fx-acc2-tabs" role="tablist">
          {GROUPS.map((g) => {
            const on = g.id === group
            return (
              <button
                key={g.id}
                type="button"
                role="tab"
                aria-selected={on}
                className="fx-acc2-tab"
                onClick={() => setGroup(g.id)}
              >
                {t(`account.group2_${g.id}`)}
              </button>
            )
          })}
        </div>
      </nav>

      <div className="fx-acc2-sections">
        {active.tabs.map((tab) => (
          <section key={tab} aria-labelledby={`acc-sec-${tab}`}>
            {/* A heading per section, not just the tab: the tab says where you
                can go; a screen reader arriving in the section needs to be told
                where it landed. */}
            <h2 id={`acc-sec-${tab}`} className="fx-acc2-section-title">
              {t(SECTION_TITLE[tab].title)}
            </h2>
            <p className="fx-acc2-lede">{t(SECTION_TITLE[tab].lede)}</p>
            <AccountSection tab={tab} user={user} googleEnabled={googleEnabled} onSignOut={() => void signOut()} onSignOutEverywhere={() => void signOutEverywhere()} />
          </section>
        ))}
      </div>
    </div>
  )
}

function AccountSection({
  tab,
  user,
  googleEnabled,
  onSignOut,
  onSignOutEverywhere,
}: Readonly<{
  tab: AccountTab
  user: AuthUser
  googleEnabled: boolean
  onSignOut: () => void
  onSignOutEverywhere: () => void
}>) {
  switch (tab) {
    case 'profile':
      return <ProfileFields user={user} />
    case 'access':
      return <AccessSection user={user} googleEnabled={googleEnabled} />
    case 'security':
      return <TwoFactorSection />
    case 'tokens':
      return <ApiTokensSection />
    case 'activity':
      return <ActivitySection />
    case 'sessions':
      return <SessionsSection onSignOut={onSignOut} onSignOutEverywhere={onSignOutEverywhere} />
  }
}

/**
 * Who is logged in, above the tabs. The chips the old hero carried (password
 * on, 2FA on) moved to where they act: the access badge and the two-factor
 * banner. Up here they were answers to questions the sections below ask
 * better.
 */
function AccountHead({ user }: Readonly<{ user: AuthUser }>) {
  const { t } = useTranslation()
  return (
    <header className="fx-acc2-head">
      <span className="fx-acc2-avatar" aria-hidden="true">
        {initialsOf(user.name, user.email)}
      </span>
      <div>
        <h1 className="fx-acc2-name">{user.name || user.email}</h1>
        <p className="fx-acc2-meta">
          <span>{user.email}</span>
          <span className="fx-acc2-dot">·</span>
          <span>{t(`admin.role_${user.role}`)}</span>
          {hasSecondFactor(user) && (
            <>
              <span className="fx-acc2-dot">·</span>
              <span>{t('account.chip_2fa_on')}</span>
            </>
          )}
        </p>
      </div>
    </header>
  )
}
