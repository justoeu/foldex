import { useState, lazy, Suspense } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from '../components/icons'
import { HubCard, HubShortcut, HubRule } from '../components/HubCard'
import { PasswordStrength } from '../components/PasswordStrength'
import { initialsOf } from '../lib/initials'
import { AccountPage, type AccountTab } from './AccountPage'
import { useFolders, useResetFolderPassword } from '../api/folders'
import {
  useMasterPasswordStatus,
  useSetMasterPassword,
  useRemoveMasterPassword,
} from '../api/settings'
import { useCurrentUser } from '../auth/AuthProvider'
import { hasSecondFactor, isAdminRole } from '../auth/types'
import { apiErrorCode as errCode } from '../lib/apiError'
import type { AppView } from '../AppWorkspace'
import { PasswordInput } from '../components/PasswordInput'

// Lazy: non-admins never download the administration code at all — the
// settings chunk they fetch carries no /api/admin surface or its strings.
const AdminUsersPage = lazy(() =>
  import('./AdminUsersPage').then((module) => ({ default: module.AdminUsersPage })),
)
const AdminOverview = lazy(() =>
  import('../components/admin/AdminOverview').then((m) => ({ default: m.AdminOverview })),
)
const AuditSection = lazy(() =>
  import('../components/admin/AuditSection').then((m) => ({ default: m.AuditSection })),
)
const PolicySection = lazy(() =>
  import('../components/admin/PolicySection').then((m) => ({ default: m.PolicySection })),
)
const BackupSection = lazy(() =>
  import('../components/admin/BackupSection').then((m) => ({ default: m.BackupSection })),
)
const AbuseSection = lazy(() =>
  import('../components/admin/AbuseSection').then((m) => ({ default: m.AbuseSection })),
)
const RolesMatrixSection = lazy(() =>
  import('../components/admin/RolesMatrix').then((m) => ({
    // Wrapped in the card the detail pages use, so the matrix looks the same
    // standing alone as it does inside the administration overview.
    default: () => (
      <div className="fx-card">
        <div className="fx-card-body">
          <m.RolesMatrix />
        </div>
      </div>
    ),
  })),
)

type Props = {
  // Opens the folder edit dialog (to set a fresh password after a reset).
  onEditFolder?: (folderId: number) => void
  // Leaves the hub for the app views it links to (import/export, stats).
  onNavigate?: (view: AppView) => void
  // Deep-links the hub straight into a section (user menu → Profile). Valid
  // values are HubSection names; anything unknown falls back to 'overview'.
  initialSection?: string
}

// A locked folder's master-verified action: `reset` clears the password and
// nudges you to set a new one; `remove` clears it and leaves the folder open.
type FolderPwMode = 'reset' | 'remove'

// The hub is one screen with two scopes (RBAC): everything a signed-in user
// manages about themselves, and — only for admins — the instance-wide
// administration surface. `overview` is the tile grid; every other value is a
// detail page reached from a tile, with a back affordance to the grid.
type HubSection =
  | 'overview' | 'profile' | 'account' | 'security' | 'tokens' | 'master' | 'locked'
  | 'admin' | 'roles' | 'audit' | 'policy' | 'backup' | 'abuse'
type HubScope = 'personal' | 'admin'

const HUB_SECTIONS: readonly HubSection[] = [
  'overview', 'profile', 'account', 'security', 'tokens', 'master', 'locked',
  'admin', 'roles', 'audit', 'policy', 'backup', 'abuse',
]

// Every section that lives under the administration scope. A non-admin who
// deep-links into one is bounced to the overview by resolveHubView, mirroring
// the server's 404 on the whole /api/admin surface.
const ADMIN_SECTIONS: readonly HubSection[] = [
  'admin', 'roles', 'audit', 'policy', 'backup', 'abuse',
]

function isHubSection(value: string | undefined): value is HubSection {
  return value !== undefined && (HUB_SECTIONS as readonly string[]).includes(value)
}

/**
 * Sections that were folded into the single account page.
 *
 * They remain accepted rather than deleted: the topbar user menu deep-links
 * `profile`, and a link someone kept would otherwise land on the overview with
 * no explanation. Resolving them is also what keeps `resolveHubView`'s admin
 * bounce and the back affordance working unchanged.
 */
