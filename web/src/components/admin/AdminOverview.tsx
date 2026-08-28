import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { I } from '../icons'
import { HubCard, HubRule } from '../HubCard'
import { fetchMetrics, fetchAudit, auditQueryKey, type InstanceMetrics } from '../../api/admin'
import { RolesMatrix } from './RolesMatrix'

/** The sections the administration scope can open. */
export type AdminSection = 'users' | 'roles' | 'audit' | 'policy' | 'backup' | 'abuse'

type Props = { onOpen: (section: AdminSection) => void }

/**
 * The administration landing screen: four derived metrics, the action cards,
 * the RBAC matrix and the most recent security events.
 *
 * Everything here is read-only. Each card navigates to the section that owns
 * the mutation, so there is exactly one place that can change any given thing.
 */
export function AdminOverview({ onOpen }: Props) {
  const { t } = useTranslation()
  const metrics = useQuery({ queryKey: ['admin', 'metrics'], queryFn: fetchMetrics })
  // The attention feed is the audit trail's head. Same data, no second
  // endpoint: "what needs attention" is just "what happened recently" filtered
  // by how alarming it is — and the shared key means AuditSection reads it
  // from cache rather than refetching.
  const recent = useQuery({ queryKey: auditQueryKey(), queryFn: () => fetchAudit({}) })

  const attention = Array.isArray(recent.data) ? recent.data : []

  return (
    <div className="fx-hub-stack">
      <MetricRow metrics={metrics.data} loading={metrics.isPending} />

      <section>
        <HubRule label={t('admin.group_access')} />
        <div className="fx-acards">
          <HubCard
            icon={I.users} tone="fx-tone-accent"
            title={t('admin.card_users_title')} desc={t('admin.card_users_desc')}
            action={t('admin.card_users_action')} onClick={() => onOpen('users')}
          />
          <HubCard
            icon={I.layers} tone="fx-tone-pink"
            title={t('admin.card_roles_title')} desc={t('admin.card_roles_desc')}
            action={t('admin.card_roles_action')}
            status={t('admin.card_roles_status', { count: metrics.data?.roles_in_use ?? 0 })}
            onClick={() => onOpen('roles')}
          />
          <HubCard
            icon={I.mail} tone="fx-tone-blue"
            title={t('admin.card_invites_title')} desc={t('admin.card_invites_desc')}
            action={t('admin.card_invites_action')}
            status={
              metrics.data && metrics.data.pending_invites > 0
                ? t('admin.card_invites_status', { count: metrics.data.pending_invites })
                : undefined
            }
            statusTone={metrics.data && metrics.data.pending_invites > 0 ? 'fx-chip-warn' : undefined}
            onClick={() => onOpen('users')}
          />
          <HubCard
            icon={I.sliders} tone="fx-tone-amber"
            title={t('admin.card_policy_title')} desc={t('admin.card_policy_desc')}
            action={t('admin.card_policy_action')} onClick={() => onOpen('policy')}
          />
          <HubCard
            icon={I.clock} tone="fx-tone-green"
            title={t('admin.card_audit_title')} desc={t('admin.card_audit_desc')}
            action={t('admin.card_audit_action')} onClick={() => onOpen('audit')}
          />
          <HubCard
            icon={I.shield} tone="fx-tone-green"
            title={t('admin.card_backup_title')} desc={t('admin.card_backup_desc')}
            action={t('admin.card_backup_action')} onClick={() => onOpen('backup')}
          />
          <HubCard
            icon={I.alert} tone="fx-tone-pink"
            title={t('admin.card_abuse_title')} desc={t('admin.card_abuse_desc')}
            action={t('admin.card_abuse_action')} onClick={() => onOpen('abuse')}
          />
        </div>
      </section>

      <section className="fx-adm-split">
        <div className="fx-panel">
          <div className="fx-panel-head">
            <div>
              <div className="fx-panel-title">{t('admin.group_roles')}</div>
              <div className="fx-panel-desc">
                {t('admin.roles_count', { count: metrics.data?.roles_in_use ?? 0 })}
              </div>
            </div>
            <button className="fx-pillbtn" onClick={() => onOpen('roles')}>{t('admin.roles_manage')}</button>
          </div>
          <RolesMatrix />
        </div>

        <div className="fx-panel fx-panel-col">
          <div className="fx-panel-title">{t('admin.group_attention')}</div>
          <div className="fx-panel-desc">{t('admin.attention_desc')}</div>
          <div className="fx-attn" style={{ marginTop: 16 }}>
            {recent.isPending && <div className="fx-empty">{t('common.loading')}</div>}
            {!recent.isPending && attention.length === 0 && (
              <div className="fx-empty">{t('admin.attention_empty')}</div>
            )}
            {attention.slice(0, 5).map((e) => (
              <div className={'fx-attn-item ' + attentionTone(e.action)} key={e.id}>
                <div style={{ minWidth: 0 }}>
                  <div className="fx-attn-title">{t(`admin.action_${e.action.replace(/\./g, '_')}`, e.action)}</div>
                  <div className="fx-attn-desc">
                    {[e.target_email, e.actor_email && t('admin.by_actor', { email: e.actor_email })]
                      .filter(Boolean)
                      .join(' · ')}
                  </div>
                </div>
                <span className="fx-attn-time">{relativeTime(e.created_at)}</span>
              </div>
            ))}
          </div>
          <button className="fx-pillbtn" style={{ marginTop: 'auto' }} onClick={() => onOpen('audit')}>
            {t('admin.audit_open')}
          </button>
        </div>
      </section>
    </div>
  )
}

