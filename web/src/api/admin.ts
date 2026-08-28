import { http } from './client'
import type { AvailabilityResponse } from '../hooks/useAvailability'
import type { AuthUser, Permission, Role } from '../auth/types'

/**
 * The /api/admin surface.
 *
 * Two things about it that the UI mirrors but must never be relied on: a
 * non-admin gets 404 (not 403) from every route here, and the guards that keep
 * the instance from reaching zero administrators live on the server, inside a
 * transaction. The client-side disabling below is affordance, not enforcement.
 */

export type Invite = {
  id: number
  email: string
  role: Role
  created_at: string
  expires_at: string
  accepted_at?: string
  /** Present only in the create response — the raw token is unrecoverable after. */
  accept_url?: string
}

export async function listUsers(): Promise<AuthUser[]> {
  const { data } = await http.get<{ users: AuthUser[] }>('/api/admin/users')
  return data.users
}

export async function updateUser(
  id: number,
  patch: { name?: string; role?: Role; status?: 'active' | 'disabled' },
): Promise<AuthUser> {
  const { data } = await http.patch<AuthUser>(`/api/admin/users/${id}`, patch)
  return data
}

/**
 * Creates an account with a password the ADMINISTRATOR chose.
 *
 * A deliberate exception to the rule the rest of the auth surface enforces —
 * CLAUDE.md §4, "an administrator never chooses, installs or receives another
 * user's credential" — taken by the instance owner with the trade stated. The
 * invitation flow and `sendPasswordRecovery` both avoid the window where two
 * people know one password, and remain the recommended path.
 */
export async function createUser(input: {
  email: string
  name: string
  password: string
  role: Role
}): Promise<AuthUser> {
  const { data } = await http.post('/api/admin/users', input)
  return data as AuthUser
}

export async function deleteUser(id: number): Promise<void> {
  await http.delete(`/api/admin/users/${id}`)
}

export async function revokeUserSessions(id: number): Promise<void> {
  await http.post(`/api/admin/users/${id}/sessions/revoke`)
}

/** Sends a recovery link to the target's verified mailbox. No secret returns. */
export async function sendPasswordRecovery(id: number): Promise<void> {
  await http.post(`/api/admin/users/${id}/force-password-reset`)
}

export async function listInvites(): Promise<Invite[]> {
  const { data } = await http.get<{ invites: Invite[] }>('/api/admin/invites')
  return data.invites
}

export async function createInvite(email: string, role: Role): Promise<Invite> {
  const { data } = await http.post<Invite>('/api/admin/invites', { email, role })
  return data
}

export async function revokeInvite(id: number): Promise<void> {
  await http.delete(`/api/admin/invites/${id}`)
}

/** The administration header's four tiles. Every field is derived server-side. */
export type InstanceMetrics = {
  active_users: number
  active_users_added_30d: number
  pending_invites: number
  /** Hours until the soonest invite expires, or null when none is pending. */
  next_invite_expiry_hours: number | null
  roles_in_use: number
  permission_count: number
  two_factor_percent: number
}

export type RoleSummary = {
  role: Role
  permissions: Permission[]
  user_count: number
  /**
   * Whether this role's grants may be configured at all. Read from the server
   * rather than re-derived as `role !== 'owner'`, for §5's usual reason: two
   * copies of one policy drift, and the direction nobody notices is a screen
   * offering a save the server refuses.
   */
  editable: boolean
}

/** What GET /api/admin/roles answers. */
export type RolesResponse = {
  roles: RoleSummary[]
  /** The full ordered vocabulary — the matrix's rows. */
  permissions: Permission[]
  /** Entries no configuration may add or remove, in either direction. */
  locked: Permission[]
  /** The caller's own role, which bounds what they may grant. */
  caller_role: Role
  /** Whether the caller may write the matrix at all. */
  can_edit: boolean
  /** True on an instance serving the compiled matrix with no store behind it. */
  editable_disabled: boolean
}

