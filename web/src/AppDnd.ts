import { useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { useUpdateLink } from './api/links'
import { useUpdateNote } from './api/notes'
import { useCreateFolder, useUpdateFolder } from './api/folders'
import type { Folder, MergeSource } from './api/types'

export function wouldCreateFolderCycle(
  folders: Folder[],
  sourceId: number,
  targetId: number,
): boolean {
  if (sourceId === targetId) return true
  const childrenByParent = new Map<number, number[]>()
  for (const folder of folders) {
    if (folder.parent_id == null) continue
    const children = childrenByParent.get(folder.parent_id) ?? []
    children.push(folder.id)
    childrenByParent.set(folder.parent_id, children)
  }

  const stack = [...(childrenByParent.get(sourceId) ?? [])]
  const seen = new Set<number>()
  while (stack.length > 0) {
    const id = stack.pop() as number
    if (id === targetId) return true
    if (seen.has(id)) continue
    seen.add(id)
    stack.push(...(childrenByParent.get(id) ?? []))
  }
  return false
}

export function isSameEntry(a: MergeSource, b: MergeSource): boolean {
  return a.kind === b.kind && a.id === b.id
}

export function useAppDndController({
  folders,
  openFolder,
  onFolderCreated,
}: {
  folders: Folder[] | undefined
  openFolder: number | null
  onFolderCreated: (folder: Folder) => void
}) {
  const { t } = useTranslation()
  const updateLink = useUpdateLink()
  const updateNote = useUpdateNote()
  const createFolder = useCreateFolder()
  const updateFolder = useUpdateFolder()

  const onMoveLinkToFolder = useCallback((linkId: number, folderId: number) => {
    updateLink.mutate({ id: linkId, body: { folder_id: folderId } })
  }, [updateLink.mutate])

  const onMoveNoteToFolder = useCallback((noteId: number, folderId: number) => {
    updateNote.mutate({ id: noteId, body: { folder_id: folderId } })
  }, [updateNote.mutate])

  const onMoveFolder = useCallback((sourceId: number, targetId: number) => {
    if (wouldCreateFolderCycle(folders ?? [], sourceId, targetId)) return
    updateFolder.mutate({ id: sourceId, body: { parent_id: targetId } })
  }, [folders, updateFolder.mutate])

  const moveEntryToFolder = useCallback((source: MergeSource, folderId: number) => (
    source.kind === 'link'
      ? updateLink.mutateAsync({ id: source.id, body: { folder_id: folderId } })
      : updateNote.mutateAsync({ id: source.id, body: { folder_id: folderId } })
  ), [updateLink.mutateAsync, updateNote.mutateAsync])

  const onMergeEntries = useCallback(async (a: MergeSource, b: MergeSource) => {
    if (isSameEntry(a, b)) return
    try {
      const folder = await createFolder.mutateAsync({
        name: t('home.merge_new_folder_name'),
        parent_id: openFolder,
      })
      await Promise.all([moveEntryToFolder(a, folder.id), moveEntryToFolder(b, folder.id)])
      onFolderCreated(folder)
    } catch {
      // Mutation errors already surface through the shared API error handling.
    }
  }, [createFolder.mutateAsync, moveEntryToFolder, onFolderCreated, openFolder, t])

  return {
    onMoveLinkToFolder,
    onMoveNoteToFolder,
    onMoveFolder,
    onMergeEntries,
  }
}

export type AppDndController = ReturnType<typeof useAppDndController>