const MERGED_INTO_ACCOUNT: readonly HubSection[] = ['profile', 'security', 'tokens']

/**
 * Which panel of the account page a merged name lands on.
 *
 * Resolving all three to "the account page" was the first shape and it threw
 * information away: a deep-link that said `security` knew exactly where it
 * wanted to go, and arrived on Profile. The names survive the merge as
 * DESTINATIONS, not just as aliases kept alive for old links.
 */
const SECTION_TAB: Partial<Record<HubSection, AccountTab>> = {
  profile: 'profile',
  security: 'security',
  tokens: 'tokens',
  account: 'profile',
}

/** What the hub can actually RENDER, once the merged names are resolved. */
export type CanonicalSection = Exclude<HubSection, 'profile' | 'security' | 'tokens'>

function canonicalSection(section: HubSection): CanonicalSection {
  return MERGED_INTO_ACCOUNT.includes(section) ? 'account' : (section as CanonicalSection)
}

// Literal keys per section: dynamic `t(\`settings.sec_${section}…\`)` template
// keys are invisible to static key checking, so a typo would ship the raw key
// string to the UI instead of failing anywhere.
// Keyed by the CANONICAL section: profile/security/tokens resolve to 'account'
// before this is read, so they carry no head of their own.
const SECTION_HEAD: Record<CanonicalSection, { kicker: string; title: string }> = {
  overview: { kicker: 'settings.page_kicker', title: 'settings.page_title' },
  account: { kicker: 'settings.sec_account_kicker', title: 'settings.sec_account_title' },
  master: { kicker: 'settings.sec_master_kicker', title: 'settings.sec_master_title' },
  locked: { kicker: 'settings.sec_locked_kicker', title: 'settings.sec_locked_title' },
  admin: { kicker: 'settings.sec_admin_kicker', title: 'settings.sec_admin_title' },
  roles: { kicker: 'settings.sec_roles_kicker', title: 'settings.sec_roles_title' },
  audit: { kicker: 'settings.sec_audit_kicker', title: 'settings.sec_audit_title' },
  policy: { kicker: 'settings.sec_policy_kicker', title: 'settings.sec_policy_title' },
  backup: { kicker: 'settings.sec_backup_kicker', title: 'settings.sec_backup_title' },
  abuse: { kicker: 'settings.sec_abuse_kicker', title: 'settings.sec_abuse_title' },
}

/**
 * Resolves what the hub actually shows for the live session. The role is only
 * refreshed on mount/login/session-change, so a demotion by ANOTHER admin
 * self-heals here on the next session refresh — until then the SERVER holds
 * the line (every /api/admin call re-reads the role and 404s). This resolves
 * the UI to surfaces the session can still see; it is not the guard.
 */
export function resolveHubView(
  isAdmin: boolean,
  scope: HubScope,
  section: CanonicalSection,
): { scope: HubScope; section: CanonicalSection } {
  if (isAdmin) return { scope, section }
  return {
    scope: 'personal',
    section: ADMIN_SECTIONS.includes(section) ? 'overview' : section,
  }
}

