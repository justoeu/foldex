import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Icon, I } from './icons'
import { Notice, SectionBlock, SectionCard, SectionRow } from './account/SectionCard'
import { useConfirm } from './ConfirmDialog'
import { listTokens, createToken, revokeToken, rotateToken, type ApiToken } from '../api/tokens'
import { apiErrorCode as errCode } from '../lib/apiError'
import { SecretBand } from './SecretBand'

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
  const [error, setError] = useState('')

  const create = useMutation({
    mutationFn: () => createToken(name.trim()),
    onSuccess: async (tok) => {
      setCreated(tok)
      setName('')
      setError('')
      await qc.invalidateQueries({ queryKey: ['api-tokens'] })
    },
    onError: (err) => setError(messageFor(err, t)),
  })

  const revoke = useMutation({
    mutationFn: revokeToken,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['api-tokens'] }),
    onError: (err) => setError(messageFor(err, t)),
  })

  const rotate = useMutation({
    mutationFn: rotateToken,
    onSuccess: async (tok) => {
      setCreated(tok)
      setName('')
      setError('')
      await qc.invalidateQueries({ queryKey: ['api-tokens'] })
    },
    onError: (err) => {
      // Whatever broke, the revoke half may already have landed: refetch so
      // the list never shows a revoked token as live (rotating a ghost row
      // would 404).
      void qc.invalidateQueries({ queryKey: ['api-tokens'] })
      setError(
        // The cap copy stays truthful in the one race that can produce it
        // here (another token created between the two halves); every other
        // failure needs the pair-specific wording, because the generic
        // message would leave the user believing the old token still works.
        errCode(err) === 'too_many_tokens' ? messageFor(err, t) : t('tokens.rotate_failed'),
      )
    },
  })

  async function askRevoke(tok: ApiToken) {
    const ok = await confirmAction({
      title: t('tokens.revoke_title'),
      message: t('tokens.revoke_message', { name: tok.name }),
      destructive: true,
    })
    if (ok) revoke.mutate(tok.id)
  }

  async function askRotate(tok: ApiToken) {
    const ok = await confirmAction({
      title: t('tokens.rotate_title'),
      message: t('tokens.rotate_message', { name: tok.name }),
      destructive: true,
    })
    if (ok) rotate.mutate(tok)
  }

  return (
    <div>
      {error && <div className="fx-acc2-empty" role="alert" style={{ marginBottom: 16, padding: '14px 20px', textAlign: 'start' }}>{error}</div>}

      {/* The one and only display of the plaintext. The server keeps sha256,
          so this is not a convenience that was skipped — showing it again is
          genuinely impossible. That is why it gets a card of its own rather
          than a line in the list: it has to be copied before it is dismissed. */}
      {created?.token && (
        <div className="fx-acc2-token-new" data-testid="new-token-band">
          <div className="fx-acc2-token-new-title">{t('tokens.created_title')}</div>
          <div className="fx-acc2-token-new-sub">{t('tokens.created_warning')}</div>
          <div className="fx-acc2-token-new-actions">
            <code className="fx-acc2-token-value" data-testid="new-token">{created.token}</code>
            <CopyToken value={created.token} />
            <button type="button" className="fx-acc2-btn-outline" onClick={() => setCreated(null)}>
              {t('tokens.done')}
            </button>
          </div>
        </div>
      )}

      <div className="fx-acc2-card" style={{ marginBottom: 16 }}>
        <div className="fx-acc2-card-body">
          <form
            className="fx-acc2-token-form"
            onSubmit={(e) => {
              e.preventDefault()
              if (name.trim() && !create.isPending) create.mutate()
            }}
          >
            <input
              className="fx-acc2-input fx-acc2-token-name"
              value={name}
              placeholder={t('tokens.name_placeholder')}
              aria-label={t('tokens.name_label')}
              onChange={(e) => setName(e.target.value)}
            />
            <button
              type="submit"
              className="fx-acc2-btn fx-acc2-token-create"
              disabled={create.isPending || !name.trim()}
            >
              {t('tokens.create')}
            </button>
          </form>
        </div>
      </div>

      <div className="fx-acc2-card">
        <div className="fx-acc2-card-head">
          <span>{t('tokens.list_label')}</span>
          <span className="fx-acc2-card-count">{tokens.data?.length ?? 0}</span>
        </div>
        {tokens.data?.length === 0 && (
          <div className="fx-acc2-empty fx-acc2-empty-soft">{t('tokens.empty')}</div>
        )}
        {(tokens.data ?? []).map((tok) => (
          <div className="fx-acc2-row" key={tok.id}>
            <div className="fx-acc2-row-main">
              <div className="fx-acc2-row-title">{tok.name}</div>
              <div className="fx-acc2-row-meta">
                <code className="fx-acc2-mono">fx_{tok.id}…</code>
                <span className="fx-acc2-dot-sep">·</span>
                <span>
                  {tok.last_used_at
                    ? t('tokens.last_used', { when: new Date(tok.last_used_at).toLocaleDateString() })
                    : t('tokens.never_used')}
                </span>
              </div>
            </div>
            <button
              type="button"
              className="fx-acc2-btn-outline"
              aria-label={t('tokens.rotate_label', { name: tok.name })}
              disabled={rotate.isPending}
              onClick={() => void askRotate(tok)}
            >
              {t('tokens.rotate')}
            </button>
            <button
              type="button"
              className="fx-acc2-btn-danger"
              aria-label={t('tokens.revoke_label', { name: tok.name })}
              onClick={() => void askRevoke(tok)}
            >
              {t('tokens.revoke')}
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}

/** Copy button with the "Copiado" confirmation the one-time card needs — the
 *  plaintext leaves the screen forever on dismiss, so the click has to say it
 *  worked without navigating anywhere. */
function CopyToken({ value }: Readonly<{ value: string }>) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)
  return (
    <button
      type="button"
      className="fx-acc2-btn"
      onClick={() => {
        void navigator.clipboard?.writeText(value).catch(() => {})
        setCopied(true)
      }}
    >
      {copied ? t('tokens.copied') : t('tokens.copy')}
    </button>
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
