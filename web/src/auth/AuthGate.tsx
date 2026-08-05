import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useAuth } from './AuthProvider'
import { urlTokens } from './authUrl'
import { LoginScreen } from '../components/auth/LoginScreen'
import { SetupScreen } from '../components/auth/SetupScreen'
import { InviteScreen } from '../components/auth/InviteScreen'

/**
 * Decides between the app and an auth screen.
 *
 * It sits ABOVE <App/> rather than being another member of App's `view` union.
 * App calls useEntries, useFolders (twice) and useTags unconditionally, before
 * it looks at `view` at all — so a `view === 'login'` branch would still fire
 * four authenticated queries on every anonymous mount, each 401ing and each
 * kicking the refresh interceptor. Keeping the gate outside means the app's
 * hooks never run until there is a session to run them under.
 *
 * A router was considered and rejected: CLAUDE.md §4 makes "internal IDs never
 * appear in the URL" an explicit invariant, and navigation is component state
 * throughout the product.
 */
export function AuthGate({ children }: { children: ReactNode }) {
  const { session } = useAuth()
  const { t } = useTranslation()
  // Captured once at module scope (see authUrl.ts) and held in state so
  // dismissing the invite screen does not resurrect it on the next render.
  const [inviteToken, setInviteToken] = useState(urlTokens.invite ?? '')

  if (session.status === 'loading') {
    return (
      <div className="fx-auth fx-auth-boot" role="status" aria-live="polite">
        <span className="fx-auth-spinner fx-auth-spinner-lg" aria-hidden="true" />
        <span className="fx-auth-boot-label">{t('auth.loading')}</span>
      </div>
    )
  }

  if (session.status === 'authenticated') return <>{children}</>

  // An invite token outranks both remaining screens: someone arriving with one
  // is trying to CREATE the account, so showing them a login form they cannot
  // satisfy would be a dead end.
  if (inviteToken) {
    return <InviteScreen token={inviteToken} onGiveUp={() => setInviteToken('')} />
  }

  if (session.status === 'setup_required') return <SetupScreen />

  return <LoginScreen />
}
