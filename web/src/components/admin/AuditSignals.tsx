import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  blockIP, ipBlocksQueryKey, unblockIP,
  type AuditStats, type IPBlock,
} from '../../api/admin'
import { blockable } from './auditFormat'
import { initialsOf } from '../../lib/initials'
import { useConfirm } from '../ConfirmDialog'
import { apiErrorMessage } from '../../lib/apiError'

/**
 * Installing and removing a permanent block.
 *
 * One hook for every card that offers a block — the two here and the anomalies
 * panel — because the confirmation, the cache invalidation and the error
 * surface are identical and the only thing that differs is the verb.
 *
 * The reason is DERIVED from the signal that prompted the click rather than
 * typed: the operator is looking at "5 failures from this address in 4 minutes"
 * when they press the button, and asking them to restate it in a text field
 * produces either that same sentence or an empty string.
 *
 * The server's refusal is kept verbatim. Each rail answers with its own code
 * and a sentence describing the OPERATOR's situation — "that is the address you
 * are connected from" — and flattening those into "failed" would remove the one
 * thing that makes the refusal actionable.
 */
export function useBlockControls() {
  const confirm = useConfirm()
  const { t } = useTranslation()
  const qc = useQueryClient()
  const [error, setError] = useState<string | null>(null)

  // Every surface that shows whether an address is blocked, by PREFIX so all
  // four anomaly windows go with it. A row that still offers "block" on an
  // address just blocked asks for an action already taken, and the second
  // click answers 409 for no reason the operator can see.
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ['admin', 'audit'] })
    void qc.invalidateQueries({ queryKey: ['admin', 'anomalies'] })
    void qc.invalidateQueries({ queryKey: ipBlocksQueryKey })
  }

  const block = useMutation({
    mutationFn: (v: { ip: string; reason: string }) => blockIP(v.ip, v.reason),
    onSuccess: () => { setError(null); invalidate() },
    onError: (err) => setError(apiErrorMessage(err) ?? t('admin.audit_block_failed')),
  })
  const unblock = useMutation({
    mutationFn: (ip: string) => unblockIP(ip),
    onSuccess: () => { setError(null); invalidate() },
    onError: (err) => setError(apiErrorMessage(err) ?? t('admin.audit_unblock_failed')),
  })

  return {
    error,
    busy: block.isPending || unblock.isPending,
    async askBlock(ip: string, reason: string) {
      const ok = await confirm({
        title: t('admin.audit_block_title', { ip }),
        message: t('admin.audit_block_warning'),
        confirmLabel: t('admin.audit_block_confirm'),
        destructive: true,
      })
      if (ok) block.mutate({ ip, reason })
    },
    async askUnblock(ip: string) {
      const ok = await confirm({
        title: t('admin.audit_unblock_title', { ip }),
        message: t('admin.audit_unblock_warning'),
        confirmLabel: t('admin.audit_unblock_confirm'),
      })
      if (ok) unblock.mutate(ip)
    },
  }
}

