export type Tag = {
  id: number
  name: string
  color: string
  icon?: string | null
  link_count?: number
  created_at?: string
}

export type Link = {
  id: number
  url: string
  title: string
  // slug is always set after migration 000009 — backend enforces NOT NULL.
  // Used as the primary path for /go/{slug}; /go/{id} stays as fallback.
  slug: string
  description?: string | null
  favicon_url?: string | null
  og_image_url?: string | null
  click_count: number
  preview_status: 'pending' | 'ok' | 'failed'
  preview_error?: string | null
  last_clicked_at?: string | null
  pinned: boolean
  folder_id?: number | null
  created_at: string
  updated_at: string
  // Change-detection fields (migration 000010). Nullable across the board —
  // a link with check_interval=null is opted out.
  check_interval?: 'hourly' | 'daily' | 'weekly' | null
  last_checked_at?: string | null
  last_fingerprint?: string | null
  last_change_detected_at?: string | null
  change_seen_at?: string | null
  tags: Tag[]
}

export type LinkCreate = {
  url: string
  title: string
  // Optional: backend auto-derives from title when omitted (Slugify).
  slug?: string
  description?: string | null
  tag_ids?: number[]
  pinned?: boolean
  folder_id?: number | null
  check_interval?: 'hourly' | 'daily' | 'weekly' | null
}

export type LinkUpdate = Partial<{
  url: string
  title: string
  // null = regenerate from current title; explicit string = set verbatim.
  slug: string | null
  description: string | null
  tag_ids: number[]
  pinned: boolean
  folder_id: number | null
  // null on PATCH = opt out (backend wipes fingerprint + timestamps).
  check_interval: 'hourly' | 'daily' | 'weekly' | null
}>

export type TagCreate = {
  name: string
  color?: string
  icon?: string | null
}

export type PreviewTile = {
  id: number
  title: string
  og_image_url?: string | null
  favicon_url?: string | null
}

export type PreviewFolderTile = {
  id: number
  name: string
  color: string
}

export type Folder = {
  id: number
  name: string
  color: string
  parent_id?: number | null
  link_count: number
  folder_count: number
  preview_links: PreviewTile[]
  preview_folders: PreviewFolderTile[]
  created_at?: string
}

export type FolderCreate = {
  name: string
  color?: string
  parent_id?: number | null
}

export type FolderUpdate = Partial<{
  name: string
  color: string
  parent_id: number | null
}>

export type Note = {
  id: number
  title: string
  slug: string
  body_html: string
  pinned: boolean
  folder_id?: number | null
  cover_url?: string | null
  click_count: number
  last_clicked_at?: string | null
  created_at: string
  updated_at: string
  tags: Tag[]
}

export type NoteCreate = {
  title: string
  // Optional: backend auto-derives from title when omitted.
  slug?: string
  body_html: string
  tag_ids?: number[]
  pinned?: boolean
  folder_id?: number | null
}

export type NoteUpdate = Partial<{
  title: string
  // null = regenerate from current title; explicit string = set verbatim.
  slug: string | null
  body_html: string
  tag_ids: number[]
  pinned: boolean
  folder_id: number | null
}>

// Entry is the discriminated union GET /api/entries returns — one row per
// link or note, sorted/searched/paginated together by the backend (see
// internal/entries' UNION ALL, ADR-27). The link variant mirrors the full
// Link shape (including change-detection fields) so a kind:'link' Entry can
// be passed anywhere a Link is expected (LinkCard's Monitored chip / unseen-
// change badge / preview-failed indicator all keep working unmodified).
export type Entry =
  | ({ kind: 'link' } & Link)
  | ({
      kind: 'note'
      id: number
      title: string
      slug: string
      pinned: boolean
      folder_id?: number | null
      created_at: string
      updated_at: string
      click_count: number
      last_clicked_at?: string | null
      tags: Tag[]
      cover_url?: string | null
      body_text_snippet?: string | null
    })

// Drag-merge source discriminator shared by LinkCard/NoteCard/FolderCard —
// lives here (not in either card component) so neither card has to import
// the other just to reference the merge-target shape.
export type MergeSource = { kind: 'link' | 'note'; id: number }
