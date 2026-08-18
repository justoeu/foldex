import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { fetchAudit, auditQueryKey, type AuditEntry } from '../../api/admin'

/** The actions the filter offers, in the order the screen lists them. */
const FILTERS = [
  '',
  'login.failed',
  'login.succeeded',
  'user.role_changed',
  'user.status_changed',
  'invite.created',
  'policy.changed',
] as const

/**
 * The administrative trail.
 *
 * Paginated by keyset (`before` = the last id shown), not by offset: the trail
 * grows at its head, so an offset-paged second page would repeat rows the first
 * page already showed as soon as anything was written between the two requests.
 */
export function AuditSection() {
  const { t } = useTranslation()
  const [action, setAction] = useState<string>('')
  const [pages, setPages] = useState<number[]>([])

  const before = pages.length > 0 ? pages[pages.length - 1] : undefined
  const query = useQuery({
    queryKey: auditQueryKey(action, before),
    queryFn: () => fetchAudit({ action: action || undefined, before }),
  })

  return (
    <div className="fx-card">
      <div className="fx-card-body" style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
          {FILTERS.map((f) => (
            <button
              key={f || 'all'}
              className={'fx-pillbtn' + (action === f ? ' fx-pillbtn-active' : '')}
              aria-pressed={action === f}
              onClick={() => {
                setAction(f)
                // Changing the filter restarts pagination: a cursor taken from
                // the previous filter's result set points at an id that may not
                // appear in this one at all.
                setPages([])
              }}
            >
              {f === '' ? t('admin.audit_filter_all') : t(`admin.action_${f.replace(/\./g, '_')}`, f)}
            </button>
          ))}
        </div>

        {query.isPending && <div className="fx-empty">{t('common.loading')}</div>}
        {query.isError && <div className="fx-empty">{t('admin.audit_unavailable')}</div>}
        {!query.isPending && (query.data?.length ?? 0) === 0 && (
          <div className="fx-empty">{t('admin.audit_empty')}</div>
        )}

        <div>
          {query.data?.map((e) => <AuditRow entry={e} key={e.id} />)}
        </div>

        <div style={{ display: 'flex', gap: 8 }}>
          {pages.length > 0 && (
            <button className="fx-pillbtn" onClick={() => setPages((p) => p.slice(0, -1))}>
              {t('admin.audit_prev')}
            </button>
          )}
          {(query.data?.length ?? 0) > 0 && (
            <button
              className="fx-pillbtn"
              onClick={() => {
                const last = query.data?.[query.data.length - 1]
                if (last) setPages((p) => [...p, last.id])
              }}
            >
              {t('admin.audit_next')}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

function AuditRow({ entry }: { entry: AuditEntry }) {
  const { t } = useTranslation()
  const who = [entry.target_email, entry.actor_email && t('admin.by_actor', { email: entry.actor_email })]
    .filter(Boolean)
    .join(' · ')
  return (
    <div className="fx-audit-row">
      <span className="fx-audit-when">
        {new Date(entry.created_at).toLocaleString()}
      </span>
      <span className="fx-audit-action">
        {/* The raw action id is the fallback, so an action added server-side
            renders as its identifier instead of a missing-key blank. */}
        {t(`admin.action_${entry.action.replace(/\./g, '_')}`, entry.action)}
      </span>
      <span className="fx-audit-who">{who}</span>
      {entry.detail && <span className="fx-audit-who">· {entry.detail}</span>}
    </div>
  )
}
