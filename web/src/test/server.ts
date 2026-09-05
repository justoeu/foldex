import { http } from '../api/client'
import { vi } from 'vitest'
import type { Entry, Folder, Link, Note, Tag } from '../api/types'

// Minimal in-memory mock state that intercepts the axios instance used by the
// app. Each test installs the spy once and mutates state to set up scenarios.

export type MockState = {
  tags: Tag[]
  /**
   * The signed-in account's own content activity (ADR-46).
   *
   * Rows carry `subject` — the link's title, the folder's name — because this
   * feed's reader IS the actor. The administrative trail never selects that
   * column, so a fixture that leaked one here would be describing a projection
   * the server does not have.
   */
  activity: AuditEntryMock[]
  links: Link[]
  notes: Note[]
  folders: Folder[]
  // Side-channel for folder passwords (ADR-28) — the real Folder type never
  // carries the hash/plaintext, so this mirrors backend/internal/folders'
  // password_hash column outside the public shape. Keyed by folder id;
  // presence of a key = protected. Plaintext is fine here, this is a test
  // double, not a security boundary.
  folderPasswords: Record<number, string>
  // Backup-related state. Tests can drive validation/restore responses by
  // mutating these or by intercepting the route directly.
  backupBlob?: Uint8Array
  backupValidation?: any
  backupRestore?: any
  lastRestoreMode?: string
  // Import preview/apply state.
  importValidation?: any
  importApply?: any
  lastImportMode?: string
  lastImportExcluded?: string[]
  // URL-metadata fetch state. Tests set `urlMetadata` to control the mock
  // response, or `urlMetadataError` to simulate a 502 from the real handler.
  // `urlMetadataCalls` records every fetched URL so tests can assert the
  // debounce + never-overwrite behaviors of LinkDialog.
  urlMetadata?: { title?: string; description?: string; favicon_url?: string; og_image_url?: string }
  urlMetadataError?: any
  urlMetadataCalls: string[]
  // Master recovery password (ADR-29). undefined = not configured. Plaintext
  // is fine here — test double, not a security boundary.
  masterPassword?: string
  // Non-secret reminder hint for the master password.
  masterHint?: string
  /** Identifiers the availability probes report as taken (lowercased). */
  takenIdentifiers?: string[]
  // The instance password floor (ADR-35). Undefined = the compiled-in 8, which
  // is what an untouched test sees; a test raising it exercises the generator
  // against a hardened instance.
  passwordMinLength?: number
  // Every payload POSTed to /api/admin/users, so a test can assert WHICH
  // password reached the server rather than only that a request happened.
  adminCreatedUsers?: Array<Record<string, unknown>>
  // Per-folder unlock attempt tracking (mirrors the backend rate limiter).
  unlockAttempts?: Record<number, { fails: number; lockedUntil: number }>
  // Stats endpoints. Tests populate these to drive StatsPage charts/KPIs.
  statsSummary?: {
    total_links: number
    total_tags: number
    total_clicks: number
    clicks_last_30d: number
    clicks_prev_30d: number
    new_links_last_30d: number
    top_host: string
    top_host_clicks: number
  }
  statsDaily?: { date: string; clicks: number }[]
  statsTop?: {
    id: number
    url: string
    title: string
    slug: string
    host: string
    clicks: number
    clicks_30d: number
    clicks_prev_30d: number
  }[]
  statsTags?: { id: number; name: string; color: string; clicks: number; links: number }[]
  statsStorage?: { objects: number; total_bytes: number } | null
  statsStorageError?: boolean
  // When set, DELETE /api/links/:id/image rejects with this error object.
  linkImageRemoveError?: { status?: number; code?: string; message: string }
  // When set, POST /api/links/:id/image rejects with this error object.
  linkImageUploadError?: { status?: number; code?: string; message: string }
  // When set, POST /api/links/:id/screenshot rejects with this error object.
  linkScreenshotError?: { status?: number; code?: string; message: string }
  // Operational backup status (ADR-43). `backupJobs` overrides the per-job
  // summary; unset, every job reports "never ran". `backupStatusRuns` is the
  // history page; POST /api/admin/backup/run appends a `requested` row here
  // (mirroring the backend) and records the job in `backupRunRequests`, or
  // rejects with 409 `backup_run_pending` when the job already has a
  // requested/running row — same rule the real handler enforces.
  backupJobs?: any[]
  backupStatusRuns?: any[]
  backupRunRequests?: string[]
  // Configurable backup schedule (ADR-44). `backupScheduleRows` is the stored
  // (editable) layer keyed by job — only jobs with a row appear, mirroring the
  // backend. `backupAgent` is the heartbeat; unset = never seen, served as
  // null — and each of its per-job reports carries the structured env
  // `baseline` the editor seeds from. PUT validates the unified shape like
  // backupagent.ValidateJobConfig (400 invalid_schedule) and upserts the row;
  // DELETE removes it. Requests
  // are recorded in `backupSchedulePuts` / `backupScheduleDeletes` so tests
  // assert WHAT was sent, not only that something was.
  backupScheduleRows?: Record<string, any>
  backupAgent?: any
  backupSchedulePuts?: Array<{ job: string; config: any }>
  backupScheduleDeletes?: string[]
  // Limits and abuse (SDD-ABUSE-DEFENSE). `abusePolicy` overrides individual
  // knobs on top of the compiled defaults; `abuseCanWrite` drives the
  // owner-only affordance (default true, so a test that does not care gets the
  // writable form). PUT enforces the SAME two-sided bounds the Go package does
  // and refuses with the SAME sentence — the screen renders that message
  // verbatim, so a mock that invented its own wording would be testing nothing.
  abusePolicy?: Partial<AbusePolicyMock>
  abuseCanWrite?: boolean
  abuseObserved?: Partial<AbuseObservedMock>
  abusePolicyPuts?: AbusePolicyMock[]
  // The anomalies panel. Unset = a quiet instance, which is the healthy state
  // and the one the empty copy has to describe without sounding like a failure.
  anomalies?: AnomalyMock[]
  anomalyWindows?: string[]
  /** GET /api/status — optional-dependency reachability for the app footer. */
  depStatus?: { resources: { id: string; state: string }[] }
  depStatusCalls?: number
}

export function freshState(): MockState {
  return {
    tags: [], links: [], notes: [], folders: [], folderPasswords: {},
    urlMetadataCalls: [], activity: [],
  }
}

type Method = 'get' | 'post' | 'put' | 'patch' | 'delete'

type Route = {
  url: RegExp
  handle: (m: RegExpMatchArray, data: any, params: URLSearchParams, state: MockState, headers: Record<string, string>) => any
}


/** Mirrors the server's answer shape: a boolean plus a closed `reason` set. */
function availability(value: string | null, s: MockState) {
  const v = (value ?? '').trim().toLowerCase()
  if (!v) return { available: false, reason: 'empty' }
  if ((s.takenIdentifiers ?? []).includes(v)) return { available: false, reason: 'taken' }
  return { available: true }
}