export type AuditCategory = 'identity' | 'content'
export type AuditSeverity = 'info' | 'warning' | 'critical'
/** The periods the server offers. An arbitrary range is not one of them. */
export type AuditWindow = '24h' | '7d' | '30d'

/**
 * One row of the trail.
 *
 * `actor_email` is null on a CONTENT row and `actor_ref` carries an opaque
 * account id instead — the server withholds the name in SQL (ADR-46), so this
 * is a shape the client renders, never a rule it applies. `subject` arrives
 * only from the caller's OWN activity feed; the administrative projection does
 * not select the column at all.
 */
export type AuditEntry = {
  id: number
  action: string
  category: AuditCategory
  severity: AuditSeverity
  actor_email: string | null
  actor_ref: number | null
  target_email: string | null
  detail: string | null
  ip: string | null
  ip_trusted: boolean
  user_agent: string | null
  entity_kind?: string | null
  entity_id?: number | null
  subject?: string | null
  created_at: string
}

export type AuditTotals = {
  events: number
  events_prev: number
  failures: number
  failures_prev: number
  access_changes: number
  access_changes_prev: number
  actors: number
  active_users: number
}

export type AuditDayBucket = {
  day: string
  logins: number
  failed: number
  admin: number
  content: number
}

export type AuditActionStat = { action: string; category: AuditCategory; count: number }
export type AuditActorStat = { email: string; role: Role; count: number }
export type AuditOriginStat = {
  ip: string
  trusted: boolean
  user_agent: string | null
  count: number
  failures: number
  last_seen: string
  blocked: boolean
}
export type AuditRisk = {
  ip: string
  failures: number
  targets: number
  first_at: string
  last_at: string
  blocked: boolean
}

export type AuditStats = {
  totals: AuditTotals
  days: AuditDayBucket[]
  distribution: AuditActionStat[]
  actors: AuditActorStat[]
  origins: AuditOriginStat[]
  risk: AuditRisk | null
}

export type IPBlock = {
  id: number
  ip: string
  reason: string | null
  created_by: string | null
  created_at: string
}

/** The filter the trail and its header are both read under. */
export type AuditQuery = {
  window?: AuditWindow
  action?: string
  category?: AuditCategory
  q?: string
  before?: number
  /** Oldest-first. A different QUERY on the server, not a reversed page. */
  order?: 'asc'
}

/** Which factors satisfy AUTH_REQUIRE_2FA_FOR_ADMINS (ADR-37). */
export type AdminFactorMode = 'any' | 'totp_only'

export type InstancePolicy = {
  admin_second_factor: AdminFactorMode
  password_min_length: number
  otp_ttl_minutes: number
  otp_cooldown_seconds: number
  google_allowed_domains: string[]
  google_auto_provision: boolean
  google_default_role: Role
}

export async function fetchMetrics(): Promise<InstanceMetrics> {
  const { data } = await http.get<InstanceMetrics>('/api/admin/metrics')
  return data
}

/**
 * The RBAC matrix as the SERVER enforces it, plus how many accounts hold each
 * role. `permissions` is the full ordered vocabulary — rendering the columns
 * from it rather than from a local list is what stops the screen from
 * describing a grid the server does not implement.
 */
export async function fetchRoles(): Promise<RolesResponse> {
  const { data } = await http.get<RolesResponse>('/api/admin/roles')
  return data
}

/**
 * Replaces one role's configurable grants (ADR-42).
 *
 * The full set is sent, not a delta: absent means revoked, which is the only
 * encoding where two administrators editing at once cannot silently merge
 * their intents into a role neither of them chose.
 */
export async function setRolePermissions(
  role: Role,
  permissions: Permission[],
): Promise<{ roles: RoleSummary[] }> {
  const { data } = await http.put<{ roles: RoleSummary[] }>(
    `/api/admin/roles/${encodeURIComponent(role)}/permissions`,
    { permissions },
  )
  return data
}

