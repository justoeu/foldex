import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  abusePolicyQueryKey, fetchAbusePolicy, saveAbusePolicy,
  type AbuseBound, type AbuseObserved, type AbusePolicy, type AbusePolicyResponse,
} from '../../api/admin'
import { apiErrorMessage } from '../../lib/apiError'
import {
  ABUSE_BANDS, COALESCE_FIELD, boundOf, observedFor, restoreBandDefaults,
  type AbuseBand, type AbuseField,
} from './abuseFormat'

/**
 * The nine numbers that decide when a caller is abusing this instance.
 *
 * Four bands, not four cards (INV-146): they are one decision taken in four
 * parts, and boxing each one would suggest they can be saved separately.
 *
 * Two properties of this screen are load-bearing and both are about NOT holding
 * a second copy of the server's rules:
 *
 *   - Every min, max and default is read from the `bounds` the payload carries.
 *     A copy in TypeScript is the copy that goes stale, and the direction
 *     nobody notices is a form offering a range the server refuses.
 *   - A refused write is rendered VERBATIM. The server's sentence names the
 *     field and the two real numbers on purpose; rewriting it into "invalid
 *     value" removes the only part that says what to type instead.
 *
 * And one that is about the reader: three of the nine knobs REFUSE nothing.
 * They decide what the anomalies panel calls anomalous, and their band says so
 * in its own words. An operator who confuses the two tightens the threshold
 * believing they tightened the defence, and the instance is no safer.
 */
export function AbuseSection() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const query = useQuery({ queryKey: abusePolicyQueryKey, queryFn: fetchAbusePolicy })

  const [draft, setDraft] = useState<AbusePolicy | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  // Seeds the form once the server answers, and only while nothing is being
  // edited — the same shape the instance policy form uses, so a refetch cannot
  // overwrite a number half-typed.
  useEffect(() => {
    if (query.data && draft === null) setDraft(seed(query.data))
  }, [query.data, draft])

  const save = useMutation({
    mutationFn: saveAbusePolicy,
    onSuccess: (policy) => {
      qc.setQueryData<AbusePolicyResponse>(abusePolicyQueryKey, (prev) =>
        prev ? { ...prev, policy } : prev)
      setDraft(policy)
      setError(null)
      setSaved(true)
    },
    onError: (e) => {
      setSaved(false)
      setError(apiErrorMessage(e) ?? t('admin.abuse_save_failed'))
    },
  })

  if (query.isPending) return <div className="fx-empty">{t('common.loading')}</div>
  if (query.isError || !query.data || !draft) {
    return <div className="fx-empty">{t('admin.abuse_unavailable')}</div>
  }

  // Read from the payload, not re-derived as `role === 'owner'` the way the
  // instance policy form does it. Same reason `RoleSummary.editable` is a
  // server field: two copies of one rule drift, and the direction nobody
  // notices is a screen offering a save the server refuses.
  const { bounds, observed, can_write: canWrite } = query.data

  const patch = (field: AbuseField, value: number) => {
    setSaved(false)
    setDraft({ ...draft, [field]: value })
  }

  const restore = (band: AbuseBand) => {
    setSaved(false)
    setDraft(restoreBandDefaults(draft, bounds, band))
  }

  return (
    <form
      className="fx-card"
      // The browser's own bubble would preempt the server's sentence with a
      // generic one, and the server's is the sentence this screen promises to
      // show unchanged. The min/max attributes stay: they still drive the
      // stepper and they still tell the reader the range.
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        save.mutate(draft)
      }}
    >
      <div className="fx-card-body fx-policy-body">
        {!canWrite && <div className="fx-chip fx-chip-warn">{t('admin.abuse_read_only')}</div>}

        <fieldset className="fx-policy-groups" disabled={!canWrite || save.isPending}>
          {ABUSE_BANDS.map((band) => (
            <Band
              key={band.id}
              band={band}
              draft={draft}
              bounds={bounds}
              observed={observed}
              canWrite={canWrite}
              onPatch={patch}
              onRestore={restore}
            />
          ))}
        </fieldset>

        <div className="fx-policy-footer">
          <div className="fx-abuse-status">
            {error && <p className="fx-aud-error" role="alert">{error}</p>}
            {!error && saved && <p className="fx-abuse-saved">{t('admin.abuse_saved')}</p>}
          </div>
          {canWrite && (
            <button className="fx-btn fx-btn-primary" type="submit" disabled={save.isPending}>
              {save.isPending ? t('common.saving') : t('common.save')}
            </button>
          )}
        </div>
      </div>
    </form>
  )
}

