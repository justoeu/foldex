import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Icon, I } from './icons'
import { Notice, SectionBlock, SectionCard, SectionRow } from './account/SectionCard'
import { useConfirm } from './ConfirmDialog'
import { listTokens, createToken, revokeToken, type ApiToken } from '../api/tokens'
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

  async function askRevoke(tok: ApiToken) {
    const ok = await confirmAction({
      title: t('tokens.revoke_title'),
      message: t('tokens.revoke_message', { name: tok.name }),
      destructive: true,
    })
    if (ok) revoke.mutate(tok.id)
  }

  return (
    <SectionCard icon={I.link} title={t('tokens.card_title')} subtitle={t('tokens.section_desc')}>
      {error && <Notice tone="bad">{error}</Notice>}

      {/* The one and only display of the plaintext. The server keeps sha256,
          so this is not a convenience that was skipped — showing it again is
          genuinely impossible. That is why it gets a band of its own rather
          than a line in the list: it has to be copied before it is dismissed. */}
      {created?.token && (
        <SecretBand
          label={t('tokens.created_title')}
          value={created.token}
          testId="new-token"
          hint={<Notice tone="info">{t('tokens.created_warning')}</Notice>}
        >
          <button className="fx-btn fx-btn-primary" onClick={() => setCreated(null)}>
            {t('tokens.done')}
          </button>
        </SecretBand>
      )}

      <SectionBlock label={t('tokens.list_label')}>
        <div className="fx-sec-rows">
          {(tokens.data ?? []).map((tok) => (
            <SectionRow
              key={tok.id}
              icon={I.link}
              name={tok.name}
              hint={
                tok.last_used_at
                  ? t('tokens.last_used', { when: new Date(tok.last_used_at).toLocaleDateString() })
                  : t('tokens.never_used')
              }
              action={
                <button
                  className="fx-btn fx-btn-danger"
                  aria-label={t('tokens.revoke_label', { name: tok.name })}
                  onClick={() => void askRevoke(tok)}
                >
                  <Icon d={I.trash} size={13} /> {t('tokens.revoke')}
                </button>
              }
            />
          ))}
          {tokens.data?.length === 0 && <Notice tone="info">{t('tokens.empty')}</Notice>}
        </div>
      </SectionBlock>

      <div className="fx-sec-form">
        <label className="fx-field">
          <span className="fx-field-label">{t('tokens.name_label')}</span>
          <input
            className="fx-input"
            value={name}
            placeholder={t('tokens.name_placeholder')}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <div className="fx-sec-actions">
          <button
            className="fx-btn fx-btn-primary"
            disabled={create.isPending || !name.trim()}
            onClick={() => create.mutate()}
          >
            {t('tokens.create')}
          </button>
        </div>
      </div>
    </SectionCard>
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