export function SettingsPage({ onEditFolder, onNavigate, initialSection }: Props) {
  const { t } = useTranslation()
  // Read from the session rather than a prop, mirroring the server: the whole
  // /api/admin surface answers 404 for a non-admin, so the scope must not even
  // exist for them — hidden, not disabled.
  // isAdminRole, never `=== 'admin'`: the OWNER administers too, and an
  // equality test here hid the whole administration scope from the one role
  // that can reach the owner-only surfaces inside it.
  const me = useCurrentUser()
  const role = me?.role
  const isAdmin = role !== undefined && isAdminRole(role)
  // The OR, not the authenticator alone: the tile and the account hero must
  // not disagree about whether the account has a second factor.
  const twoFactorOn = me !== null && hasSecondFactor(me)
  const [scope, setScope] = useState<HubScope>('personal')
  const [section, setSection] = useState<HubSection>(
    isHubSection(initialSection) ? initialSection : 'overview',
  )
  const effective = resolveHubView(isAdmin, scope, canonicalSection(section))
  // Read from the ORIGINAL name, before it is canonicalized away.
  const accountTab = SECTION_TAB[section] ?? 'profile'
  const effectiveScope = effective.scope
  const effectiveSection = effective.section

  if (effectiveSection !== 'overview') {
    // The administration sections carry tables and matrices and get the full
    // container; everything else is a form, and stays in a readable column.
    // The account page carries a rail beside its panel and wants the room;
    // everything else here is a form and stays in a readable column.
    const wide = ADMIN_SECTIONS.includes(effectiveSection) || effectiveSection === 'account'
    return (
      <div className={'fx-hub-page' + (wide ? '' : ' fx-hub-page-narrow')}>
        <button className="fx-hub-back" onClick={() => setSection('overview')}>
          <Icon d={I.chevronLeft} size={13} /> {t('settings.hub_back')}
        </button>
        <div className="fx-pagehead" style={{ margin: '14px 0 18px' }}>
          <div>
            <div className="fx-pagehead-kicker">{t(SECTION_HEAD[effectiveSection].kicker)}</div>
            <h1 className="fx-pagehead-h">{t(SECTION_HEAD[effectiveSection].title)}</h1>
          </div>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          {effectiveSection === 'account' && <AccountPage initialTab={accountTab} />}
          {effectiveSection === 'master' && <MasterPasswordSection />}
          {effectiveSection === 'locked' && <LockedFoldersSection onEditFolder={onEditFolder} />}
          {isAdmin && ADMIN_SECTIONS.includes(effectiveSection) && (
            <Suspense fallback={<div className="fx-empty">...</div>}>
              {effectiveSection === 'admin' && <AdminUsersPage />}
              {effectiveSection === 'roles' && <RolesMatrixSection />}
              {effectiveSection === 'audit' && <AuditSection />}
              {effectiveSection === 'policy' && <PolicySection />}
              {effectiveSection === 'backup' && <BackupSection />}
              {effectiveSection === 'abuse' && <AbuseSection />}
            </Suspense>
          )}
        </div>
      </div>
    )
  }

  return (
    <div className="fx-hub-page">
      <div className="fx-hub-head" style={{ marginBottom: 22 }}>
        <div>
          <div className="fx-pagehead-kicker">{t('settings.page_kicker')}</div>
          <h1 className="fx-pagehead-h">{t('settings.page_title')}</h1>
          <p className="fx-hub-head-lede">{t('settings.page_lede')}</p>
        </div>
        <div className="fx-hub-head-actions">
          <button className="fx-pillbtn" onClick={() => onNavigate?.('import')}>
            <Icon d={I.upload} size={13} /> {t('settings.head_backup')}
          </button>
          {isAdmin && (
            <button
              className="fx-cta fx-cta-fill"
              onClick={() => {
                setScope('admin')
                setSection('admin')
              }}
            >
              <Icon d={I.plus} size={13} /> {t('settings.head_invite')}
            </button>
          )}
        </div>
      </div>

      {isAdmin && (
        <div
          className="fx-hub-seg"
          role="group"
          aria-label={t('settings.hub_scope_aria')}
          style={{ marginBottom: 16 }}
        >
          <button
            className={'fx-hub-seg-btn' + (effectiveScope === 'personal' ? ' fx-hub-seg-btn-active' : '')}
            aria-pressed={effectiveScope === 'personal'}
            onClick={() => setScope('personal')}
          >
            <Icon d={I.user} size={13} /> {t('settings.hub_scope_personal')}
          </button>
          <button
            className={'fx-hub-seg-btn' + (effectiveScope === 'admin' ? ' fx-hub-seg-btn-active' : '')}
            aria-pressed={effectiveScope === 'admin'}
            onClick={() => setScope('admin')}
          >
            <Icon d={I.shield} size={13} /> {t('settings.hub_scope_admin')}
          </button>
        </div>
      )}

      {effectiveScope === 'personal' ? (
        <div className="fx-hub-stack">
          <IdentityHero onOpenSecurity={() => setSection('security')} />

          <section>
            <HubRule label={t('settings.hub_group_account')} />
            <div className="fx-acards">
              {/* One tile, not four. Profile, sign-in methods, two-factor and
                  API tokens are all "my account" and each rendered a card or two
                  of content; the sign-in one commonly rendered a single line of
                  status with no action at all. */}
              <HubCard
                icon={I.user} tone="fx-tone-accent"
                title={t('settings.tile_account_title')} desc={t('settings.tile_account_desc')}
                action={t('settings.tile_account_action')}
                status={twoFactorOn ? t('settings.chip_2fa_on') : t('settings.chip_2fa_off')}
                statusTone={twoFactorOn ? 'fx-chip-ok' : 'fx-chip-warn'}
                onClick={() => setSection('account')}
              />
              <HubCard
                icon={I.lock} tone="fx-tone-amber"
                title={t('settings.tile_master_title')} desc={t('settings.tile_master_desc')}
                action={t('settings.tile_master_action')} onClick={() => setSection('master')}
              />
              <HubCard
                icon={I.folder} tone="fx-tone-accent"
                title={t('settings.tile_locked_title')} desc={t('settings.tile_locked_desc')}
                action={t('settings.tile_locked_action')} onClick={() => setSection('locked')}
              />
            </div>
          </section>

          <section>
            <HubRule label={t('settings.hub_group_shortcuts')} />
            <div className="fx-scuts">
              <HubShortcut
                icon={I.upload} tone="fx-tone-accent"
                title={t('settings.tile_import_title')} desc={t('settings.tile_import_desc')}
                onClick={() => onNavigate?.('import')}
              />
              <HubShortcut
                icon={I.chart} tone="fx-tone-pink"
                title={t('settings.tile_stats_title')} desc={t('settings.tile_stats_desc')}
                onClick={() => onNavigate?.('stats')}
              />
            </div>
          </section>
        </div>
      ) : (
        <Suspense fallback={<div className="fx-empty">...</div>}>
          <AdminOverview
            onOpen={(s) => setSection(s === 'users' ? 'admin' : s)}
          />
        </Suspense>
      )}
    </div>
  )
}