function Band({
  band, draft, bounds, observed, canWrite, onPatch, onRestore,
}: {
  band: AbuseBand
  draft: AbusePolicy
  bounds: AbuseBound[]
  observed: AbuseObserved
  canWrite: boolean
  onPatch: (field: AbuseField, value: number) => void
  onRestore: (band: AbuseBand) => void
}) {
  const { t } = useTranslation()
  const titleId = `fx-abuse-band-${band.id}`
  return (
    <section className="fx-policy-row" aria-labelledby={titleId}>
      <div className="fx-policy-side">
        <h3 className="fx-panel-title" id={titleId}>{t(`admin.abuse_band_${band.id}`)}</h3>
        <p className="fx-panel-desc">{t(`admin.abuse_band_${band.id}_desc`)}</p>
        {/* The disclaimer belongs to the band, beside the numbers it is about,
            not to a footnote at the bottom of a form nobody scrolls to. */}
        {!band.enforces && (
          <p className="fx-abuse-note">{t('admin.abuse_detection_note')}</p>
        )}
        {canWrite && (
          <button
            type="button"
            className="fx-abuse-reset"
            onClick={() => onRestore(band)}
          >
            {t('admin.abuse_restore')}
          </button>
        )}
      </div>
      <div className="fx-policy-controls">
        <div className="fx-policy-fields">
          {band.fields.map((field) => (
            <Knob
              key={field}
              field={field}
              value={draft[field] ?? 0}
              bound={boundOf(bounds, field)}
              observed={observed}
              onPatch={onPatch}
            />
          ))}
        </div>
      </div>
    </section>
  )
}

/**
 * One number, under the range the server advertised.
 *
 * A knob with no bound in the payload still renders, without min/max and
 * without the range hint. Dropping it would hide a setting the policy carries
 * because an aggregate went missing, which reads as a broken screen rather
 * than as the degraded payload it is.
 */
function Knob({
  field, value, bound, observed, onPatch,
}: {
  field: AbuseField
  value: number
  bound: AbuseBound | null
  observed: AbuseObserved
  onPatch: (field: AbuseField, value: number) => void
}) {
  const { t } = useTranslation()
  const seen = observedFor(field, observed)
  return (
    <label className="fx-field">
      <span className="fx-field-label">{t(`admin.abuse_field_${field}`)}</span>
      <input
        className="fx-input"
        type="number"
        min={bound?.min}
        max={bound?.max}
        value={value}
        onChange={(e) => onPatch(field, Number(e.target.value))}
      />
      {bound && (
        <span className="fx-field-hint">
          {t('admin.abuse_range', { min: bound.min, max: bound.max, def: bound.default })}
        </span>
      )}
      {field === COALESCE_FIELD && (
        <span className="fx-field-hint">{t('admin.abuse_coalesce_off')}</span>
      )}
      {seen !== null && (
        <span className="fx-field-hint fx-abuse-observed">
          {seen > 0
            ? t('admin.abuse_observed', { days: observed.days, value: seen })
            : t('admin.abuse_observed_none')}
        </span>
      )}
    </label>
  )
}

/**
 * Resolves the one nullable knob before it reaches an input.
 *
 * null means "use the default" on the wire, and an input bound to null renders
 * empty — an empty box the owner would save as zero by accident, which for
 * this knob is not "unset" but "coalescing off". Resolving it here makes the
 * form show the number that is actually in force.
 */
function seed(res: AbusePolicyResponse): AbusePolicy {
  const { policy, bounds } = res
  if (policy[COALESCE_FIELD] !== null) return policy
  return {
    ...policy,
    public_click_coalesce_seconds: boundOf(bounds, COALESCE_FIELD)?.default ?? 0,
  }
}
