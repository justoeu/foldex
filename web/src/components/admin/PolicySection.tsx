import { useEffect, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { savePolicy, type AdminFactorMode, type InstancePolicy } from '../../api/admin'
import { INSTANCE_POLICY_KEY, useInstancePolicy } from '../../hooks/useInstancePolicy'
import { apiErrorCode as errCode } from '../../lib/apiError'
import { useCurrentUser } from '../../auth/AuthProvider'

/**
 * The owner-editable instance rules: password floor, mailed-code lifetime, and
 * who may join through Google.
 *
 * An admin sees this read-only. That mirrors the server, where PermPolicyRead
 * is granted to both roles and PermPolicyWrite only to the owner — an admin who
 * could lower the password floor or widen the Google allowlist could lower the
 * instance's security and then walk in through the gap.
 */
export function PolicySection() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const canWrite = useCurrentUser()?.role === 'owner'

  const query = useInstancePolicy()
  const [draft, setDraft] = useState<InstancePolicy | null>(null)
  const [domainsText, setDomainsText] = useState('')
  const [error, setError] = useState('')

  // Seeds the form once the server answers, and re-seeds if the query refetches
  // while nothing is being edited. Keyed on the query data rather than done in
  // the queryFn so the form stays the single owner of in-progress edits.
  useEffect(() => {
    if (query.policy && draft === null) {
      setDraft(query.policy)
      setDomainsText(query.policy.google_allowed_domains.join('\n'))
    }
  }, [query.policy, draft])

  const save = useMutation({
    mutationFn: savePolicy,
    onSuccess: (saved) => {
      qc.setQueryData(INSTANCE_POLICY_KEY, saved)
      setDraft(saved)
      setDomainsText(saved.google_allowed_domains.join('\n'))
      setError('')
    },
    onError: (e) => setError(messageFor(errCode(e) ?? '', t)),
  })

  if (query.isPending) return <div className="fx-empty">{t('common.loading')}</div>
  if (query.isError || !draft) return <div className="fx-empty">{t('admin.policy_unavailable')}</div>

  const patch = (p: Partial<InstancePolicy>) => setDraft({ ...draft, ...p })

  return (
    <form
      className="fx-card"
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate({
          ...draft,
          // Parsed at submit rather than on every keystroke: splitting a
          // half-typed line would drop the domain the user is mid-way through.
          google_allowed_domains: domainsText
            .split(/[\s,]+/)
            .map((d) => d.trim().toLowerCase())
            .filter(Boolean),
        })
      }}
    >
      <div className="fx-card-body" style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
        {!canWrite && <div className="fx-chip fx-chip-warn">{t('admin.policy_read_only')}</div>}

        <fieldset disabled={!canWrite || save.isPending} style={{ border: 0, padding: 0, margin: 0, display: 'contents' }}>
          <div>
            <p className="fx-hub-section-label">{t('admin.policy_group_credentials')}</p>
            <div style={{ display: 'grid', gap: 12, gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', marginTop: 10 }}>
              <label className="fx-field">
                <span className="fx-field-label">{t('admin.policy_password_min')}</span>
                <input
                  className="fx-input" type="number" min={8} max={72}
                  value={draft.password_min_length}
                  onChange={(e) => patch({ password_min_length: Number(e.target.value) })}
                />
              </label>
              <label className="fx-field">
                <span className="fx-field-label">{t('admin.policy_otp_ttl')}</span>
                <input
                  className="fx-input" type="number" min={1} max={30}
                  value={draft.otp_ttl_minutes}
                  onChange={(e) => patch({ otp_ttl_minutes: Number(e.target.value) })}
                />
              </label>
              <label className="fx-field">
                <span className="fx-field-label">{t('admin.policy_otp_cooldown')}</span>
                <input
                  className="fx-input" type="number" min={30} max={600}
                  value={draft.otp_cooldown_seconds}
                  onChange={(e) => patch({ otp_cooldown_seconds: Number(e.target.value) })}
                />
              </label>
            </div>
            <label className="fx-field" style={{ marginTop: 12, maxWidth: 320 }}>
              <span className="fx-field-label">{t('admin.policy_admin_factor')}</span>
              <select
                className="fx-input"
                value={draft.admin_second_factor}
                onChange={(e) => patch({ admin_second_factor: e.target.value as AdminFactorMode })}
              >
                <option value="any">{t('admin.policy_admin_factor_any')}</option>
                <option value="totp_only">{t('admin.policy_admin_factor_totp')}</option>
              </select>
              <span className="fx-field-hint">{t('admin.policy_admin_factor_hint')}</span>
            </label>
            <p className="fx-hint" style={{ marginTop: 8 }}>{t('admin.policy_floor_hint')}</p>
          </div>

          <div>
            <p className="fx-hub-section-label">{t('admin.policy_group_google')}</p>
            <label className="fx-field" style={{ marginTop: 10 }}>
              <span className="fx-field-label">{t('admin.policy_domains')}</span>
              <textarea
                className="fx-input" rows={3} value={domainsText}
                placeholder="example.com"
                onChange={(e) => setDomainsText(e.target.value)}
              />
            </label>
            <p className="fx-hint" style={{ marginTop: 6 }}>{t('admin.policy_domains_hint')}</p>

            <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start', marginTop: 12 }}>
              <input
                type="checkbox" checked={draft.google_auto_provision}
                onChange={(e) => patch({ google_auto_provision: e.target.checked })}
              />
              <span>
                <span className="fx-acard-title">{t('admin.policy_auto_provision')}</span>
                <span className="fx-acard-desc" style={{ display: 'block' }}>
                  {t('admin.policy_auto_provision_desc')}
                </span>
              </span>
            </label>

            <label className="fx-field" style={{ marginTop: 12, maxWidth: 240 }}>
              <span className="fx-field-label">{t('admin.policy_default_role')}</span>
              <select
                className="fx-input" value={draft.google_default_role}
                onChange={(e) => patch({ google_default_role: e.target.value as InstancePolicy['google_default_role'] })}
              >
                <option value="editor">{t('admin.role_editor')}</option>
                <option value="viewer">{t('admin.role_viewer')}</option>
              </select>
            </label>
          </div>
        </fieldset>

        {error && <div className="fx-error">{error}</div>}

        {canWrite && (
          <div>
            <button className="fx-btn fx-btn-primary" type="submit" disabled={save.isPending}>
              {save.isPending ? t('common.saving') : t('common.save')}
            </button>
          </div>
        )}
      </div>
    </form>
  )
}

function messageFor(code: string, t: (k: string) => string): string {
  if (code === 'invalid_policy') return t('admin.policy_invalid')
  if (code === 'forbidden_role') return t('admin.policy_read_only')
  return t('common.error')
}
