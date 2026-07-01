import { http } from '../api/client'
import { vi } from 'vitest'
import type { Entry, Folder, Link, Note, Tag } from '../api/types'

// Minimal in-memory mock state that intercepts the axios instance used by the
// app. Each test installs the spy once and mutates state to set up scenarios.

export type MockState = {
  tags: Tag[]
  links: Link[]
  notes: Note[]
  folders: Folder[]
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
}

export function freshState(): MockState {
  return { tags: [], links: [], notes: [], folders: [], urlMetadataCalls: [] }
}

type Method = 'get' | 'post' | 'patch' | 'delete'

type Route = {
  url: RegExp
  handle: (m: RegExpMatchArray, data: any, params: URLSearchParams, state: MockState) => any
}

const buildRoutes = (): Record<Method, Route[]> => ({
  get: [
    { url: /^\/api\/tags$/, handle: (_m, _d, _p, s) => s.tags },
    { url: /^\/api\/folders$/, handle: listFolders },
    // /recent-changes is static — keep it before /api/links so the static
    // path matches first; the catch-all /api/links handler is fine after.
    { url: /^\/api\/links\/recent-changes$/, handle: listRecentChanges },
    { url: /^\/api\/links\/url-metadata$/, handle: fetchUrlMetadata },
    { url: /^\/api\/links$/, handle: listLinks },
    { url: /^\/api\/entries$/, handle: listEntries },
    { url: /^\/api\/notes\/(\d+)$/, handle: getNote },
    { url: /^\/api\/notes$/, handle: listNotes },
    { url: /^\/api\/push\/vapid-key$/, handle: () => ({ public_key: 'MOCK_VAPID_PUBLIC' }) },
  ],
  post: [
    { url: /^\/api\/tags$/, handle: createTag },
    { url: /^\/api\/folders$/, handle: createFolder },
    { url: /^\/api\/links\/(\d+)\/refresh-preview$/, handle: () => null },
    { url: /^\/api\/links\/(\d+)\/screenshot$/, handle: captureScreenshot },
    { url: /^\/api\/links\/(\d+)\/seen-change$/, handle: seenChange },
    { url: /^\/api\/links$/, handle: createLink },
    { url: /^\/api\/notes\/images$/, handle: uploadNoteImage },
    { url: /^\/api\/notes$/, handle: createNote },
    { url: /^\/api\/backup$/, handle: backupExport },
    { url: /^\/api\/backup\/validate$/, handle: backupValidate },
    { url: /^\/api\/backup\/restore$/, handle: backupRestore },
    { url: /^\/api\/import\/validate$/, handle: importValidate },
    { url: /^\/api\/import\/apply$/, handle: importApply },
    { url: /^\/api\/push\/subscriptions$/, handle: () => ({ id: 1, created_at: new Date().toISOString() }) },
    { url: /^\/api\/push\/test$/, handle: () => null },
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
    { url: /^\/api\/links\/(\d+)$/, handle: deleteLink },
    { url: /^\/api\/notes\/(\d+)$/, handle: deleteNote },
    { url: /^\/api\/push\/subscriptions$/, handle: () => null },
  ],
})

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