/** Keyset pagination: `before` is the last id already shown, not an offset. */
/**
 * Query key for one page of the audit trail. Shared so the administration
 * overview's attention feed and the audit section's unfiltered first page
 * resolve to the SAME cache entry — the feed ends in a button that opens
 * exactly that view, and two spellings of the same page would refetch it one
 * click later. `before` is the keyset cursor; 0 stands for "the head".
 */
export function auditQueryKey(q: AuditQuery = {}) {
  return [
    'admin', 'audit',
    q.window ?? '', q.action ?? '', q.category ?? '', q.q ?? '', q.before ?? 0, q.order ?? '',
  ] as const
}

export async function fetchAudit(q: AuditQuery = {}): Promise<AuditEntry[]> {
  const { data } = await http.get<{ entries: AuditEntry[] }>('/api/admin/audit', { params: auditParams(q) })
  return data.entries
}

/**
 * Drops empty values instead of sending them.
 *
 * `?action=` is not "no action filter" to the server — parseAuditFilter reads
 * the empty string as absent, which happens to agree here, but `?before=0`
 * would be a cursor of zero and `?q=` a search for nothing. Sending only what
 * is set keeps the URL the same one the query key describes.
 */
function auditParams(q: AuditQuery): Record<string, string | number> {
  const params: Record<string, string | number> = {}
  if (q.window) params.window = q.window
  if (q.action) params.action = q.action
  if (q.category) params.category = q.category
  if (q.q) params.q = q.q
  if (q.before) params.before = q.before
  if (q.order) params.order = q.order
  return params
}

export function auditStatsQueryKey(window: AuditWindow) {
  return ['admin', 'audit', 'stats', window] as const
}

export async function fetchAuditStats(window: AuditWindow): Promise<AuditStats> {
  const { data } = await http.get<AuditStats>('/api/admin/audit/stats', { params: { window } })
  return data
}

/**
 * The CSV the browser saves.
 *
 * Fetched through the same axios client as everything else rather than a plain
 * anchor: the session lives in a cookie the SPA sends with credentials, and the
 * CSRF header is injected by the client — a bare <a href> would carry neither
 * on a cross-origin dev setup, and the download would answer 401 with no
 * visible reason.
 */
export async function exportAuditCsv(q: AuditQuery = {}): Promise<Blob> {
  const { data } = await http.get('/api/admin/audit/export.csv', {
    params: auditParams(q),
    responseType: 'blob',
  })
  return data as Blob
}

export const ipBlocksQueryKey = ['admin', 'audit', 'blocks'] as const

export async function fetchIPBlocks(): Promise<IPBlock[]> {
  const { data } = await http.get<{ blocks: IPBlock[] }>('/api/admin/audit/blocks')
  return data.blocks
}

/** Owner-only: `instance.ip_block` is locked to the owner (ADR-46). */
export async function blockIP(ip: string, reason: string): Promise<IPBlock> {
  const { data } = await http.post<IPBlock>('/api/admin/audit/blocks', { ip, reason })
  return data
}

export async function unblockIP(ip: string): Promise<void> {
  await http.delete(`/api/admin/audit/blocks/${encodeURIComponent(ip)}`)
}

/**
 * The caller's OWN activity. Not an admin route: it needs no administrative
 * permission and is the only projection that returns the content label.
 */
export const activityQueryKey = (before = 0) => ['activity', before] as const

export async function fetchOwnActivity(before?: number): Promise<AuditEntry[]> {
  const params = before ? { before } : {}
  const { data } = await http.get<{ entries: AuditEntry[] }>('/api/activity', { params })
  return data.entries
}

export async function fetchPolicy(): Promise<InstancePolicy> {
  const { data } = await http.get<InstancePolicy>('/api/admin/policy')
  return data
}

/** Owner-only. An admin gets 403 `forbidden_role`. */
export async function savePolicy(p: InstancePolicy): Promise<InstancePolicy> {
  const { data } = await http.put<InstancePolicy>('/api/admin/policy', p)
  return data
}

