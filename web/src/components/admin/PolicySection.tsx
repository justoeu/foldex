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
 *
 * Layout: one card, three side-by-side rows (what-this-governs on the left,
 * controls on the right) separated by rules — each policy group reads as its
 * own decision instead of one undifferentiated pile of fields.
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
      <div className="fx-card-body fx-policy-body">
        {!canWrite && <div className="fx-chip fx-chip-warn">{t('admin.policy_read_only')}</div>}

        <fieldset className="fx-policy-groups" disabled={!canWrite || save.isPending}>
          <section className="fx-policy-row">
            <div className="fx-policy-side">
              <h3 className="fx-panel-title">{t('admin.policy_group_credentials')}</h3>
              <p className="fx-panel-desc">{t('admin.policy_floor_hint')}</p>
            </div>
            <div className="fx-policy-controls">
              <div className="fx-policy-fields">
                <label className="fx-field">
                  <span className="fx-field-label">{t('admin.policy_password_min')}</span>
                  <input
                    className="fx-input" type="number" min={8} max={72}
                    value={draft.password_min_length}
                    onChange={(e) => patch({ password_min_length: Number(e.target.value) })}
                  />
                  <span className="fx-field-hint">{t('admin.policy_password_min_hint')}</span>
                </label>
                <label className="fx-field">
                  <span className="fx-field-label">{t('admin.policy_otp_ttl')}</span>
                  <input
                    className="fx-input" type="number" min={1} max={30}
                    value={draft.otp_ttl_minutes}
                    onChange={(e) => patch({ otp_ttl_minutes: Number(e.target.value) })}
                  />
                  <span className="fx-field-hint">{t('admin.policy_otp_ttl_hint')}</span>
                </label>
                <label className="fx-field">
                  <span className="fx-field-label">{t('admin.policy_otp_cooldown')}</span>
                  <input
                    className="fx-input" type="number" min={30} max={600}
                    value={draft.otp_cooldown_seconds}
                    onChange={(e) => patch({ otp_cooldown_seconds: Number(e.target.value) })}
                  />
                  <span className="fx-field-hint">{t('admin.policy_otp_cooldown_hint')}</span>
                </label>
              </div>
            </div>
          </section>

          <section className="fx-policy-row">
            <div className="fx-policy-side">
              <h3 className="fx-panel-title">{t('admin.policy_group_admin')}</h3>
              <p className="fx-panel-desc">{t('admin.policy_admin_factor_hint')}</p>
            </div>
            <div className="fx-policy-controls">
              <label className="fx-field fx-policy-narrow">
                <span className="fx-field-label">{t('admin.policy_admin_factor')}</span>
                <select
                  className="fx-input"
                  value={draft.admin_second_factor}
                  onChange={(e) => patch({ admin_second_factor: e.target.value as AdminFactorMode })}
                >
                  <option value="any">{t('admin.policy_admin_factor_any')}</option>
                  <option value="totp_only">{t('admin.policy_admin_factor_totp')}</option>
                </select>
              </label>
            </div>
          </section>

          <section className="fx-policy-row">
            <div className="fx-policy-side">
              <h3 className="fx-panel-title">{t('admin.policy_group_google')}</h3>
              <p className="fx-panel-desc">{t('admin.policy_group_google_desc')}</p>
            </div>
            <div className="fx-policy-controls">
              <label className="fx-field">
                <span className="fx-field-label">{t('admin.policy_domains')}</span>
                <textarea
                  className="fx-input" rows={3} value={domainsText}
                  placeholder="example.com"
                  onChange={(e) => setDomainsText(e.target.value)}
                />
                <span className="fx-field-hint">{t('admin.policy_domains_hint')}</span>
              </label>

              <label className="fx-toggle-row">
                <input
                  type="checkbox" checked={draft.google_auto_provision}
                  onChange={(e) => patch({ google_auto_provision: e.target.checked })}
                  aria-label={t('admin.policy_auto_provision')}
                />
                <span className="fx-toggle-track"><span className="fx-toggle-knob" /></span>
                <span className="fx-toggle-label">
                  {t('admin.policy_auto_provision')}
                  <span className="fx-toggle-hint">{t('admin.policy_auto_provision_desc')}</span>
                </span>
              </label>

              {draft.google_auto_provision && (
                <label className="fx-field fx-policy-narrow">
                  <span className="fx-field-label">{t('admin.policy_default_role')}</span>
                  <select
                    className="fx-input" value={draft.google_default_role}
                    onChange={(e) => patch({ google_default_role: e.target.value as InstancePolicy['google_default_role'] })}
                  >
                    <option value="editor">{t('admin.role_editor')}</option>
                    <option value="viewer">{t('admin.role_viewer')}</option>
                  </select>
                </label>
              )}
            </div>
          </section>
        </fieldset>

        {(error || canWrite) && (
          <div className="fx-policy-footer">
            {error ? <div className="fx-error">{error}</div> : <span />}
            {canWrite && (
              <button className="fx-btn fx-btn-primary" type="submit" disabled={save.isPending}>
                {save.isPending ? t('common.saving') : t('common.save')}
              </button>
            )}
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
