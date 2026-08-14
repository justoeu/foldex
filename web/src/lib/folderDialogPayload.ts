import type { Folder, FolderCreate, FolderUpdate } from '../api/types'
import { makeGradient } from './tagColor'
import type { ColorMode } from '../components/ColorModeFields'

export type FolderDialogValues = {
  name: string
  mode: ColorMode
  solid: string
  gradFrom: string
  gradTo: string
  parentChoice: number | null
  parentDirty: boolean
  password: string
  passwordEditing: boolean
  currentPassword: string
  newPassword: string
  removePassword: boolean
  hint: string
}

export function folderDialogColor(values: FolderDialogValues): string {
  return values.mode === 'gradient' ? makeGradient(values.gradFrom, values.gradTo) : values.solid
}

export function folderHintMatchesPassword(folder: Folder | null | undefined, values: FolderDialogValues): boolean {
  const candidate = folder?.has_password && values.passwordEditing && !values.removePassword
    ? values.newPassword
    : values.password
  const hint = values.hint.trim()
  return !!hint && !!candidate && hint.toLowerCase() === candidate.toLowerCase()
}

export function buildFolderCreatePayload(
  values: FolderDialogValues,
  parentId?: number | null,
): FolderCreate {
  const body: FolderCreate = {
    name: values.name.trim(),
    color: folderDialogColor(values),
    parent_id: parentId ?? null,
  }
  if (values.password) {
    body.password = values.password
    const hint = values.hint.trim()
    if (hint) body.password_hint = hint
  }
  return body
}

export function buildFolderUpdatePayload(folder: Folder, values: FolderDialogValues): FolderUpdate {
  const body: FolderUpdate = {
    name: values.name.trim(),
    color: folderDialogColor(values),
  }
  if (values.parentDirty) body.parent_id = values.parentChoice
  addPasswordUpdate(body, folder, values)
  addHintUpdate(body, folder, values)
  return body
}

function addPasswordUpdate(body: FolderUpdate, folder: Folder, values: FolderDialogValues): void {
  if (!folder.has_password) {
    if (!values.password) return
    body.password = values.password
    const hint = values.hint.trim()
    if (hint) body.password_hint = hint
    return
  }
  if (!values.passwordEditing) return
  if (values.removePassword) {
    body.password = null
    body.current_password = values.currentPassword
    return
  }
  if (values.newPassword) {
    body.password = values.newPassword
    body.current_password = values.currentPassword
  }
}

function addHintUpdate(body: FolderUpdate, folder: Folder, values: FolderDialogValues): void {
  if (!folder.has_password || (values.passwordEditing && values.removePassword)) return
  const hint = values.hint.trim()
  if (hint !== (folder.password_hint ?? '')) body.password_hint = hint || null
}

export function folderDescendants(rootId: number, folders: Folder[]): Set<number> {
  const descendants = new Set<number>([rootId])
  let changed = true
  while (changed) {
    changed = false
    for (const folder of folders) {
      if (folder.parent_id == null || !descendants.has(folder.parent_id) || descendants.has(folder.id)) continue
      descendants.add(folder.id)
      changed = true
    }
  }
  return descendants
}