/**
 * Hands the instance to another active account and demotes the caller to admin.
 *
 * The outgoing owner is always the CALLER — the server takes no `from`. Both
 * accounts lose every session, so the caller is signed out by this call.
 */
export async function transferOwnership(id: number): Promise<AuthUser> {
  const { data } = await http.post<AuthUser>(`/api/admin/users/${id}/transfer-ownership`)
  return data
}

/** Asks whether an address is free, for the administrator creating an account.
 *
 *  This lives under /api/admin and must never be mirrored anywhere an ordinary
 *  session can reach: past the admin gate the caller can already list every
 *  account with its address, which is the whole reason the probe is allowed to
 *  answer about e-mail at all. `reason: "pending"` rides an AVAILABLE answer —
 *  somebody is moving to this address but has not confirmed, and the create
 *  would still succeed. */
// ── Operational backup status (ADR-43) ─────────────────────────────────

export type BackupJob = 'dump' | 'drill' | 'mirror' | 'user_zip'

export type BackupRunStatus = 'requested' | 'running' | 'succeeded' | 'failed'

/**
 * One backup_run row. `last_error` is a normalized reason token
 * (`upload_failed`, `drill_counts_mismatch`, …), never raw tool output — the
 * UI renders it as code and must not translate it: it is the exact string an
 * operator greps the agent's logs and the runbook for.
 */
export type BackupRun = {
  id: number
  job: BackupJob
  status: BackupRunStatus
  scheduled_for: string
  started_at: string
  finished_at: string | null
  artifact_key: string | null
  artifact_bytes: number | null
  artifact_sha256: string | null
  objects_scanned: number | null
  objects_copied: number | null
  bytes_copied: number | null
  drill_of_run_id: number | null
  last_error: string | null
  meta: Record<string, unknown>
}

export type BackupJobStatus = {
  job: BackupJob
  /** The drill's entry carries drill_of_run_id and the restored counts in meta
   *  — that row IS the "last drill" highlight. */
  last_success: BackupRun | null
  /** Excludes operational outcomes (shutdown, lock_busy, …) — the same number
   *  the agent's alert threshold compares against. */
  consecutive_failures: number
}

export type BackupStatusResponse = {
  jobs: BackupJobStatus[]
  runs: BackupRun[]
}

/** Query key for one history page. `before` is the keyset cursor; 0 = head. */
export function backupStatusQueryKey(before = 0) {
  return ['admin', 'backup', before] as const
}

export async function fetchBackupStatus(
  params: { before?: number } = {},
): Promise<BackupStatusResponse> {
  const { data } = await http.get<BackupStatusResponse>('/api/admin/backup/runs', { params })
  return data
}

/**
 * Enqueues a manual run. Only ever a 'requested' row the agent claims on its
 * next poll — the web process never executes a backup and never holds the S3
 * credentials. 409 `backup_run_pending` while one is already queued/running.
 */
export async function requestBackupRun(
  job: BackupJob,
): Promise<{ id: number; job: BackupJob; status: 'requested' }> {
  const { data } = await http.post<{ id: number; job: BackupJob; status: 'requested' }>(
    '/api/admin/backup/run',
    { job },
  )
  return data
}

// ── Configurable backup schedule (ADR-44) ──────────────────────────────

/**
 * ONE jsonb shape for all four jobs — the client mirror of
 * backupagent.JobConfig. Every job speaks the same vocabulary; what differs
 * between them is only the floor the server enforces (see `bounds`).
 */
export type BackupScheduleConfig = {
  /** "times" | "interval". Explicit, never inferred from which field is set. */
  mode?: 'times' | 'interval'
  /** mode "times": wall times "HH:MM", 1..6 of them, no repeats. */
  times?: string[]
  /** mode "times": a non-empty subset of sun..sat, no repeats. */
  weekdays?: string[]
  /** mode "interval": 15..1440 minutes. */
  interval_min?: number
  /** Only user_zip may carry `false` — the other three are the instance's floor. */
  enabled?: boolean
}