/**
 * Who you are signed in as, beside the one thing the instance most wants you
 * to fix. The nudge is rendered only while a second factor is missing: a
 * permanent panel that always says "you are fine" trains the eye to skip the
 * slot, so the slot has to stay empty when there is nothing to say.
 */
function IdentityHero({ onOpenSecurity }: { onOpenSecurity: () => void }) {
  const { t } = useTranslation()
  const me = useCurrentUser()
  if (!me) return null

  const verified = me.email_verified_at != null
  const needs2fa = !me.totp_enabled

  return (
    <div className={needs2fa ? 'fx-hub-hero' : undefined}>
      <div className={'fx-idcard' + (needs2fa ? '' : ' fx-idcard-solo')}>
        <span className="fx-avatar fx-avatar-lg">{initialsOf(me.name ?? '', me.email)}</span>
        <div style={{ minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 9, flexWrap: 'wrap' }}>
            <span className="fx-idcard-name">{me.name?.trim() || me.email}</span>
            <span className="fx-chip">{t(`admin.role_${me.role}`, me.role)}</span>
          </div>
          <div className="fx-idcard-mail">{me.email}</div>
          <div className="fx-idcard-chips">
            <span className={'fx-chip ' + (verified ? 'fx-chip-ok' : 'fx-chip-warn')}>
              {verified ? t('settings.chip_mail_verified') : t('settings.chip_mail_unverified')}
            </span>
            <span className={'fx-chip ' + (needs2fa ? 'fx-chip-warn' : 'fx-chip-ok')}>
              {needs2fa ? t('settings.chip_2fa_off') : t('settings.chip_2fa_on')}
            </span>
          </div>
        </div>
      </div>

      {needs2fa && (
        <div className="fx-promo">
          <div>
            <div className="fx-promo-kicker">{t('settings.promo_kicker')}</div>
            <div className="fx-promo-title">{t('settings.promo_title')}</div>
            <div className="fx-promo-desc">{t('settings.promo_desc')}</div>
          </div>
          <button className="fx-promo-btn" onClick={onOpenSecurity}>{t('settings.promo_action')}</button>
        </div>
      )}
    </div>
  )
}