/** Failed logins are the one event that reads as an alarm rather than a note. */
function attentionTone(action: string): string {
  if (action === 'login.failed') return 'fx-attn-danger'
  if (action.startsWith('user.') || action.startsWith('instance.')) return 'fx-attn-warn'
  return ''
}

/**
 * Coarse relative time — "3h", "2d".
 *
 * Deliberately not a live-updating clock: this list is a snapshot the
 * administrator scans, and a ticking timestamp would re-render the whole feed
 * every second to change a character nobody is watching.
 */
export function relativeTime(iso: string): string {
  const diffMs = Date.now() - new Date(iso).getTime()
  const minutes = Math.max(0, Math.floor(diffMs / 60000))
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

function MetricRow({ metrics, loading }: { metrics?: InstanceMetrics; loading: boolean }) {
  const { t } = useTranslation()
  // typeof-checked rather than merely truthy for the same reason the roles
  // matrix guards on its array: a response of an unexpected shape arrives as a
  // truthy object and would render "undefined" into every tile.
  if (loading || !metrics || typeof metrics.active_users !== 'number') {
    return <div className="fx-metrics">{[0, 1, 2, 3].map((i) => <div className="fx-metric" key={i} />)}</div>
  }
  return (
    <div className="fx-metrics">
      <Metric
        tone="var(--fx-tone-green)" label={t('admin.metric_active_users')}
        value={String(metrics.active_users)}
        hint={t('admin.metric_active_users_hint', { count: metrics.active_users_added_30d })}
      />
      <Metric
        tone="var(--fx-tone-amber)" label={t('admin.metric_pending_invites')}
        value={String(metrics.pending_invites)}
        hint={
          metrics.next_invite_expiry_hours === null
            ? t('admin.metric_pending_invites_none')
            : t('admin.metric_pending_invites_hint', { count: metrics.next_invite_expiry_hours })
        }
      />
      <Metric
        tone="var(--fx-tone-accent)" label={t('admin.metric_roles')}
        value={String(metrics.roles_in_use)}
        hint={t('admin.metric_roles_hint', { count: metrics.permission_count })}
      />
      <Metric
        tone="var(--fx-tone-pink)" label={t('admin.metric_two_factor')}
        value={`${metrics.two_factor_percent}%`}
        hint={t('admin.metric_two_factor_hint')}
      />
    </div>
  )
}

function Metric({ tone, label, value, hint }: { tone: string; label: string; value: string; hint: string }) {
  return (
    <div className="fx-metric">
      <div className="fx-metric-label">
        <span className="fx-metric-dot" style={{ color: tone }} />
        {label}
      </div>
      <div className="fx-metric-value">{value}</div>
      <div className="fx-metric-hint">{hint}</div>
    </div>
  )
}

