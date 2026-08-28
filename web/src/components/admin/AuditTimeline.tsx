import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { TFunction } from 'i18next'
import type { AuditEntry } from '../../api/admin'
import { actorOf, severityClass } from './auditFormat'
import { actionLabel } from '../../lib/auditLabels'

/**
 * The trail itself.
 *
 * Each row expands to its context — the address, whether anyone vouched for it,
 * and the device. A <details> would have been fewer lines, but the summary row
 * is a grid of four columns and browsers give `summary` a marker and a display
 * mode that fight it; a button plus `aria-expanded` is the same semantics with
 * layout that survives.
 */
export function AuditTimeline({ entries }: { entries: AuditEntry[] }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState<number | null>(null)
  return (
    <ol className="fx-aud-timeline">
      {entries.map((e) => {
        const expanded = open === e.id
        return (
          <li className="fx-aud-event" key={e.id} data-testid="fx-aud-event">
            <span className={`fx-aud-pip fx-aud-pip-${e.severity}`} aria-hidden="true" />
            <button
              type="button"
              className={'fx-aud-event-row' + (expanded ? ' fx-aud-event-open' : '')}
              aria-expanded={expanded}
              onClick={() => setOpen(expanded ? null : e.id)}
            >
              <time className="fx-aud-when" dateTime={e.created_at}>
                {new Date(e.created_at).toLocaleString()}
              </time>
              <span className={`fx-aud-kind fx-aud-kind-${e.category}`}>
                {actionLabel(t, e.action)}
              </span>
              <span className="fx-aud-event-main">
                <span className="fx-aud-event-summary">{summaryOf(e, t)}</span>
                <span className="fx-aud-event-actor">{actorText(e, t)}</span>
              </span>
              <span className="fx-aud-event-end">
                <span className={severityClass(e.severity)}>
                  {t(`admin.audit_severity_${e.severity}`)}
                </span>
                <span className="fx-aud-caret" aria-hidden="true">{expanded ? '▴' : '▾'}</span>
              </span>
            </button>
            {expanded && (
              <dl className="fx-aud-details">
                <div>
                  <dt>{t('admin.audit_detail_ip')}</dt>
                  <dd>
                    {e.ip ?? t('admin.audit_detail_absent')}
                    {e.ip && (
                      <span className="fx-aud-provenance">
                        {' · '}
                        {e.ip_trusted ? t('admin.audit_ip_trusted') : t('admin.audit_ip_direct')}
                      </span>
                    )}
                  </dd>
                </div>
                <div>
                  <dt>{t('admin.audit_detail_device')}</dt>
                  <dd>{e.user_agent ?? t('admin.audit_detail_absent')}</dd>
                </div>
                <div>
                  <dt>{t('admin.audit_detail_result')}</dt>
                  <dd>{e.detail ?? t('admin.audit_detail_absent')}</dd>
                </div>
                <div>
                  <dt>{t('admin.audit_detail_scope')}</dt>
                  <dd>{t(`admin.audit_category_${e.category}`)}</dd>
                </div>
              </dl>
            )}
          </li>
        )
      })}
    </ol>
  )
}

/**
 * The one-line description.
 *
 * A content row has no subject on this projection — the server withheld it —
 * so it says what KIND of thing changed and nothing about which one. That is
 * the whole point of the split, and phrasing it explicitly here keeps the row
 * from looking like a rendering bug.
 */
function summaryOf(e: AuditEntry, t: TFunction): string {
  if (e.category === 'content') return t('admin.audit_content_withheld')
  if (e.target_email) return e.target_email
  return e.detail ?? actionLabel(t, e.action)
}

function actorText(e: AuditEntry, t: TFunction): string {
  const actor = actorOf(e)
  if (actor.kind === 'email') return t('admin.by_actor', { email: actor.email })
  if (actor.kind === 'ref') return t('admin.audit_actor_ref', { ref: actor.ref })
  return t('admin.audit_actor_anonymous')
}