function MasterPasswordSection() {
  const { t } = useTranslation()
  const status = useMasterPasswordStatus()
  const setMaster = useSetMasterPassword()
  const removeMaster = useRemoveMasterPassword()

  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [hint, setHint] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [ok, setOk] = useState<string | null>(null)
  const configured = status.data?.configured === true
  const currentHint = status.data?.hint ?? null

  const save = async () => {
    setError(null)
    setOk(null)
    if (next.length < 8) {
      setError(t('settings.master_too_short'))
      return
    }
    if (next !== confirm) {
      setError(t('settings.master_mismatch'))
      return
    }
    const trimmedHint = hint.trim()
    if (trimmedHint && trimmedHint.toLowerCase() === next.toLowerCase()) {
      setError(t('settings.master_hint_equals'))
      return
    }
    try {
      await setMaster.mutateAsync({
        password: next,
        currentPassword: configured ? current : undefined,
        // Omit when empty → the backend keeps the existing hint (a password
        // change doesn't silently wipe it). A non-empty value replaces it.
        hint: trimmedHint || undefined,
      })
      setCurrent('')
      setNext('')
      setConfirm('')
      setHint('')
      setOk(configured ? t('settings.master_changed') : t('settings.master_set'))
    } catch (e) {
      setError(errCode(e) === 'wrong_password' ? t('settings.master_wrong_current') : t('settings.save_error'))
    }
  }

  const remove = async () => {
    setError(null)
    setOk(null)
    try {
      await removeMaster.mutateAsync({ currentPassword: current })
      setCurrent('')
      setNext('')
      setConfirm('')
      setHint('')
      setOk(t('settings.master_removed'))
    } catch (e) {
      setError(errCode(e) === 'wrong_password' ? t('settings.master_wrong_current') : t('settings.save_error'))
    }
  }

  const busy = setMaster.isPending || removeMaster.isPending

  return (
    <section className="fx-card">
      <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
        <h3 className="fx-card-title" style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon d={I.lock} size={15} /> {t('settings.master_title')}
        </h3>
        <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>{t('settings.master_desc')}</p>

        <div
          style={{ fontSize: 12, display: 'flex', alignItems: 'center', gap: 6, color: configured ? 'var(--fx-ink-2)' : 'var(--fx-ink-4)' }}
        >
          <Icon d={configured ? I.check : I.info} size={13} />{' '}
          {configured ? t('settings.master_status_on') : t('settings.master_status_off')}
        </div>

        {configured && currentHint && (
          <div style={{ fontSize: 12, color: 'var(--fx-ink-3)', display: 'flex', alignItems: 'center', gap: 6 }}>
            <Icon d={I.info} size={12} /> {t('settings.master_current_hint', { hint: currentHint })}
          </div>
        )}

        {configured && (
          <label className="fx-field">
            <span className="fx-field-label">{t('settings.master_current_label')}</span>
            <div className="fx-input">
              <PasswordInput
                autoComplete="off"
                value={current}
                onChange={(e) => {
                  setCurrent(e.target.value)
                  setError(null)
                }}
                placeholder={t('settings.master_current_placeholder')}
                aria-label={t('settings.master_current_label')}
              />
            </div>
          </label>
        )}

        <label className="fx-field">
          <span className="fx-field-label">
            {configured ? t('settings.master_new_label') : t('settings.master_new_label_first')}
          </span>
          <div className="fx-input">
            <PasswordInput
              autoComplete="new-password"
              value={next}
              onChange={(e) => {
                setNext(e.target.value)
                setError(null)
              }}
              placeholder={t('settings.master_new_placeholder')}
              aria-label={configured ? t('settings.master_new_label') : t('settings.master_new_label_first')}
            />
          </div>
          <span className="fx-field-hint">{t('settings.master_min_hint')}</span>
          <PasswordStrength value={next} />
        </label>

        <label className="fx-field">
          <span className="fx-field-label">{t('settings.master_confirm_label')}</span>
          <div className="fx-input">
            <PasswordInput
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => {
                setConfirm(e.target.value)
                setError(null)
              }}
              placeholder={t('settings.master_confirm_placeholder')}
              aria-label={t('settings.master_confirm_label')}
            />
          </div>
          {confirm.length > 0 && next !== confirm && (
            <span className="fx-field-hint" style={{ color: 'var(--fx-danger)' }}>
              {t('settings.master_mismatch')}
            </span>
          )}
        </label>

        <label className="fx-field">
          <span className="fx-field-label">{t('settings.master_hint_label')}</span>
          <div className="fx-input">
            <input
              type="text"
              maxLength={200}
              value={hint}
              onChange={(e) => {
                setHint(e.target.value)
                setError(null)
              }}
              placeholder={configured ? t('settings.master_hint_placeholder_keep') : t('settings.master_hint_placeholder')}
              aria-label={t('settings.master_hint_label')}
            />
          </div>
          <span className="fx-field-hint">{t('settings.master_hint_help')}</span>
        </label>

        {error && (
          <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4 }}>
            <Icon d={I.alert} size={12} /> {error}
          </div>
        )}
        {ok && (
          <div style={{ fontSize: 11, color: 'var(--fx-success)', display: 'flex', alignItems: 'center', gap: 4 }}>
            <Icon d={I.check} size={12} /> {ok}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8 }}>
          <button className="fx-confirm-btn fx-confirm-btn-primary" onClick={save} disabled={busy || !next || next !== confirm}>
            <Icon d={I.check} size={13} stroke={2.2} />{' '}
            {configured ? t('settings.master_change_action') : t('settings.master_set_action')}
          </button>
          {configured && (
            <button className="fx-confirm-btn fx-confirm-btn-warn" onClick={remove} disabled={busy || !current}>
              <Icon d={I.trash} size={13} stroke={2} /> {t('settings.master_remove_action')}
            </button>
          )}
        </div>
      </div>
    </section>
  )
}

