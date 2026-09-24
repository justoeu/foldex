import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { http } from '../../api/client'
import { relativeTimeLong } from '../../lib/time'
import { useConfirm } from '../ConfirmDialog'

type SessionRow = {
  id: number
  created_at: string
  last_seen_at: string
  user_agent?: string
  ip?: string
  current: boolean
}

/** The list is the caller's own — owner-scoped on the server. */
async function listSessions(): Promise<SessionRow[]> {
  const { data } = await http.get<{ sessions: SessionRow[] }>('/api/auth/sessions')
  return data.sessions
}

async function revokeSession(id: number): Promise<void> {
  await http.delete(`/api/auth/sessions/${id}`)
}

/**
 * Parses just enough of a UA to name the device: browser family and OS
 * family, the two words a person uses to tell their machines apart. Anything
 * unrecognized falls back to the raw string — a wrong label ("Chrome on
 * macOS" for an Edge-on-Windows box) is worse than an ugly true one, because
 * the user decides which session to kill based on the name.
 */
function deviceLabel(ua: string | undefined): string {
  if (!ua) return ''
  const browser = /Edg\//.test(ua)
    ? 'Edge'
    : /OPR\//.test(ua)
      ? 'Opera'
      : /Firefox\//.test(ua)
        ? 'Firefox'
        : /Chrome\//.test(ua)
          ? 'Chrome'
          : /Safari\//.test(ua)
            ? 'Safari'
            : ''
  const os = /Windows/.test(ua)
    ? 'Windows'
    : /Android/.test(ua)
      ? 'Android'
      : /iPhone|iPad/.test(ua)
        ? 'iOS'
        : /Mac OS X/.test(ua)
          ? 'macOS'
          : /Linux/.test(ua)
            ? 'Linux'
            : ''
  return [browser, os].filter(Boolean).join(' · ')
}

/**
 * Where this account is connected, as a list — the thing people expect when a
 * screen is called "Sessões", which the previous bulk-only panel deferred on
 * purpose. Each row revokes one session (`DELETE /api/auth/sessions/{id}`);
 * "sair de todos" stays as the nuclear row below the card, and the note about
 * API tokens surviving both actions keeps its place — an extension that keeps
 * working after "sign out everywhere" is otherwise a surprise.
 */
export function SessionsSection({
  onSignOut,
  onSignOutEverywhere,
}: Readonly<{
  onSignOut: () => void
  onSignOutEverywhere: () => void
}>) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const confirmAction = useConfirm()

  const sessions = useQuery({ queryKey: ['auth-sessions'], queryFn: listSessions })

  const [revokeError, setRevokeError] = useState(false)
  const revoke = useMutation({
    mutationFn: revokeSession,
    onSuccess: () => {
      setRevokeError(false)
      void qc.invalidateQueries({ queryKey: ['auth-sessions'] })
    },
    // Killing an unrecognized session is the one action this screen exists
    // for; a silent failure would leave the user believing the intruder is
    // out when the row is still live.
    onError: () => setRevokeError(true),
  })

  async function askRevoke(row: SessionRow) {
    const ok = await confirmAction({
      title: t('account.session_revoke_title'),
      message: t('account.session_revoke_message', {
        device: deviceLabel(row.user_agent) || t('account.session_unknown_device'),
      }),
      destructive: true,
    })
    if (ok) revoke.mutate(row.id)
  }

  const rows = sessions.data ?? []

  return (
    <div>
      {revokeError && (
        <div className="fx-acc2-empty fx-acc2-alert" role="alert">
          {t('account.session_revoke_failed')}
        </div>
      )}
      {sessions.isPending && <div className="fx-acc2-empty fx-acc2-empty-soft">{t('common.loading')}</div>}
      {sessions.isError && <div className="fx-acc2-empty">{t('account.sessions_unavailable')}</div>}

      {!sessions.isPending && !sessions.isError && (
        <div className="fx-acc2-card">
          {rows.length === 0 && (
            <div className="fx-acc2-empty fx-acc2-empty-soft">{t('account.sessions_empty')}</div>
          )}
          {rows.map((row) => (
            <div className="fx-acc2-row" key={row.id}>
              <div className="fx-acc2-row-main">
                <div className="fx-acc2-row-title-wrap">
                  <span className="fx-acc2-row-title">
                    {deviceLabel(row.user_agent) || t('account.session_unknown_device')}
                  </span>
                  {row.current && <span className="fx-acc2-pill fx-acc2-pill-current">{t('account.session_current')}</span>}
                </div>
                <div className="fx-acc2-row-meta">
                  {row.ip && <code className="fx-acc2-mono">{row.ip}</code>}
                  <span>{relativeTimeLong(row.last_seen_at, t)}</span>
                </div>
              </div>
              {row.current ? (
                <button type="button" className="fx-acc2-btn-outline" onClick={onSignOut}>
                  {t('auth.sign_out')}
                </button>
              ) : (
                <button
                  type="button"
                  className="fx-acc2-btn-danger"
                  disabled={revoke.isPending}
                  onClick={() => void askRevoke(row)}
                >
                  {t('account.session_revoke')}
                </button>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Tinted before the click, not only in the confirmation that follows:
          the card is the last chance to stop; the row is where the user
          decides which of the two exits they meant. */}
      <div className="fx-acc2-logout-all">
        <div className="fx-acc2-row-main">
          <div className="fx-acc2-row-title">{t('profile.logout_all_action')}</div>
          <div className="fx-acc2-row-sub">{t('account.sign_out_all_hint')}</div>
        </div>
        <button type="button" className="fx-acc2-btn-danger" onClick={onSignOutEverywhere}>
          {t('profile.logout_all_action')}
        </button>
      </div>

      {/* API tokens are NOT sessions and survive both actions. Stated here
          because "sign out everywhere" reads like it covers everything, and an
          extension that keeps working afterwards is otherwise a surprise. */}
      <p className="fx-acc2-lede fx-acc2-mt12">
        {t('account.group_sessions_hint')}
      </p>
    </div>
  )
}