/** Who generated the most events. */
export function AuditActors({ stats }: { stats: AuditStats }) {
  const { t } = useTranslation()
  return (
    <section className="fx-aud-card" aria-labelledby="fx-aud-actors-title">
      <header className="fx-aud-card-head">
        <div>
          <h3 id="fx-aud-actors-title">{t('admin.audit_actors_title')}</h3>
          <p>{t('admin.audit_actors_desc')}</p>
        </div>
      </header>
      {stats.actors.length === 0 ? (
        <p className="fx-empty">{t('admin.audit_empty')}</p>
      ) : (
        <ul className="fx-aud-rows">
          {stats.actors.map((a) => (
            <li className="fx-aud-row" key={a.email}>
              <span className="fx-aud-avatar" aria-hidden="true">{initialsOf('', a.email)}</span>
              <div className="fx-aud-row-main">
                <span className="fx-aud-row-title">{a.email}</span>
                <span className="fx-aud-row-sub">{t(`admin.role_${a.role}`, a.role)}</span>
              </div>
              <span className="fx-aud-row-count">{a.count}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

/**
 * Where the traffic came from.
 *
 * Each row says whether a configured proxy VOUCHED for the address. Migration
 * 000033 refused an ip column precisely because one that cannot say where its
 * value came from is authoritative-looking and forgeable at once; showing the
 * difference is what makes storing it honest.
 */
export function AuditOrigins({
  stats, canBlock,
}: { stats: AuditStats; canBlock: boolean }) {
  const { t } = useTranslation()
  const controls = useBlockControls()
  return (
    <section className="fx-aud-card" aria-labelledby="fx-aud-origins-title">
      <header className="fx-aud-card-head">
        <div>
          <h3 id="fx-aud-origins-title">{t('admin.audit_origins_title')}</h3>
          <p>{t('admin.audit_origins_desc')}</p>
        </div>
      </header>
      {stats.origins.length === 0 ? (
        <p className="fx-empty">{t('admin.audit_origins_none')}</p>
      ) : (
        <ul className="fx-aud-rows">
          {stats.origins.map((o) => (
            <li className="fx-aud-row" key={o.ip}>
              <span
                className={`fx-aud-dot fx-aud-dot-${o.blocked ? 'danger' : o.failures > 0 ? 'warn' : 'ok'}`}
                aria-hidden="true"
              />
              <div className="fx-aud-row-main">
                <span className="fx-aud-row-ip">{o.ip}</span>
                <span className="fx-aud-row-sub">
                  {o.user_agent || t('admin.audit_origin_unknown_device')}
                  {' · '}
                  {o.trusted ? t('admin.audit_ip_trusted') : t('admin.audit_ip_direct')}
                </span>
              </div>
              <span className="fx-aud-row-count">{o.count}</span>
              {o.blocked ? (
                <span className="fx-aud-tag fx-aud-tag-blocked">{t('admin.audit_blocked')}</span>
              ) : canBlock && blockable(o.ip) ? (
                <button
                  type="button"
                  className="fx-pillbtn"
                  disabled={controls.busy}
                  onClick={() => void controls.askBlock(o.ip,
                    t('admin.audit_block_reason_origin', { count: o.failures }))}
                >
                  {t('admin.audit_block_action')}
                </button>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      {controls.error && <p className="fx-aud-error" role="alert">{controls.error}</p>}
    </section>
  )
}

/**
 * The burst the screen leads with.
 *
 * It states what the instance ALREADY did — attemptlimit parks the address for
 * fifteen minutes on its own — before offering the permanent block. A card that
 * only raised the alarm would invite a click that changes nothing new.
 */
export function AuditRiskCard({ stats, canBlock }: { stats: AuditStats; canBlock: boolean }) {
  const { t } = useTranslation()
  const controls = useBlockControls()
  const risk = stats.risk

  if (!risk) {
    return (
      <section className="fx-aud-card fx-aud-risk fx-aud-risk-quiet" aria-labelledby="fx-aud-risk-title">
        <span className="fx-aud-risk-kicker">{t('admin.audit_risk_kicker')}</span>
        <h3 id="fx-aud-risk-title">{t('admin.audit_risk_none_title')}</h3>
        <p>{t('admin.audit_risk_none_desc')}</p>
      </section>
    )
  }
  return (
    <section className="fx-aud-card fx-aud-risk" aria-labelledby="fx-aud-risk-title">
      <div className="fx-aud-risk-head">
        <span className="fx-aud-risk-kicker">{t('admin.audit_risk_kicker')}</span>
        <span className="fx-aud-dot fx-aud-dot-warn" aria-hidden="true" />
      </div>
      <h3 id="fx-aud-risk-title">
        {t('admin.audit_risk_title', { count: risk.failures })}
      </h3>
      <p>{t('admin.audit_risk_desc', { ip: risk.ip, targets: risk.targets })}</p>
      <dl className="fx-aud-risk-rows">
        <div><dt>{t('admin.audit_risk_first')}</dt><dd>{new Date(risk.first_at).toLocaleTimeString()}</dd></div>
        <div><dt>{t('admin.audit_risk_last')}</dt><dd>{new Date(risk.last_at).toLocaleTimeString()}</dd></div>
        <div><dt>{t('admin.audit_risk_targets')}</dt><dd>{risk.targets}</dd></div>
      </dl>
      {risk.blocked ? (
        <p className="fx-aud-risk-done">{t('admin.audit_risk_already_blocked')}</p>
      ) : canBlock && blockable(risk.ip) ? (
        <button
          type="button"
          className="fx-btn fx-aud-risk-cta"
          disabled={controls.busy}
          onClick={() => void controls.askBlock(risk.ip,
            t('admin.audit_block_reason_burst', { count: risk.failures }))}
        >
          {t('admin.audit_block_permanently')}
        </button>
      ) : (
        <p className="fx-aud-risk-done">{t('admin.audit_risk_auto_handled')}</p>
      )}
      {controls.error && <p className="fx-aud-error" role="alert">{controls.error}</p>}
    </section>
  )
}

/** The blocklist, with the way back out. */
export function AuditBlocklist({ blocks, canBlock }: { blocks: IPBlock[]; canBlock: boolean }) {
  const { t } = useTranslation()
  const controls = useBlockControls()
  if (blocks.length === 0) return null
  return (
    <section className="fx-aud-card" aria-labelledby="fx-aud-blocks-title">
      <header className="fx-aud-card-head">
        <div>
          <h3 id="fx-aud-blocks-title">{t('admin.audit_blocklist_title')}</h3>
          <p>{t('admin.audit_blocklist_desc')}</p>
        </div>
      </header>
      <ul className="fx-aud-rows">
        {blocks.map((b) => (
          <li className="fx-aud-row" key={b.id}>
            <span className="fx-aud-dot fx-aud-dot-danger" aria-hidden="true" />
            <div className="fx-aud-row-main">
              <span className="fx-aud-row-ip">{b.ip}</span>
              <span className="fx-aud-row-sub">
                {b.reason || t('admin.audit_block_no_reason')}
                {b.created_by ? ` · ${t('admin.by_actor', { email: b.created_by })}` : ''}
              </span>
            </div>
            {canBlock && (
              <button
                type="button"
                className="fx-pillbtn"
                disabled={controls.busy}
                onClick={() => void controls.askUnblock(b.ip)}
              >
                {t('admin.audit_unblock_action')}
              </button>
            )}
          </li>
        ))}
      </ul>
      {controls.error && <p className="fx-aud-error" role="alert">{controls.error}</p>}
    </section>
  )
}