function LockedFoldersSection({ onEditFolder }: Props) {
  const { t } = useTranslation()
  const { data: folders = [] } = useFolders({ scope: null })
  // Once reset, a folder drops out of the `locked` list on refetch — but we
  // want its success row (and "set new password" affordance) to persist. Track
  // reset folders separately and render them as done rows even after they leave
  // the locked set.
  const [resetDone, setResetDone] = useState<{ id: number; name: string; color: string; mode: FolderPwMode }[]>([])
  const doneIds = new Set(resetDone.map((f) => f.id))
  const locked = folders.filter((f) => f.has_password && !doneIds.has(f.id))
  const isEmpty = locked.length === 0 && resetDone.length === 0

  return (
    <section className="fx-card">
      <div className="fx-card-body" style={{ gap: 12, padding: 18 }}>
        <h3 className="fx-card-title" style={{ fontSize: 16, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon d={I.lock} size={15} /> {t('settings.locked_title')}
        </h3>
        <p style={{ fontSize: 12, color: 'var(--fx-ink-3)', margin: 0 }}>{t('settings.locked_desc')}</p>

        {isEmpty ? (
          <div style={{ fontSize: 12, color: 'var(--fx-ink-4)' }}>{t('settings.locked_empty')}</div>
        ) : (
          <ul style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 8 }}>
            {locked.map((f) => (
              <LockedFolderRow
                key={f.id}
                id={f.id}
                name={f.name}
                color={f.color}
                onDone={(mode) => setResetDone((prev) => [...prev, { id: f.id, name: f.name, color: f.color, mode }])}
              />
            ))}
            {resetDone.map((f) => (
              <DoneFolderRow key={f.id} id={f.id} name={f.name} color={f.color} mode={f.mode} onEditFolder={onEditFolder} />
            ))}
          </ul>
        )}
      </div>
    </section>
  )
}

function DoneFolderRow({
  id,
  name,
  color,
  mode,
  onEditFolder,
}: {
  id: number
  name: string
  color: string
  mode: FolderPwMode
  onEditFolder?: (folderId: number) => void
}) {
  const { t } = useTranslation()
  return (
    <li
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 8,
        border: '1px solid var(--fx-border)',
        borderRadius: 10,
        padding: 12,
      }}
    >
      <span style={{ width: 12, height: 12, borderRadius: 4, background: color, flex: '0 0 auto' }} />
      <span style={{ fontSize: 13, fontWeight: 600, flex: 1 }}>{name}</span>
      <span style={{ fontSize: 12, color: 'var(--fx-success)', display: 'flex', alignItems: 'center', gap: 4 }}>
        <Icon d={I.check} size={13} /> {mode === 'remove' ? t('settings.remove_done') : t('settings.reset_done')}
      </span>
      {/* Only the recovery ("redefinir") flow nudges you to set a new password;
          "remover" intentionally leaves the folder unprotected. */}
      {mode === 'reset' && onEditFolder && (
        <button className="fx-pillbtn" onClick={() => onEditFolder(id)}>
          {t('settings.reset_set_new')}
        </button>
      )}
    </li>
  )
}

