import { Suspense, lazy, startTransition, useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useAuth } from './AuthProvider'
import { urlTokens } from './authUrl'
import { LoginScreen } from '../components/auth/LoginScreen'
import { SetupScreen } from '../components/auth/SetupScreen'
import { InviteScreen } from '../components/auth/InviteScreen'

// Lazy, matching App.tsx's treatment of ImportPage/StatsPage/SettingsPage: the
// gate sits ABOVE <App/>, so anything imported eagerly here ships on every
// visit — including the overwhelmingly common one where the user is already
// signed in and none of these four screens will ever render.
//
// Three of them keep their factories reachable because they are the ones the
// gate reaches by a TRANSITION from a screen that is already painted — see the
// prefetch below. The remaining three are entered at MOUNT, from a URL token,
// with nothing on the glass to preserve.
const importTwoFactor = () => import('../components/auth/TwoFactorScreen')
const importEnrollTotp = () => import('../components/auth/EnrollTotpScreen')
const importForgot = () => import('../components/auth/ForgotScreen')
const TwoFactorScreen = lazy(() =>
  importTwoFactor().then((m) => ({ default: m.TwoFactorScreen })))
const EnrollTotpScreen = lazy(() =>
  importEnrollTotp().then((m) => ({ default: m.EnrollTotpScreen })))
const ResetScreen = lazy(() =>
  import('../components/auth/ResetScreen').then((m) => ({ default: m.ResetScreen })))
const ForgotScreen = lazy(() => importForgot().then((m) => ({ default: m.ForgotScreen })))
const VerifyEmailScreen = lazy(() =>
  import('../components/auth/VerifyEmailScreen').then((m) => ({ default: m.VerifyEmailScreen })))
const ConvertScreen = lazy(() =>
  import('../components/auth/ConvertScreen').then((m) => ({ default: m.ConvertScreen })))

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
  const [resetToken, setResetToken] = useState(urlTokens.reset ?? '')
  const [verifyToken, setVerifyToken] = useState(urlTokens.verify ?? '')
  const [changeToken, setChangeToken] = useState(urlTokens.emailChange ?? '')
  const [forgot, setForgot] = useState(false)

  // Warmed while the pre-auth form is on screen, so the swap has nothing left to
  // fetch. Two of the three are one submit away — a second factor is the DEFAULT
  // here (AUTH_REQUIRE_2FA_FOR_ADMINS) — and the third is one click away, on the
  // form's own "forgot your password?". Paying for the chunk at that moment is
  // what put a full-viewport spinner in the middle of a flow.
  //
  // The body no-ops for anyone already signed in, which is the visit the split
  // was made for.
  const preAuthFormVisible =
    session.status === 'anonymous' || session.status === 'setup_required'
  useEffect(() => {
    if (!preAuthFormVisible) return
    void importTwoFactor()
    void importEnrollTotp()
    void importForgot()
  }, [preAuthFormVisible])

  const boot = (
    <div className="fx-auth fx-auth-boot" role="status" aria-live="polite">
      <span className="fx-auth-spinner fx-auth-spinner-lg" aria-hidden="true" />
      <span className="fx-auth-boot-label">{t('auth.loading')}</span>
    </div>
  )

  if (session.status === 'loading') return boot

  // Above the authenticated short-circuit on purpose: a signed-in user who
  // follows the link from their inbox must still be told whether it worked,
  // rather than being dropped into the app with no feedback at all.
  if (verifyToken) {
    return (
      <Suspense fallback={boot}>
        <VerifyEmailScreen token={verifyToken} onDone={() => setVerifyToken('')} />
      </Suspense>
    )
  }

  // Also above the authenticated short-circuit, and for a stronger reason: this
  // one REVOKES every session, so whoever followed the link is about to become
  // anonymous whether they are told or not. Dropping them into the app — or
  // into a bare login form — with no explanation is the one outcome to avoid.
  if (changeToken) {
    return (
      <Suspense fallback={boot}>
        <VerifyEmailScreen
          kind="email-change"
          token={changeToken}
          onDone={() => setChangeToken('')}
        />
      </Suspense>
    )
  }

  if (session.status === 'authenticated') return <>{children}</>

  // The lazy screens need a boundary, and the boot spinner is the right
  // fallback: chunk-loading is indistinguishable from the /me probe as far as
  // the user is concerned, so reusing it avoids a second, differently-shaped
  // loading state one render apart from the first.
  return <Suspense fallback={boot}>{screenFor()}</Suspense>

  function screenFor() {
    // A half-finished login outranks every other screen, including the invite
    // and reset tokens: the user is mid-flow with a live pre-auth challenge,
    // and dropping them anywhere else would strand it.
    if (session.status === 'two_factor_required') {
      return session.pending.purpose === 'enroll_2fa' ? (
        <EnrollTotpScreen />
      ) : (
        <TwoFactorScreen pending={session.pending} />
      )
    }

    // Same reasoning, one flow over: a live convert challenge is a half-finished
    // sign-in with a 10-minute pre-auth cookie behind it. Showing anything else
    // strands it, and the user has no way back to this screen except starting
    // the whole Google round-trip again.
    if (session.status === 'convert_password_account') {
      return <ConvertScreen email={session.email} />
    }

    // A reset token means the user is holding a credential that expires in 30
    // minutes — honour it before the ordinary login form.
    if (resetToken) {
      return <ResetScreen token={resetToken} onGiveUp={() => setResetToken('')} />
    }

    // An invite token outranks both remaining screens: someone arriving with
    // one is trying to CREATE the account, so showing them a login form they
    // cannot satisfy would be a dead end.
    if (inviteToken) {
      return <InviteScreen token={inviteToken} onGiveUp={() => setInviteToken('')} />
    }

    if (session.status === 'setup_required') return <SetupScreen />

    if (forgot) return <ForgotScreen onBack={() => setForgot(false)} />

    // A transition for the same reason applySession uses one: this swaps a lazy
    // screen in over a form that is already painted, and as an urgent update
    // React would commit the boot overlay over the whole viewport.
    return <LoginScreen onForgotPassword={() => startTransition(() => setForgot(true))} />
  }
}