const buildRoutes = (): Record<Method, Route[]> => ({
  get: [
    { url: /^\/api\/tags$/, handle: (_m, _d, _p, s) => s.tags },
    {
      url: /^\/api\/status$/,
      handle: (_m, _d, _p, s) => {
        s.depStatusCalls = (s.depStatusCalls ?? 0) + 1
        return s.depStatus ?? { resources: [] }
      },
    },
    // The caller's OWN activity (ADR-46). Real route and real cursor param, so
    // a rename on either side is caught here rather than in a blanket mock —
    // and this is the ONE projection that returns `subject`, which is the
    // property the read split exists to keep true.
    {
      url: /^\/api\/activity$/,
      handle: (_m, _d, p, s) => {
        const before = Number(p.get('before') ?? 0)
        const rows = before > 0 ? s.activity.filter((e) => e.id < before) : s.activity
        return { entries: rows.slice(0, 50) }
      },
    },
    // The availability probes. Present so component tests exercise the REAL
    // route and query-param name — a suite that only blanket-mocks `http.get`
    // stays green through a rename on either side. `takenIdentifiers` is empty
    // by default, so an untouched test sees "available" and nothing blocks.
    { url: /^\/api\/auth\/username-available$/, handle: (_m, _d, p, s) => availability(p.get('u'), s) },
    { url: /^\/api\/admin\/users\/email-available$/, handle: (_m, _d, p, s) => availability(p.get('email'), s) },
    // Read-only here: an admin may READ the policy, and CreateUserDialog needs
    // the floor to generate a password the server will accept.
    { url: /^\/api\/admin\/policy$/, handle: (_m, _d, _p, s) => ({
      admin_second_factor: 'any',
      password_min_length: s.passwordMinLength ?? 8,
      otp_ttl_minutes: 5,
      otp_cooldown_seconds: 60,
      google_allowed_domains: [],
      google_auto_provision: false,
      google_default_role: 'editor',
    }) },
    { url: /^\/api\/folders$/, handle: listFolders },
    // /recent-changes is static — keep it before /api/links so the static
    // path matches first; the catch-all /api/links handler is fine after.
    { url: /^\/api\/links\/recent-changes$/, handle: listRecentChanges },
    { url: /^\/api\/links\/url-metadata$/, handle: fetchUrlMetadata },
    { url: /^\/api\/links\/by-url$/, handle: getLinkByURL },
    { url: /^\/api\/links$/, handle: listLinks },
    { url: /^\/api\/entries$/, handle: listEntries },
    { url: /^\/api\/notes\/(\d+)$/, handle: getNote },
    { url: /^\/api\/notes$/, handle: listNotes },
    // 32-byte zero key, unpadded base64url — valid input for urlBase64ToUint8Array.
    { url: /^\/api\/push\/vapid-key$/, handle: () => ({ public_key: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' }) },
    { url: /^\/api\/settings\/master-password$/, handle: (_m, _d, _p, s) => ({ configured: s.masterPassword !== undefined, hint: s.masterHint ?? null }) },
    { url: /^\/api\/stats\/summary$/, handle: (_m, _d, _p, s) => s.statsSummary ?? {
      total_links: 0, total_tags: 0, total_clicks: 0,
      clicks_last_30d: 0, clicks_prev_30d: 0, new_links_last_30d: 0,
      top_host: '', top_host_clicks: 0,
    } },
    { url: /^\/api\/stats\/daily$/, handle: (_m, _d, _p, s) => s.statsDaily ?? [] },
    { url: /^\/api\/stats\/top$/, handle: (_m, _d, _p, s) => s.statsTop ?? [] },
    { url: /^\/api\/stats\/tags$/, handle: (_m, _d, _p, s) => s.statsTags ?? [] },
    { url: /^\/api\/stats\/storage$/, handle: (_m, _d, _p, s) => {
      if (s.statsStorageError) {
        const e: any = new Error('storage unavailable')
        e.response = { status: 503, data: { error: { code: 'unavailable', message: 'object store down' } } }
        throw e
      }
      return s.statsStorage ?? { objects: 0, total_bytes: 0 }
    } },
    { url: /^\/api\/backup\/download\/status$/, handle: backupDownloadStatus },
    { url: /^\/api\/admin\/backup\/schedule$/, handle: (_m, _d, _p, s) => ({
      jobs: ['dump', 'drill', 'mirror', 'user_zip'],
      rows: s.backupScheduleRows ?? {},
      bounds: SCHEDULE_BOUNDS,
      agent: s.backupAgent ?? null,
      agent_schema_version: AGENT_SCHEMA_VERSION,
    }) },
    { url: /^\/api\/admin\/backup\/runs$/, handle: (_m, _d, _p, s) => ({
      jobs: s.backupJobs ?? ['dump', 'drill', 'mirror', 'user_zip'].map((job) => ({
        job, last_success: null, consecutive_failures: 0,
      })),
      runs: s.backupStatusRuns ?? [],
    }) },
    { url: /^\/api\/admin\/abuse-policy$/, handle: (_m, _d, _p, s) => ({
      policy: abusePolicyOf(s),
      bounds: ABUSE_BOUNDS,
      observed: { ...ABUSE_OBSERVED_NONE, ...(s.abuseObserved ?? {}) },
      can_write: s.abuseCanWrite !== false,
    }) },
    { url: /^\/api\/admin\/anomalies$/, handle: (_m, _d, p, s) => {
      const w = p.get('window') ?? '24h'
      ;(s.anomalyWindows ??= []).push(w)
      const policy = abusePolicyOf(s)
      return {
        window: w,
        thresholds: {
          spray_accounts: policy.anomaly_spray_accounts,
          hammer_failures: policy.anomaly_hammer_failures,
          window_minutes: policy.anomaly_window_minutes,
        },
        anomalies: s.anomalies ?? [],
      }
    } },
  ],
  post: [
    { url: /^\/api\/admin\/users$/, handle: (_m, d, _p, s) => {
      ;(s.adminCreatedUsers ??= []).push(d)
      return { id: 99, email: d.email, name: d.name, role: d.role, status: 'active' }
    } },
    { url: /^\/api\/tags$/, handle: createTag },
    { url: /^\/api\/folders$/, handle: createFolder },
    { url: /^\/api\/links\/(\d+)\/refresh-preview$/, handle: refreshPreview },
    { url: /^\/api\/links\/(\d+)\/screenshot$/, handle: captureScreenshot },
    { url: /^\/api\/links\/(\d+)\/seen-change$/, handle: seenChange },
    { url: /^\/api\/links\/(\d+)\/image$/, handle: uploadLinkImage },
    { url: /^\/api\/links$/, handle: createLink },
    { url: /^\/api\/notes\/images$/, handle: uploadNoteImage },
    { url: /^\/api\/notes$/, handle: createNote },
    { url: /^\/api\/backup\/download$/, handle: backupDownloadTicket },
    { url: /^\/api\/backup$/, handle: backupExport },
    { url: /^\/api\/backup\/validate$/, handle: backupValidate },
    { url: /^\/api\/backup\/restore$/, handle: backupRestore },
    { url: /^\/api\/import\/validate$/, handle: importValidate },
    { url: /^\/api\/import\/apply$/, handle: importApply },
    { url: /^\/api\/push\/subscriptions$/, handle: () => ({ id: 1, created_at: new Date().toISOString() }) },
    { url: /^\/api\/push\/test$/, handle: () => null },
    { url: /^\/api\/folders\/(\d+)\/unlock$/, handle: unlockFolder },
    { url: /^\/api\/folders\/(\d+)\/reset-password$/, handle: resetFolderPassword },
    { url: /^\/api\/admin\/backup\/run$/, handle: (_m, d, _p, s) => {
      const job = d?.job
      if (!['dump', 'drill', 'mirror', 'user_zip'].includes(job)) {
        const e: any = new Error('invalid job')
        e.response = { status: 400, data: { error: { code: 'invalid_job', message: 'job must be one of dump, drill, mirror, user_zip' } } }
        throw e
      }
      const runs = (s.backupStatusRuns ??= [])
      if (runs.some((r: any) => r.job === job && (r.status === 'requested' || r.status === 'running'))) {
        const e: any = new Error('pending')
        e.response = { status: 409, data: { error: { code: 'backup_run_pending', message: 'a run of this job is already requested or running' } } }
        throw e
      }
      ;(s.backupRunRequests ??= []).push(job)
      const id = runs.reduce((max: number, r: any) => Math.max(max, r.id), 0) + 1
      runs.unshift({
        id, job, status: 'requested',
        scheduled_for: new Date().toISOString(), started_at: new Date().toISOString(),
        finished_at: null, artifact_key: null, artifact_bytes: null, artifact_sha256: null,
        objects_scanned: null, objects_copied: null, bytes_copied: null,
        drill_of_run_id: null, last_error: null, meta: {},
      })
      return { id, job, status: 'requested' }
    } },
  ],
  put: [
    { url: /^\/api\/settings\/master-password$/, handle: setMaster },
    { url: /^\/api\/admin\/backup\/schedule\/([a-z_]+)$/, handle: putBackupSchedule },
    { url: /^\/api\/admin\/abuse-policy$/, handle: putAbusePolicy },
  ],
  patch: [
    { url: /^\/api\/tags\/(\d+)$/, handle: patchTag },
    { url: /^\/api\/folders\/(\d+)$/, handle: patchFolder },
    { url: /^\/api\/links\/(\d+)$/, handle: patchLink },
    { url: /^\/api\/notes\/(\d+)$/, handle: patchNote },
  ],
  delete: [
    { url: /^\/api\/tags\/(\d+)$/, handle: deleteTag },
    { url: /^\/api\/folders\/(\d+)$/, handle: deleteFolder },
    { url: /^\/api\/links\/(\d+)\/image$/, handle: removeLinkImage },
    { url: /^\/api\/links\/(\d+)$/, handle: deleteLink },
    { url: /^\/api\/notes\/(\d+)$/, handle: deleteNote },
    { url: /^\/api\/push\/subscriptions$/, handle: () => null },
    { url: /^\/api\/settings\/master-password$/, handle: clearMaster },
    { url: /^\/api\/admin\/backup\/schedule\/([a-z_]+)$/, handle: deleteBackupSchedule },
  ],
})

// ────────────────────────────────────────────────────────────────────────────
// Backup schedule mock handlers (ADR-44).

const SCHEDULE_JOBS = ['dump', 'drill', 'mirror', 'user_zip']
const WEEKDAYS = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat']

/**
 * The compiled floors, in the server's own numbers — the same ones GET
 * /api/admin/backup/schedule answers as `bounds`.
 */

const SCHEDULE_BOUNDS = {
  times_min: 1,
  times_max: 6,
  weekdays_min: 1,
  dump_weekdays_min: 5,
  interval_min: 15,
  interval_max: 1440,
}

/**
 * The document shape the backend writes — backupagent.RequiredSchemaVersion.
 * The band compares the heartbeat's own number against it to tell an agent
 * that predates the unified agenda from one that speaks it.
 */
const AGENT_SCHEMA_VERSION = 43

/** Only user_zip may be switched off — the other three are the instance's floor. */
const MAY_DISABLE = ['user_zip']

// ────────────────────────────────────────────────────────────────────────────
// Limits and abuse (SDD-ABUSE-DEFENSE).

export type AbusePolicyMock = {
  login_distinct_accounts_per_ip: number
  login_failures_per_account: number
  login_window_minutes: number
  api_writes_per_minute: number
  api_expensive_per_hour: number
  public_click_coalesce_seconds: number | null
  anomaly_spray_accounts: number
  anomaly_hammer_failures: number
  anomaly_window_minutes: number
}

export type AbuseObservedMock = {
  days: number
  max_distinct_accounts_per_ip: number
  max_failures_per_account: number
  peak_writes_per_minute: number
}

export type AnomalyMock = {
  kind: 'spray' | 'hammer' | 'throttle'
  ip: string
  ip_trusted: boolean
  distinct_accounts: number
  failures: number
  throttles: number
  first_seen: string
  last_seen: string
  blocked: boolean
  severity: 'critical' | 'warning'
}

/**
 * `abusepolicy.Bounds()`, field for field — including the ORDER, which puts the
 * nullable coalesce knob last because the Go side appends it after the loop.
 * The screen must look its bounds up by name; keeping the odd order here is
 * what makes a screen that reads them positionally fail in a test rather than
 * on an instance.
 */
const ABUSE_BOUNDS = [
  { field: 'login_distinct_accounts_per_ip', min: 3, max: 100, default: 10 },
  { field: 'login_failures_per_account', min: 3, max: 50, default: 5 },
  { field: 'login_window_minutes', min: 5, max: 1440, default: 15 },
  { field: 'api_writes_per_minute', min: 30, max: 6000, default: 120 },
  { field: 'api_expensive_per_hour', min: 5, max: 1000, default: 20 },
  { field: 'anomaly_spray_accounts', min: 2, max: 1000, default: 10 },
  { field: 'anomaly_hammer_failures', min: 3, max: 1000, default: 20 },
  { field: 'anomaly_window_minutes', min: 5, max: 10080, default: 15 },
  { field: 'public_click_coalesce_seconds', min: 0, max: 3600, default: 10 },
]

/** `abusepolicy.Default()`, assembled from the bounds so the two cannot drift. */
function abuseDefaults(): AbusePolicyMock {
  const out: Record<string, number> = {}
  for (const b of ABUSE_BOUNDS) out[b.field] = b.default
  return out as unknown as AbusePolicyMock
}

/** An instance that has seen nothing yet. Zero is "no data", not a measurement. */
const ABUSE_OBSERVED_NONE: AbuseObservedMock = {
  days: 30,
  max_distinct_accounts_per_ip: 0,
  max_failures_per_account: 0,
  peak_writes_per_minute: 0,
}

function abusePolicyOf(s: MockState): AbusePolicyMock {
  return { ...abuseDefaults(), ...(s.abusePolicy ?? {}) }
}

/**
 * `abusepolicy.ValidateForWrite`, refusal for refusal.
 *
 * The message is reproduced to the character because the screen renders it
 * VERBATIM: it names the field and the two real numbers on purpose, and a mock
 * that summarised it would let a component that rewrites the sentence pass.
 */
function putAbusePolicy(_m: RegExpMatchArray, d: any, _p: URLSearchParams, s: MockState) {
  if (s.abuseCanWrite === false) {
    const e: any = new Error('forbidden')
    e.response = { status: 403, data: { error: { code: 'forbidden_role', message: 'only the instance owner may change these limits' } } }
    throw e
  }
  for (const b of ABUSE_BOUNDS) {
    const v = d?.[b.field]
    // The nullable knob: absent means "leave it at the default", a legal write.
    if (v == null && b.field === 'public_click_coalesce_seconds') continue
    if (typeof v !== 'number' || v < b.min || v > b.max) {
      const message = `${b.field} must be between ${b.min} and ${b.max}, got ${v}`
      const e: any = new Error(message)
      e.response = { status: 400, data: { error: { code: 'invalid_policy', message } } }
      throw e
    }
  }
  const policy = { ...abusePolicyOf(s), ...d } as AbusePolicyMock
  ;(s.abusePolicyPuts ??= []).push(policy)
  s.abusePolicy = policy
  return { policy }
}

function invalidSchedule(message: string) {
  const e: any = new Error(message)
  e.response = { status: 400, data: { error: { code: 'invalid_schedule', message } } }
  return e
}

function invalidJob() {
  const e: any = new Error('invalid job')
  e.response = { status: 400, data: { error: { code: 'invalid_job', message: 'job must be one of dump, drill, mirror, user_zip' } } }
  return e
}

/** Go's %q, near enough for the values a schedule document carries. */
function q(v: unknown) {
  return JSON.stringify(typeof v === 'string' ? v : String(v))
}

/**
 * backupagent.ParseAnchor, refusal for refusal. The mock reproduces it rather
 * than testing "HH:MM" with a regex because the messages BELOW are asserted as
 * the server's own words, and a message this file invents is a contract the
 * suite cannot prove (CLAUDE.md §2: the mock tracks the backend).
 */
function parseAnchor(raw: string): { key: string; weekly: boolean } {
  const fields = raw.trim().split(/\s+/).filter((f) => f !== '')
  if (fields.length === 0 || fields.length > 2) {
    throw new Error(`want "HH:MM" or "HH:MM sun", got ${q(raw)}`)
  }
  const hm = fields[0].split(':')
  if (hm.length !== 2) throw new Error(`want "HH:MM", got ${q(fields[0])}`)
  const hour = Number(hm[0])
  const minute = Number(hm[1])
  if (
    !/^-?\d+$/.test(hm[0]) || !/^-?\d+$/.test(hm[1]) ||
    hour < 0 || hour > 23 || minute < 0 || minute > 59
  ) {
    throw new Error(`${q(fields[0])} is not a valid 24h wall time`)
  }
  // Rendered back the way Anchor.String() does, so "3:30" and "03:30" are the
  // same anchor and the repeat check catches them.
  const key = `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`
  if (fields.length === 1) return { key, weekly: false }
  if (!WEEKDAYS.includes(fields[1].toLowerCase())) {
    throw new Error(`${q(fields[1])} is not a weekday (sun..sat)`)
  }
  return { key, weekly: true }
}

/** backupagent.validateTimes. */
function validateTimes(job: string, times: any) {
  const list: any[] = Array.isArray(times) ? times : []
  if (list.length < SCHEDULE_BOUNDS.times_min || list.length > SCHEDULE_BOUNDS.times_max) {
    throw invalidSchedule(
      `${job} needs between ${SCHEDULE_BOUNDS.times_min} and ${SCHEDULE_BOUNDS.times_max} wall times — the floor is one run per scheduled day, never zero`,
    )
  }
  const seen = new Set<string>()
  for (const raw of list) {
    let anchor: { key: string; weekly: boolean }
    try {
      anchor = parseAnchor(String(raw))
    } catch (err) {
      throw invalidSchedule(`${job} time ${q(raw)}: ${(err as Error).message}`)
    }
    if (anchor.weekly) {
      throw invalidSchedule(`${job} time ${q(raw)}: the weekday belongs in "weekdays", not in the time`)
    }
    if (seen.has(anchor.key)) throw invalidSchedule(`${job} time ${q(raw)} repeats`)
    seen.add(anchor.key)
  }
}

/** backupagent.validateWeekdays. */
function validateWeekdays(job: string, days: any, minDays: number) {
  const list: any[] = Array.isArray(days) ? days : []
  if (list.length === 0) {
    throw invalidSchedule(
      `${job} needs at least ${minDays} weekday(s) — an agenda that fires on no day is the job switched off`,
    )
  }
  const seen = new Set<string>()
  for (const raw of list) {
    const wd = String(raw).trim().toLowerCase()
    if (!WEEKDAYS.includes(wd)) throw invalidSchedule(`${job} weekday ${q(raw)} is not one of sun..sat`)
    if (seen.has(wd)) throw invalidSchedule(`${job} weekday ${q(raw)} repeats`)
    seen.add(wd)
  }
  if (seen.size < minDays) {
    throw invalidSchedule(`${job} needs at least ${minDays} weekdays, got ${seen.size}`)
  }
}

/**
 * backupagent.ValidateJobConfig, refusal for refusal and message for message.
 * The UI renders the server's 400 verbatim (INV-138 by analogy), so a test
 * asserting that text is only proving a contract while THIS function says what
 * the backend says — a message invented here drifts, and the assertion then
 * proves the screen renders a string the product never sends.
 */
function validateScheduleConfig(job: string, cfg: any) {
  const floorWeekdays = job === 'dump'
    ? SCHEDULE_BOUNDS.dump_weekdays_min
    : SCHEDULE_BOUNDS.weekdays_min

  if (cfg?.time || cfg?.weekday) {
    throw invalidSchedule('"time" and "weekday" are the previous schedule vocabulary and are read-only — send {"mode":"times","times":[…],"weekdays":[…]}')
  }
  if (cfg?.mode !== 'times' && cfg?.mode !== 'interval') {
    throw invalidSchedule(`${job} needs "mode": "times" or "interval"`)
  }
  if (cfg?.enabled !== undefined && !MAY_DISABLE.includes(job)) {
    throw invalidSchedule(`${job} cannot be switched off — only user_zip carries "enabled", because it is the one job that is a product convenience rather than the instance's protection`)
  }
  if (cfg?.enabled === false) {
    if (
      (Array.isArray(cfg.times) && cfg.times.length > 0) ||
      (Array.isArray(cfg.weekdays) && cfg.weekdays.length > 0) ||
      (cfg.interval_min ?? 0) !== 0
    ) {
      throw invalidSchedule(`a disabled ${job} carries no agenda — send "enabled": false alone`)
    }
    return
  }

  if (cfg.mode === 'times') {
    if ((cfg.interval_min ?? 0) !== 0) {
      throw invalidSchedule('mode "times" does not carry "interval_min"')
    }
    validateTimes(job, cfg.times)
    validateWeekdays(job, cfg.weekdays, floorWeekdays)
    return
  }
  if (cfg.times != null || cfg.weekdays != null) {
    throw invalidSchedule('mode "interval" does not carry "times" or "weekdays"')
  }
  const n = cfg.interval_min
  if (typeof n !== 'number' || n < SCHEDULE_BOUNDS.interval_min || n > SCHEDULE_BOUNDS.interval_max) {
    throw invalidSchedule(
      `${job} interval must be between ${SCHEDULE_BOUNDS.interval_min} and ${SCHEDULE_BOUNDS.interval_max} minutes — a row tunes the cadence, it cannot switch the job off`,
    )
  }
}

function putBackupSchedule(m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState) {
  const job = m[1]
  if (!SCHEDULE_JOBS.includes(job)) throw invalidJob()
  validateScheduleConfig(job, data)
  ;(s.backupSchedulePuts ??= []).push({ job, config: data })
  ;(s.backupScheduleRows ??= {})[job] = {
    job,
    config: data,
    updated_at: new Date().toISOString(),
    updated_by_email: 'admin@foldex.test',
  }
  return { job, config: data }
}

function deleteBackupSchedule(m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState) {
  const job = m[1]
  if (!SCHEDULE_JOBS.includes(job)) throw invalidJob()
  ;(s.backupScheduleDeletes ??= []).push(job)
  if (s.backupScheduleRows) delete s.backupScheduleRows[job]
  return null
}

function fetchUrlMetadata(_m: RegExpMatchArray, _d: any, params: URLSearchParams, s: MockState) {
  const requested = params.get('url') ?? ''
  s.urlMetadataCalls.push(requested)
  if (s.urlMetadataError) throw s.urlMetadataError
  const md = s.urlMetadata ?? {}
  return {
    title: md.title ?? '',
    description: md.description ?? '',
    favicon_url: md.favicon_url ?? '',
    og_image_url: md.og_image_url ?? '',
  }
}

function listRecentChanges(_m: RegExpMatchArray, _d: any, params: URLSearchParams, s: MockState) {
  // Days clamp mirrors the backend (1..30, default 7) and limit (1..100).
  // The real backend filters by last_change_detected_at > now() - days; the
  // mock just returns links that have last_change_detected_at set, sorted
  // descending, capped at limit.
  const limit = Math.min(100, Math.max(1, Number(params.get('limit') ?? '20')))
  const out = s.links
    .filter((l) => !!l.last_change_detected_at)
    .sort((a, b) => (b.last_change_detected_at ?? '').localeCompare(a.last_change_detected_at ?? ''))
    .slice(0, limit)
  return out
}

function seenChange(m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState) {
  const id = Number(m[1])
  const idx = s.links.findIndex((l) => l.id === id)
  if (idx < 0) {
    const e: any = new Error('not found')
    e.response = { status: 404, data: { error: { code: 'not_found', message: 'link not found' } } }
    throw e
  }
  s.links[idx] = { ...s.links[idx], change_seen_at: new Date().toISOString() }
  return null
}

/** One row of the own-activity feed, as /api/activity returns it. */
export type AuditEntryMock = {
  id: number
  action: string
  category: 'content'
  severity: 'info'
  detail: string | null
  ip: string | null
  ip_trusted: boolean
  user_agent: string | null
  entity_kind: string | null
  entity_id: number | null
  subject: string | null
  created_at: string
}

export function installAxiosMock(state: MockState) {
  const routes = buildRoutes()
  for (const method of ['get', 'post', 'put', 'patch', 'delete'] as Method[]) {
    vi.spyOn(http, method).mockImplementation((async (url: string, ...rest: any[]) => {
      // GET has no body; DELETE may carry one via axios's `config.data`.
      const data = method === 'get' ? undefined : method === 'delete' ? rest[0]?.data : rest[0]
      // For methods that carry a body the request config is rest[1]; for GET/
      // DELETE it's rest[0]. axios callers pass query params via `config.params`
      // rather than embedding them in the URL — merge those into the URLSearchParams
      // so route handlers see the same shape regardless of the caller style.
      const configIdx = method === 'get' || method === 'delete' ? 0 : 1
      const config = (rest[configIdx] ?? {}) as { params?: Record<string, unknown>; headers?: Record<string, string> }
      const [path, queryStr = ''] = url.split('?')
      const params = new URLSearchParams(queryStr)
      if (config.params && typeof config.params === 'object') {
        for (const [k, v] of Object.entries(config.params)) {
          if (v != null) params.append(k, String(v))
        }
      }
      const headers = config.headers ?? {}
      for (const route of routes[method]) {
        const m = path.match(route.url)
        if (m) {
          try {
            const out = route.handle(m, data, params, state, headers)
            return { data: out }
          } catch (e: any) {
            return Promise.reject(e)
          }
        }
      }
      const e: any = new Error(`mock: no handler for ${method} ${path}`)
      e.response = { status: 404, data: { error: { code: 'no_handler', message: e.message } } }
      throw e
    }) as any)
  }
}

function listLinks(_m: RegExpMatchArray, _d: any, params: URLSearchParams, s: MockState) {
  let out = [...s.links]
  const q = params.get('q')?.toLowerCase()
  if (q) out = out.filter((l) => l.title.toLowerCase().includes(q) || l.url.toLowerCase().includes(q))
  const tagIds = params.getAll('tag').map(Number).filter((n) => n > 0)
  if (tagIds.length) {
    out = out.filter((l) => tagIds.every((id) => l.tags.some((t) => t.id === id)))
  }
  const folderID = Number(params.get('folder_id') ?? '')
  if (folderID > 0) {
    out = out.filter((l) => l.folder_id === folderID)
  } else if (params.get('ungrouped') === '1') {
    out = out.filter((l) => l.folder_id == null)
  }
  const sort = params.get('sort')
  if (sort === 'clicks') out.sort((a, b) => b.click_count - a.click_count)
  // Honor limit/offset so tests exercising useInfiniteQuery see the same
  // shape the backend produces (page slices, not the full list). Mirrors
  // the clamps in backend/internal/links/repository.go: default 100, cap
  // 500, offset >= 0. Without this, getNextPageParam would compare against
  // the full list length and never terminate.
  const limit = Math.min(500, Math.max(1, Number(params.get('limit') ?? '100')))
  const offset = Math.max(0, Number(params.get('offset') ?? '0'))
  return out.slice(offset, offset + limit)
}

// mockUnlockToken/verifyMockUnlockToken stand in for the real HMAC token
// (folders.IssueUnlockToken/VerifyUnlockToken) — no crypto needed in a test
// double, just something that (a) round-trips through the unlock endpoint,
// (b) is folder-specific (a token for folder 1 must not work for folder 2),
// and (c) changes when the password changes, mirroring the real
// invalidate-on-password-change property.
function mockUnlockToken(id: number, password: string): string {
  return `mock-unlock:${id}:${password}`
}
function verifyMockUnlockToken(id: number, s: MockState, token: string | undefined): boolean {
  const pw = s.folderPasswords[id]
  if (pw === undefined) return true // unprotected — nothing to verify
  return !!token && token === mockUnlockToken(id, pw)
}
function folderLocked() {
  const e: any = new Error('folder locked')
  e.response = { status: 403, data: { error: { code: 'folder_locked', message: 'this folder is password-protected' } } }
  return e
}
function descendantProtected(count: number) {
  const e: any = new Error('protected descendant')
  e.response = {
    status: 409,
    data: {
      error: { code: 'descendant_protected', message: 'folder subtree contains password-protected descendants' },
      count,
    },
  }
  return e
}
function wrongPassword() {
  const e: any = new Error('wrong password')
  e.response = { status: 401, data: { error: { code: 'wrong_password', message: 'incorrect password' } } }
  return e
}

// Redaction mirrors folders.Repository.List's always-on rule (ADR-28): a
// protected folder's preview_links/preview_folders are cleared regardless of
// any unlock token — that token only gates the SEPARATE "list what's inside"
// call, not this one.
function withRedaction(f: Folder): Folder {
  if (!f.has_password) return f
  return { ...f, preview_links: [], preview_folders: [] }
}

function listFolders(_m: RegExpMatchArray, _d: any, params: URLSearchParams, s: MockState, headers: Record<string, string>) {
  const root = params.get('root') === '1' || params.get('root') === 'true'
  const parentRaw = params.get('parent_id')
  const parentID = parentRaw ? Number(parentRaw) : null
  if (parentID && parentID > 0) {
    if (!verifyMockUnlockToken(parentID, s, headers['X-Foldex-Folder-Unlock'])) throw folderLocked()
    return s.folders.filter((f) => f.parent_id === parentID).map(withRedaction)
  }
  if (root) return s.folders.filter((f) => f.parent_id == null).map(withRedaction)
  return s.folders.map(withRedaction)
}

function createFolder(_m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState): Folder {
  const id = (s.folders.at(-1)?.id ?? 0) + 1
  if (data.password) s.folderPasswords[id] = data.password
  const f: Folder = {
    id,
    name: data.name,
    color: data.color ?? '#6366F1',
    parent_id: data.parent_id ?? null,
    has_password: !!data.password,
    link_count: 0,
    folder_count: 0,
    preview_links: [],
    preview_folders: [],
    password_hint: data.password && data.password_hint ? data.password_hint : null,
    created_at: new Date().toISOString(),
  }
  s.folders.push(f)
  return f
}

function patchFolder(m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState): Folder {
  const id = Number(m[1])
  const f = s.folders.find((x) => x.id === id)
  if (!f) throw notFound()
  if (data.name !== undefined) f.name = data.name
  if (data.color !== undefined) f.color = data.color
  // parent_id ships in DnD folder-merge gestures (folder→folder drop) and
  // anywhere the backend PATCH accepts it. Skipping the field here made the
  // App.test DnD assertions vacuous — onMoveFolder fired and the mock did
  // nothing.
  if ('parent_id' in data) f.parent_id = data.parent_id ?? null
  // password is tri-state (ADR-28): absent = unchanged, string = set/
  // replace, null = remove. Changing/removing an EXISTING password requires
  // current_password to match, mirroring folders.Repository.Update.
  if ('password' in data) {
    const currentPw = s.folderPasswords[id]
    if (currentPw !== undefined) {
      if (data.current_password !== currentPw) throw wrongPassword()
    }
    if (data.password == null) {
      delete s.folderPasswords[id]
      f.password_hint = null // removing the password clears the hint (ADR-29)
    } else {
      s.folderPasswords[id] = data.password
    }
    f.has_password = data.password != null
  }
  if ('password_hint' in data) f.password_hint = data.password_hint ?? null
  return f
}

function setMaster(_m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState) {
  if ((data.password ?? '').length < 8) {
    const e: any = new Error('too short')
    e.response = { status: 400, data: { error: { code: 'invalid_input', message: 'master password must be at least 8 characters' } } }
    throw e
  }
  if (s.masterPassword !== undefined && data.current_password !== s.masterPassword) throw wrongPassword()
  s.masterPassword = data.password
  // Tri-state (mirrors backend): hint key absent → keep existing; present →
  // set the trimmed value, empty clears it.
  if (typeof data.hint === 'string') {
    const h = data.hint.trim()
    s.masterHint = h === '' ? undefined : h
  }
  return { configured: true, hint: s.masterHint ?? null }
}

function clearMaster(_m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState) {
  if (s.masterPassword === undefined) return { configured: false, hint: null }
  if (data?.current_password !== s.masterPassword) throw wrongPassword()
  s.masterPassword = undefined
  s.masterHint = undefined
  return { configured: false, hint: null }
}

function resetFolderPassword(m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState) {
  const id = Number(m[1])
  const f = s.folders.find((x) => x.id === id)
  if (!f) throw notFound()
  if (s.masterPassword === undefined) {
    const e: any = new Error('master not configured')
    e.response = { status: 400, data: { error: { code: 'master_not_configured', message: 'no master password configured' } } }
    throw e
  }
  if (data.master_password !== s.masterPassword) {
    const e: any = new Error('wrong master')
    e.response = { status: 401, data: { error: { code: 'wrong_master_password', message: 'incorrect master password' } } }
    throw e
  }
  delete s.folderPasswords[id]
  f.has_password = false
  f.password_hint = null
  return null
}

const MOCK_MAX_UNLOCK = 5
const MOCK_LOCKOUT_MS = 60 * 60_000

function tooManyAttempts(lockedUntil: number) {
  const e: any = new Error('too many attempts')
  e.response = {
    status: 429,
    data: {
      error: { code: 'too_many_attempts', message: 'too many wrong attempts; folder temporarily locked' },
      locked_until: new Date(lockedUntil).toISOString(),
      retry_after_seconds: Math.max(1, Math.ceil((lockedUntil - Date.now()) / 1000)),
    },
  }
  return e
}

function unlockFolder(m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState) {
  const id = Number(m[1])
  const f = s.folders.find((x) => x.id === id)
  if (!f) throw notFound()
  s.unlockAttempts ??= {}
  const st = s.unlockAttempts[id] ?? { fails: 0, lockedUntil: 0 }
  const now = Date.now()
  if (st.lockedUntil > now) throw tooManyAttempts(st.lockedUntil)

  const pw = s.folderPasswords[id]
  if (pw === undefined) {
    const e: any = new Error('not protected')
    e.response = { status: 400, data: { error: { code: 'not_protected', message: 'this folder has no password set' } } }
    throw e
  }
  if (data.password !== pw) {
    if (st.lockedUntil && st.lockedUntil <= now) st.fails = 0
    st.fails += 1
    if (st.fails >= MOCK_MAX_UNLOCK) st.lockedUntil = now + MOCK_LOCKOUT_MS
    s.unlockAttempts[id] = st
    if (st.lockedUntil > now) throw tooManyAttempts(st.lockedUntil)
    const e: any = new Error('wrong password')
    e.response = {
      status: 401,
      data: {
        error: { code: 'wrong_password', message: 'incorrect password' },
        failed_attempts: st.fails,
        attempts_remaining: MOCK_MAX_UNLOCK - st.fails,
      },
    }
    throw e
  }
  delete s.unlockAttempts[id]
  return {
    unlock_token: mockUnlockToken(id, pw),
    expires_at: new Date(Date.now() + 24 * 60 * 60_000).toISOString(),
  }
}

function deleteFolder(m: RegExpMatchArray, _d: any, params: URLSearchParams, s: MockState, headers: Record<string, string>) {
  const id = Number(m[1])
  const idx = s.folders.findIndex((x) => x.id === id)
  if (idx < 0) throw notFound()
  if (!verifyMockUnlockToken(id, s, headers['X-Foldex-Folder-Unlock'])) throw folderLocked()
  const cascade = params.get('cascade') === '1' || params.get('cascade') === 'true'
  const deleted = new Set<number>([id])
  if (cascade) {
    let added = true
    while (added) {
      added = false
      for (const folder of s.folders) {
        if (folder.parent_id != null && deleted.has(folder.parent_id) && !deleted.has(folder.id)) {
          deleted.add(folder.id)
          added = true
        }
      }
    }
    const count = [...deleted].filter((folderID) => folderID !== id && s.folderPasswords[folderID] !== undefined).length
    if (count > 0) throw descendantProtected(count)
  }
  s.folders = s.folders.filter((folder) => {
    if (deleted.has(folder.id)) return false
    if (!cascade && folder.parent_id === id) folder.parent_id = null
    return true
  })
  for (const folderID of deleted) delete s.folderPasswords[folderID]
  s.links = s.links.filter((link) => {
    if (link.folder_id == null || !deleted.has(link.folder_id)) return true
    if (cascade) return false
    link.folder_id = null
    return true
  })
  s.notes = s.notes.filter((note) => {
    if (note.folder_id == null || !deleted.has(note.folder_id)) return true
    if (cascade) return false
    note.folder_id = null
    return true
  })
  return null
}

function createTag(_m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState): Tag {
  return insertTag(data, s)
}

function insertTag(data: any, s: MockState): Tag {
  const tag: Tag = {
    id: (s.tags.at(-1)?.id ?? 0) + 1,
    name: data.name,
    color: data.color ?? '#6366F1',
    icon: data.icon ?? null,
    link_count: 0,
    created_at: new Date().toISOString(),
  }
  s.tags.push(tag)
  return tag
}

function resolveParentTags(data: any, s: MockState): Tag[] {
  const existing = (data.tag_ids ?? [])
    .map((id: number) => s.tags.find((tag) => tag.id === id))
    .filter((tag: Tag | undefined): tag is Tag => Boolean(tag))
  const pending = (data.pending_tags ?? []).map((tag: any) => insertTag(tag, s))
  return [...existing, ...pending]
}

function patchTag(m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState): Tag {
  const id = Number(m[1])
  const t = s.tags.find((x) => x.id === id)
  if (!t) throw notFound()
  if (data.name !== undefined) t.name = data.name
  if (data.color !== undefined) t.color = data.color
  if (data.icon !== undefined) t.icon = data.icon
  return t
}

function deleteTag(m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState) {
  const id = Number(m[1])
  const idx = s.tags.findIndex((x) => x.id === id)
  if (idx < 0) throw notFound()
  s.tags.splice(idx, 1)
  s.links.forEach((l) => { l.tags = l.tags.filter((t) => t.id !== id) })
  return null
}

// Mirror of the backend's Slugify — kept in sync with internal/links/slug.go.
// Tests don't need accent-folding, just the basic shape. Empty result falls
// back to "link-{id}" the way the real backfill does.
function slugifyForMock(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '') || 'link-untitled'
}

function createLink(_m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState): Link {
  if (s.links.some((l) => l.url === data.url)) {
    const e: any = new Error('url taken')
    e.response = { status: 409, data: { error: { code: 'url_taken', message: 'url already bookmarked' } } }
    throw e
  }
  if (data.slug && s.links.some((l) => l.slug === data.slug)) {
    const e: any = new Error('slug taken')
    e.response = { status: 409, data: { error: { code: 'slug_taken', message: 'slug already in use' } } }
    throw e
  }
  const tags = resolveParentTags(data, s)
  const link: Link = {
    id: (s.links.at(-1)?.id ?? 0) + 1,
    url: data.url,
    title: data.title ?? data.url,
    slug: data.slug ?? slugifyForMock(data.title ?? data.url),
    description: data.description ?? null,
    favicon_url: null,
    og_image_url: null,
    folder_id: data.folder_id ?? null,
    click_count: 0,
    preview_status: 'pending',
    pinned: !!data.pinned,
    preview_error: null,
    last_clicked_at: null,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    // check_interval round-trips so LinkDialog tests can assert the
    // submitted value lands on the (mock) row.
    check_interval: data.check_interval ?? null,
    tags,
  }
  s.links.push(link)
  return link
}

function refreshPreview(m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState) {
  const id = Number(m[1])
  const link = s.links.find((x) => x.id === id)
  if (!link) throw notFound()
  link.preview_status = 'pending'
  return null
}

function getLinkByURL(_m: RegExpMatchArray, _d: any, params: URLSearchParams, s: MockState) {
  const url = params.get('url') ?? ''
  const link = s.links.find((row) => row.url === url)
  if (!link) throw notFound()
  return link
}

function captureScreenshot(m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState): { url: string } {
  if (s.linkScreenshotError) {
    const e: any = new Error(s.linkScreenshotError.message)
    e.response = {
      status: s.linkScreenshotError.status ?? 400,
      data: {
        error: {
          code: s.linkScreenshotError.code ?? 'screenshot_failed',
          message: s.linkScreenshotError.message,
        },
      },
    }
    throw e
  }
  const id = Number(m[1])
  const link = s.links.find((x) => x.id === id)
  if (!link) throw notFound()
  const url = `/api/files/screenshots/${id}.jpg`
  link.og_image_url = url
  link.preview_status = 'ok'
  return { url }
}

function uploadLinkImage(m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState): { url: string } {
  if (s.linkImageUploadError) {
    const e: any = new Error(s.linkImageUploadError.message)
    e.response = {
      status: s.linkImageUploadError.status ?? 500,
      data: {
        error: {
          code: s.linkImageUploadError.code ?? 'storage',
          message: s.linkImageUploadError.message,
        },
      },
    }
    throw e
  }
  const id = Number(m[1])
  const link = s.links.find((x) => x.id === id)
  if (!link) throw notFound()
  const url = `/api/files/links/${id}.jpg`
  link.og_image_url = url
  link.preview_status = 'ok'
  return { url }
}

function removeLinkImage(m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState) {
  if (s.linkImageRemoveError) {
    const e: any = new Error(s.linkImageRemoveError.message)
    e.response = {
      status: s.linkImageRemoveError.status ?? 500,
      data: {
        error: {
          code: s.linkImageRemoveError.code ?? 'storage',
          message: s.linkImageRemoveError.message,
        },
      },
    }
    throw e
  }
  const id = Number(m[1])
  const link = s.links.find((x) => x.id === id)
  if (!link) throw notFound()
  link.og_image_url = null
  return null
}

function patchLink(m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState): Link {
  const id = Number(m[1])
  const l = s.links.find((x) => x.id === id)
  if (!l) throw notFound()
  if (data.url !== undefined) l.url = data.url
  if (data.title !== undefined) l.title = data.title
  if (data.description !== undefined) l.description = data.description
  // folder_id + pinned + slug were silently ignored before. DnD link→folder
  // gestures and the pin badge depend on the mock applying these — without it
  // the App tests pass even when the production mutations are broken.
  if ('folder_id' in data) l.folder_id = data.folder_id ?? null
  if (data.pinned !== undefined) l.pinned = !!data.pinned
  if (data.slug !== undefined) l.slug = data.slug
  // check_interval tri-state: presence flips, null clears.
  if ('check_interval' in data) l.check_interval = data.check_interval ?? null
  if (data.tag_ids !== undefined || data.pending_tags?.length) {
    l.tags = resolveParentTags(data, s)
  }
  return l
}

function deleteLink(m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState) {
  const id = Number(m[1])
  const idx = s.links.findIndex((x) => x.id === id)
  if (idx < 0) throw notFound()
  s.links.splice(idx, 1)
  return null
}

// ────────────────────────────────────────────────────────────────────────────
// Notes + entries mock handlers.

// A single-pass tag-strip (`replace(/<[^>]+>/g, '')`) is "incomplete
// sanitization" against a crafted input like `<<script>script>` — the outer
// `<...>` match leaves `<script>` behind. Loop to a fixed point instead.
// This value is only ever compared as a plain string in test assertions
// (never rendered as HTML), but the mock should still model the real
// backend's htmlsanitize.PlainText behavior faithfully rather than take a
// shortcut a scanner would flag.
function stripTagsForMock(html: string): string {
  let prev: string
  let out = html
  do {
    prev = out
    out = out.replace(/<[^>]*>/g, '')
  } while (out !== prev)
  return out
}

function slugifyForMockNote(s: string): string {
  return (
    s
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '') || 'note-untitled'
  )
}

function listNotes(_m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState): Note[] {
  return s.notes
}

function getNote(m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState): Note {
  const id = Number(m[1])
  const n = s.notes.find((x) => x.id === id)
  if (!n) throw notFound()
  return n
}

function createNote(_m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState): Note {
  const tags = resolveParentTags(data, s)
  const note: Note = {
    id: (s.notes.at(-1)?.id ?? 0) + 1,
    title: data.title,
    slug: data.slug ?? slugifyForMockNote(data.title),
    body_html: data.body_html ?? '',
    pinned: !!data.pinned,
    folder_id: data.folder_id ?? null,
    cover_url: null,
    click_count: 0,
    last_clicked_at: null,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    tags,
  }
  s.notes.push(note)
  return note
}

function patchNote(m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState): Note {
  const id = Number(m[1])
  const n = s.notes.find((x) => x.id === id)
  if (!n) throw notFound()
  if (data.title !== undefined) n.title = data.title
  if (data.body_html !== undefined) n.body_html = data.body_html
  if ('folder_id' in data) n.folder_id = data.folder_id ?? null
  if (data.pinned !== undefined) n.pinned = !!data.pinned
  if (data.slug !== undefined) n.slug = data.slug
  if (data.tag_ids !== undefined || data.pending_tags?.length) {
    n.tags = resolveParentTags(data, s)
  }
  n.updated_at = new Date().toISOString()
  return n
}

function deleteNote(m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState) {
  const id = Number(m[1])
  const idx = s.notes.findIndex((x) => x.id === id)
  if (idx < 0) throw notFound()
  s.notes.splice(idx, 1)
  return null
}

function uploadNoteImage(): { url: string } {
  return { url: '/api/files/notes/mock-uuid.jpg' }
}

// listEntries mirrors listLinks' filter/pagination shape but merges links +
// notes into one Entry[] result — the mock sibling of GET /api/entries. Sort
// support intentionally matches listLinks' existing fidelity level (only
// 'clicks' is explicitly handled; other sort values fall through to
// insertion order) rather than reimplementing the backend's full ORDER BY —
// tests that need a specific order create fixtures in that order already.
function listEntries(_m: RegExpMatchArray, _d: any, params: URLSearchParams, s: MockState, headers: Record<string, string>): Entry[] {
  let linkOut = [...s.links]
  let noteOut = [...s.notes]
  const q = params.get('q')?.toLowerCase()
  if (q) {
    linkOut = linkOut.filter((l) => l.title.toLowerCase().includes(q) || l.url.toLowerCase().includes(q))
    noteOut = noteOut.filter((n) => n.title.toLowerCase().includes(q) || n.body_html.toLowerCase().includes(q))
  }
  const tagIds = params.getAll('tag').map(Number).filter((n) => n > 0)
  if (tagIds.length) {
    linkOut = linkOut.filter((l) => tagIds.every((id) => l.tags.some((t) => t.id === id)))
    noteOut = noteOut.filter((n) => tagIds.every((id) => n.tags.some((t) => t.id === id)))
  }
  const folderID = Number(params.get('folder_id') ?? '')
  if (folderID > 0) {
    // Content-gate mirrors entries.Handler.list (ADR-28): reading a
    // protected folder's links/notes requires proof of the password.
    if (!verifyMockUnlockToken(folderID, s, headers['X-Foldex-Folder-Unlock'])) throw folderLocked()
    linkOut = linkOut.filter((l) => l.folder_id === folderID)
    noteOut = noteOut.filter((n) => n.folder_id === folderID)
  } else if (params.get('ungrouped') === '1') {
    linkOut = linkOut.filter((l) => l.folder_id == null)
    noteOut = noteOut.filter((n) => n.folder_id == null)
  }

  const out: Entry[] = [
    ...linkOut.map<Entry>((l) => ({ kind: 'link', ...l })),
    ...noteOut.map<Entry>((n) => ({
      kind: 'note',
      id: n.id,
      title: n.title,
      slug: n.slug,
      pinned: n.pinned,
      folder_id: n.folder_id,
      created_at: n.created_at,
      updated_at: n.updated_at,
      click_count: n.click_count,
      last_clicked_at: n.last_clicked_at,
      tags: n.tags,
      cover_url: n.cover_url,
      body_text_snippet: n.body_html ? stripTagsForMock(n.body_html).slice(0, 240) : null,
    })),
  ]
  const sort = params.get('sort')
  if (sort === 'clicks') out.sort((a, b) => b.click_count - a.click_count)

  const limit = Math.min(500, Math.max(1, Number(params.get('limit') ?? '100')))
  const offset = Math.max(0, Number(params.get('offset') ?? '0'))
  return out.slice(offset, offset + limit)
}

function notFound() {
  const e: any = new Error('not found')
  e.response = { status: 404, data: { error: { code: 'not_found', message: 'not found' } } }
  return e
}

// ────────────────────────────────────────────────────────────────────────────
// Backup mock handlers.

function backupExport(_m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState) {
  // The hook expects a Blob (responseType:'blob'). The mock just returns the
  // raw bytes — the hook wrapper will see it as `data`. Tests that exercise
  // the download path can set s.backupBlob; otherwise return a minimal ZIP
  // with a parseable uncompressed manifest.json.
  const bytes = s.backupBlob ?? buildMinimalZip(defaultManifest())
  // Cast through ArrayBuffer to dodge TS6.0's narrower BlobPart type.
  return new Blob([bytes.buffer as ArrayBuffer], { type: 'application/zip' })
}

function backupDownloadTicket() {
  return {
    id: 'mock-backup-download',
    download_url: '/api/backup/download?id=mock-backup-download&token=mock-one-time-token',
    status_url: '/api/backup/download/status?id=mock-backup-download',
    filename: 'foldex-backup-20260514T030000Z.zip',
    created_at: '2026-05-14T03:00:00Z',
    expires_at: '2026-05-14T03:01:00Z',
  }
}

function backupDownloadStatus() {
  return {
    id: 'mock-backup-download',
    state: 'complete',
    created_at: '2026-05-14T03:00:00Z',
    duration_ms: 42,
    size_bytes: 1024,
    counts: defaultManifest().counts,
  }
}

function backupValidate(_m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState) {
  return (
    s.backupValidation ?? {
      ok: true,
      manifest: defaultManifest(),
      conflicts: { links: 0, tags: 0, folders: 0 },
      warnings: [],
      errors: [],
    }
  )
}

function backupRestore(_m: RegExpMatchArray, _d: any, params: URLSearchParams, s: MockState) {
  s.lastRestoreMode = params.get('mode') ?? 'skip'
  return (
    s.backupRestore ?? {
      mode: s.lastRestoreMode,
      inserted: { links: 5, notes: 4, tags: 2, folders: 1, link_tags: 3, click_logs: 8, files: 0, file_bytes: 0 },
      skipped:  { links: 0, notes: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
      wiped:    { links: 0, notes: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
      files:    { uploaded: 0, skipped: 0, wiped: 0 },
      warnings: [],
      duration_ms: 42,
    }
  )
}

function defaultManifest() {
  return {
    kind: 'foldex.backup',
    version: '1.0',
    schema_version: 8,
    created_at: '2026-05-14T03:00:00Z',
    counts: { links: 5, notes: 4, tags: 2, folders: 1, link_tags: 3, click_logs: 8, files: 0, file_bytes: 0 },
    checksums: {},
  }
}

// buildMinimalZip writes a single uncompressed manifest.json entry so the
// frontend's central-directory walker can extract counts in tests.
function buildMinimalZip(manifest: any): Uint8Array {
  const enc = new TextEncoder()
  const name = enc.encode('manifest.json')
  const data = enc.encode(JSON.stringify(manifest))
  const crc = crc32(data)

  const localHeader = new Uint8Array(30 + name.length)
  const dv1 = new DataView(localHeader.buffer)
  dv1.setUint32(0, 0x04034b50, true)   // local file header sig
  dv1.setUint16(4, 20, true)            // version needed
  dv1.setUint16(6, 0, true)             // flags
  dv1.setUint16(8, 0, true)             // method = store
  dv1.setUint16(10, 0, true)            // mod time
  dv1.setUint16(12, 0, true)            // mod date
  dv1.setUint32(14, crc, true)          // crc32
  dv1.setUint32(18, data.length, true)  // comp size
  dv1.setUint32(22, data.length, true)  // uncomp size
  dv1.setUint16(26, name.length, true)
  dv1.setUint16(28, 0, true)
  localHeader.set(name, 30)

  const cdEntry = new Uint8Array(46 + name.length)
  const dv2 = new DataView(cdEntry.buffer)
  dv2.setUint32(0, 0x02014b50, true)    // central dir sig
  dv2.setUint16(4, 20, true)
  dv2.setUint16(6, 20, true)
  dv2.setUint16(8, 0, true)
  dv2.setUint16(10, 0, true)            // method = store
  dv2.setUint16(12, 0, true)
  dv2.setUint16(14, 0, true)
  dv2.setUint32(16, crc, true)
  dv2.setUint32(20, data.length, true)
  dv2.setUint32(24, data.length, true)
  dv2.setUint16(28, name.length, true)
  dv2.setUint16(30, 0, true)
  dv2.setUint16(32, 0, true)
  dv2.setUint16(34, 0, true)
  dv2.setUint16(36, 0, true)
  dv2.setUint32(38, 0, true)
  dv2.setUint32(42, 0, true)            // offset of local header
  cdEntry.set(name, 46)

  const eocd = new Uint8Array(22)
  const dv3 = new DataView(eocd.buffer)
  const cdOffset = localHeader.length + data.length
  dv3.setUint32(0, 0x06054b50, true)
  dv3.setUint16(8, 1, true)             // entries on this disk
  dv3.setUint16(10, 1, true)            // entries total
  dv3.setUint32(12, cdEntry.length, true)
  dv3.setUint32(16, cdOffset, true)
  dv3.setUint16(20, 0, true)

  const total = new Uint8Array(localHeader.length + data.length + cdEntry.length + eocd.length)
  let off = 0
  total.set(localHeader, off); off += localHeader.length
  total.set(data, off);        off += data.length
  total.set(cdEntry, off);     off += cdEntry.length
  total.set(eocd, off)
  return total
}

// ────────────────────────────────────────────────────────────────────────────
// Import preview mock handlers.

function importValidate(_m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState) {
  return (
    s.importValidation ?? {
      format: 'netscape',
      counts: { links: 4, folders: 2, tags: 1 },
      conflicts: { links: 1, folders: 0, tags: 0 },
      folders: [
        { path: 'Bookmarks Bar', name: 'Bookmarks Bar', count: 2, conflicts: 1 },
        { path: 'Work', name: 'Work', count: 2, conflicts: 0 },
      ],
      ungrouped: { links: 0, conflicts: 0 },
      warnings: [],
    }
  )
}

function importApply(_m: RegExpMatchArray, d: any, _p: URLSearchParams, s: MockState) {
  if (d instanceof FormData) {
    s.lastImportMode = String(d.get('mode') ?? '')
    const ex = d.get('exclude_folders')
    s.lastImportExcluded = ex ? String(ex).split(',').filter(Boolean) : []
  }
  return (
    s.importApply ?? {
      format: 'netscape',
      mode: s.lastImportMode || 'skip',
      imported: 3,
      skipped: 1,
      wiped: 0,
      warnings: [],
    }
  )
}

// crc32 — only used by buildMinimalZip in tests.
function crc32(bytes: Uint8Array): number {
  let table: number[] | null = null
  if (!table) {
    table = []
    for (let i = 0; i < 256; i++) {
      let c = i
      for (let k = 0; k < 8; k++) c = (c & 1) ? 0xedb88320 ^ (c >>> 1) : (c >>> 1)
      table.push(c)
    }
  }
  let crc = 0xffffffff
  for (let i = 0; i < bytes.length; i++) {
    crc = (crc >>> 8) ^ table[(crc ^ bytes[i]) & 0xff]
  }
  return (crc ^ 0xffffffff) >>> 0
}