function LockedFolderRow({
  id,
  name,
  color,
  onDone,
}: {
  id: number
  name: string
  color: string
  onDone: (mode: FolderPwMode) => void
}) {
  const { t } = useTranslation()
  const reset = useResetFolderPassword()
  const { data: masterStatus } = useMasterPasswordStatus()
  // null = collapsed; otherwise which action the master-password prompt serves.
  const [mode, setMode] = useState<FolderPwMode | null>(null)
  const [master, setMaster] = useState('')
  const [error, setError] = useState<string | null>(null)

  const openFor = (m: FolderPwMode) => {
    setMode((cur) => (cur === m ? null : m))
    setMaster('')
    setError(null)
  }

  const submit = async () => {
    if (!mode) return
    setError(null)
    try {
      // Both actions clear the folder password via the master-verified reset
      // endpoint; they differ only in the follow-up UX (remove leaves the
      // folder unprotected; reset offers to set a new password).
      await reset.mutateAsync({ id, masterPassword: master })
      setMaster('')
      const m = mode
      setMode(null)
      onDone(m)
    } catch (e) {
      const code = errCode(e)
      if (code === 'master_not_configured') setError(t('settings.reset_no_master'))
      else if (code === 'wrong_master_password') setError(t('settings.reset_wrong_master'))
      else setError(t('settings.save_error'))
    }
  }

  return (
    <li
      style={{
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
        border: '1px solid var(--fx-border)',
        borderRadius: 10,
        padding: 12,
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <span style={{ width: 12, height: 12, borderRadius: 4, background: color, flex: '0 0 auto' }} />
        <span style={{ fontSize: 13, fontWeight: 600, flex: 1 }}>{name}</span>
        <button className="fx-pillbtn" onClick={() => openFor('reset')}>
          {t('settings.reset_action')}
        </button>
        <button className="fx-pillbtn fx-pillbtn-danger" onClick={() => openFor('remove')}>
          {t('settings.remove_action')}
        </button>
      </div>

      {mode && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <div style={{ fontSize: 12, color: 'var(--fx-ink-3)' }}>
            {mode === 'remove' ? t('settings.remove_explain') : t('settings.reset_explain')}
          </div>
          {masterStatus?.hint && (
            <div className="fx-field-hint" style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              <Icon d={I.info} size={12} /> {t('settings.reset_master_hint', { hint: masterStatus.hint })}
            </div>
          )}
          <div className="fx-input">
            <PasswordInput
              autoFocus
              autoComplete="off"
              value={master}
              onChange={(e) => {
                setMaster(e.target.value)
                setError(null)
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  void submit()
                }
              }}
              placeholder={t('settings.reset_master_placeholder')}
              aria-label={t('settings.reset_master_placeholder')}
            />
          </div>
          {error && (
            <div style={{ fontSize: 11, color: 'var(--fx-danger)', display: 'flex', alignItems: 'center', gap: 4 }}>
              <Icon d={I.alert} size={12} /> {error}
            </div>
          )}
          <div style={{ display: 'flex', gap: 8 }}>
            <button className="fx-confirm-btn" onClick={() => setMode(null)}>
              {t('common.cancel')}
            </button>
            <button
              className="fx-confirm-btn fx-confirm-btn-danger"
              onClick={submit}
              disabled={!master || reset.isPending}
            >
              <Icon d={mode === 'remove' ? I.trash : I.refresh} size={13} stroke={2} />{' '}
              {mode === 'remove' ? t('settings.remove_confirm') : t('settings.reset_confirm')}
            </button>
          </div>
        </div>
      )}
    </li>
  )
}