/** A stored row — the EDITABLE layer, not necessarily what runs (see agent). */
export type BackupScheduleRow = {
  job: BackupJob
  config: BackupScheduleConfig
  updated_at: string
  updated_by_email: string | null
}

/**
 * What the agent's heartbeat says about one job. `schedule` is the agenda the
 * process is actually following, rendered server-side — the truth the band
 * displays; the rows above are only the editable layer feeding it.
 */
export type BackupAgentJobReport = {
  capable: boolean
  /** Only when not capable: no_identity | mirror_off | no_source_credentials. */
  reason?: string
  source: 'db' | 'env'
  schedule: string
  /**
   * The ENV agenda, structured — what this job runs when no row is stored. It
   * is the first option the editor seeds from, and the baseline a change is
   * compared against while there is no row. A job whose env agenda is disabled
   * reports `{}`: no mode, so nothing about it can be claimed.
   */
  baseline: BackupScheduleConfig
  /**
   * Where this job's objects live in the external bucket — the ADDRESS, never
   * an access: the agent publishes endpoint, bucket and key prefix and stops
   * there (INV-171). Absent when no external bucket is configured.
   */
  destination?: BackupDestination
}

export type BackupDestination = {
  endpoint: string
  bucket: string
  prefix: string
}

export type BackupAgentState = {
  seen_at: string
  version: string
  /**
   * The newest migration the agent's build knows how to read. Absent on a
   * heartbeat written before the field existed — and absent is NOT a match:
   * `RequiredSchemaVersion` is a floor on both sides, so an older agent boots
   * fine on a newer schema and silently ignores the fields it never learned.
   */
  schema_version?: number
  jobs: Record<string, BackupAgentJobReport>
}

export type BackupScheduleResponse = {
  jobs: BackupJob[]
  /** Only the jobs with a stored row — absent key = env baseline. */
  rows: Record<string, BackupScheduleRow>
  /**
   * The compiled floors the server enforces. The client renders them as hints
   * and as add/remove affordances; the hard refusal stays the server's, and
   * its 400 message is what the editor displays (INV-138 by analogy).
   */
  bounds: {
    times_min: number
    times_max: number
    weekdays_min: number
    /** The dump is the instance's disaster floor, so at least five days a week. */
    dump_weekdays_min: number
    interval_min: number
    interval_max: number
  }
  /** null = the agent never wrote a heartbeat (never ran on this instance). */
  agent: BackupAgentState | null
  /**
   * The document shape THIS backend writes (backupagent.RequiredSchemaVersion).
   * Paired with the heartbeat's own `schema_version` it is how the band names
   * build skew: the client compares the two numbers the server sent, it does
   * not re-derive the policy (INV-138).
   */
  agent_schema_version: number
}

/** Shares the ['admin','backup'] prefix so the run-now invalidation covers it. */
export const backupScheduleQueryKey = ['admin', 'backup', 'schedule'] as const

export async function fetchBackupSchedule(): Promise<BackupScheduleResponse> {
  const { data } = await http.get<BackupScheduleResponse>('/api/admin/backup/schedule')
  return data
}

/** Owner-only (`instance.backup_schedule`); an admin gets 403 `forbidden`. */
export async function saveBackupSchedule(
  job: BackupJob,
  config: BackupScheduleConfig,
): Promise<{ job: BackupJob; config: BackupScheduleConfig }> {
  const { data } = await http.put<{ job: BackupJob; config: BackupScheduleConfig }>(
    `/api/admin/backup/schedule/${job}`,
    config,
  )
  return data
}

/** Deletes the row — the agent falls back to the env baseline on next sync. */
export async function resetBackupSchedule(job: BackupJob): Promise<void> {
  await http.delete(`/api/admin/backup/schedule/${job}`)
}

export async function emailAvailable(
  email: string,
  signal: AbortSignal,
): Promise<AvailabilityResponse> {
  const { data } = await http.get<AvailabilityResponse>('/api/admin/users/email-available', {
    params: { email },
    signal,
  })
  return data
}
