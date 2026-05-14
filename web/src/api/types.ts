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
  tags: Tag[]
}

export type LinkCreate = {
  url: string
  title: string
  description?: string | null
  tag_ids?: number[]
  pinned?: boolean
  folder_id?: number | null
}

export type LinkUpdate = Partial<{
  url: string
  title: string
  description: string | null
  tag_ids: number[]
  pinned: boolean
  folder_id: number | null
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