export function installAxiosMock(state: MockState) {
  const routes = buildRoutes()
  for (const method of ['get', 'post', 'patch', 'delete'] as Method[]) {
    vi.spyOn(http, method).mockImplementation((async (url: string, ...rest: any[]) => {
      const data = method === 'get' || method === 'delete' ? undefined : rest[0]
      // For methods that carry a body the request config is rest[1]; for GET/
      // DELETE it's rest[0]. axios callers pass query params via `config.params`
      // rather than embedding them in the URL — merge those into the URLSearchParams
      // so route handlers see the same shape regardless of the caller style.
      const configIdx = method === 'get' || method === 'delete' ? 0 : 1
      const config = (rest[configIdx] ?? {}) as { params?: Record<string, unknown> }
      const [path, queryStr = ''] = url.split('?')
      const params = new URLSearchParams(queryStr)
      if (config.params && typeof config.params === 'object') {
        for (const [k, v] of Object.entries(config.params)) {
          if (v != null) params.append(k, String(v))
        }
      }
      for (const route of routes[method]) {
        const m = path.match(route.url)
        if (m) {
          try {
            const out = route.handle(m, data, params, state)
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

function listFolders(_m: RegExpMatchArray, _d: any, params: URLSearchParams, s: MockState) {
  const root = params.get('root') === '1' || params.get('root') === 'true'
  const parentRaw = params.get('parent_id')
  const parentID = parentRaw ? Number(parentRaw) : null
  if (parentID && parentID > 0) return s.folders.filter((f) => f.parent_id === parentID)
  if (root) return s.folders.filter((f) => f.parent_id == null)
  return s.folders
}

function createFolder(_m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState): Folder {
  const f: Folder = {
    id: (s.folders.at(-1)?.id ?? 0) + 1,
    name: data.name,
    color: data.color ?? '#6366F1',
    parent_id: data.parent_id ?? null,
    link_count: 0,
    folder_count: 0,
    preview_links: [],
    preview_folders: [],
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
  return f
}

function deleteFolder(m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState) {
  const id = Number(m[1])
  const idx = s.folders.findIndex((x) => x.id === id)
  if (idx < 0) throw notFound()
  s.folders.splice(idx, 1)
  for (const l of s.links) {
    if (l.folder_id === id) l.folder_id = null
  }
  return null
}

function createTag(_m: RegExpMatchArray, data: any, _p: URLSearchParams, s: MockState): Tag {
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
  const tags = (data.tag_ids ?? [])
    .map((id: number) => s.tags.find((x) => x.id === id))
    .filter((t: Tag | undefined): t is Tag => Boolean(t))
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
  if (data.tag_ids !== undefined) {
    l.tags = data.tag_ids
      .map((id: number) => s.tags.find((x) => x.id === id))
      .filter((t: Tag | undefined): t is Tag => Boolean(t))
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

function captureScreenshot(m: RegExpMatchArray, _d: any, _p: URLSearchParams, s: MockState): { url: string } {
  const id = Number(m[1])
  const link = s.links.find((x) => x.id === id)
  if (!link) throw notFound()
  return { url: `/api/files/screenshots/${id}.png` }
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
  const tags = (data.tag_ids ?? [])
    .map((id: number) => s.tags.find((x) => x.id === id))
    .filter((t: Tag | undefined): t is Tag => Boolean(t))
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
  if (data.tag_ids !== undefined) {
    n.tags = data.tag_ids
      .map((id: number) => s.tags.find((x) => x.id === id))
      .filter((t: Tag | undefined): t is Tag => Boolean(t))
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
function listEntries(_m: RegExpMatchArray, _d: any, params: URLSearchParams, s: MockState): Entry[] {
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
      inserted: { links: 5, tags: 2, folders: 1, link_tags: 3, click_logs: 8, files: 0, file_bytes: 0 },
      skipped:  { links: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
      wiped:    { links: 0, tags: 0, folders: 0, link_tags: 0, click_logs: 0, files: 0, file_bytes: 0 },
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
    counts: { links: 5, tags: 2, folders: 1, link_tags: 3, click_logs: 8, files: 0, file_bytes: 0 },
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
        { path: 'Bookmarks Bar', name: 'Bookmarks Bar', count: 2 },
        { path: 'Work', name: 'Work', count: 2 },
      ],
      links: [
        { url: 'https://a.test', title: 'A', folder: 'Bookmarks Bar', tags: [], conflict: false },
        { url: 'https://b.test', title: 'B', folder: 'Bookmarks Bar', tags: [], conflict: true },
        { url: 'https://c.test', title: 'C', folder: 'Work', tags: [], conflict: false },
        { url: 'https://d.test', title: 'D', folder: 'Work', tags: [], conflict: false },
      ],
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
