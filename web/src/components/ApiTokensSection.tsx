import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Icon, I } from './icons'
import { useConfirm } from './ConfirmDialog'
import { listTokens, createToken, revokeToken, type ApiToken } from '../api/tokens'
import { apiErrorCode as errCode } from '../lib/apiError'

/**
 * Long-lived bearer credentials for the browser extension and for scripts.
 *
 * The section exists because the extension cannot hold a session: it has no
 * cookie jar shared with the SPA, and a refresh token that rotates would be
 * useless to a background service worker that may run months apart. A token is
 * the honest alternative — and being honest about it means saying, on screen,
 * that its scope is content only.
 */
export function ApiTokensSection() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const confirmAction = useConfirm()

  const tokens = useQuery({ queryKey: ['api-tokens'], queryFn: listTokens })
  const [name, setName] = useState('')
  const [created, setCreated] = useState<ApiToken | null>(null)
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState('')

  const create = useMutation({
    mutationFn: () => createToken(name.trim()),
    onSuccess: async (tok) => {
      setCreated(tok)
      setName('')
      setError('')
      setCopied(false)
      await qc.invalidateQueries({ queryKey: ['api-tokens'] })
    },
    onError: (err) => setError(messageFor(err, t)),
  })

  const revoke = useMutation({
    mutationFn: revokeToken,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['api-tokens'] }),
    onError: (err) => setError(messageFor(err, t)),
  })

  async function copy(value: string) {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
    } catch {
      // Clipboard access can be denied or absent (insecure context). The value
      // is on screen either way, so failing quietly beats an error the user can
      // do nothing about — the same treatment the recovery-code sheet gets.
    }
  }

  async function askRevoke(tok: ApiToken) {
    const ok = await confirmAction({
      title: t('tokens.revoke_title'),
      message: t('tokens.revoke_message', { name: tok.name }),
      destructive: true,
    })
    if (ok) revoke.mutate(tok.id)
  }

  return (
    <section className="fx-card">
      <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
        <h3
          className="fx-card-title"
          style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}
        >
          <Icon d={I.key} size={15} /> {t('tokens.section_title')}
        </h3>
        <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>{t('tokens.section_desc')}</p>

        {error && (
          <div className="fx-inline-error" role="alert" style={{ fontSize: 12 }}>
            {error}
          </div>
        )}

        {/* The one and only display of the plaintext. The server keeps sha256,
            so this is not a convenience that was skipped — showing it again is
            genuinely impossible. */}
        {created?.token && (
          <div
            style={{
              display: 'grid',
              gap: 8,
              padding: 12,
              borderRadius: 10,
              background: 'var(--fx-surface-2)',
            }}
          >
            <strong style={{ fontSize: 12 }}>{t('tokens.created_title')}</strong>
            <code
              data-testid="new-token"
              style={{ fontSize: 12, wordBreak: 'break-all', fontFamily: 'var(--fx-mono)' }}
            >
              {created.token}
            </code>
            <p style={{ fontSize: 11, color: 'var(--fx-ink-3)', margin: 0 }}>
              {t('tokens.created_warning')}
            </p>
            <div style={{ display: 'flex', gap: 8 }}>
              <button className="fx-btn" onClick={() => void copy(created.token ?? '')}>
                {copied ? t('tokens.copied') : t('tokens.copy')}
              </button>
              <button className="fx-btn" onClick={() => setCreated(null)}>
                {t('tokens.done')}
              </button>
            </div>
          </div>
        )}

        <ul style={{ listStyle: 'none', margin: 0, padding: 0, display: 'grid', gap: 8 }}>
          {(tokens.data ?? []).map((tok) => (
            <li
              key={tok.id}
              style={{ display: 'flex', alignItems: 'center', gap: 10, fontSize: 12 }}
            >
              <span style={{ flex: 1, minWidth: 0 }}>
                <strong>{tok.name}</strong>
                <span style={{ color: 'var(--fx-ink-4)', marginLeft: 8 }}>
                  {tok.last_used_at
                    ? t('tokens.last_used', { when: new Date(tok.last_used_at).toLocaleDateString() })
                    : t('tokens.never_used')}
                </span>
              </span>
              <button
                className="fx-btn"
                aria-label={t('tokens.revoke_label', { name: tok.name })}
                onClick={() => void askRevoke(tok)}
              >
                <Icon d={I.trash} size={13} />
              </button>
            </li>
          ))}
          {tokens.data?.length === 0 && (
            <li style={{ fontSize: 12, color: 'var(--fx-ink-4)' }}>{t('tokens.empty')}</li>
          )}
        </ul>

        <label className="fx-field" style={{ margin: 0 }}>
          <span className="fx-field-label">{t('tokens.name_label')}</span>
          <input
            className="fx-input"
            value={name}
            placeholder={t('tokens.name_placeholder')}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <div>
          <button
            className="fx-btn fx-btn-primary"
            disabled={create.isPending || !name.trim()}
            onClick={() => create.mutate()}
          >
            {t('tokens.create')}
          </button>
        </div>
      </div>
    </section>
  )
}

function messageFor(err: unknown, t: (k: string) => string): string {
  switch (errCode(err)) {
    case 'too_many_tokens':
      return t('tokens.too_many')
    case 'invalid_name':
      return t('tokens.invalid_name')
    default:
      return t('auth_errors.generic')
  }
}
