import { useCallback, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useUpdateLink } from '../api/links'
import { useUpdateNote } from '../api/notes'
import { useCreateFolder, useUpdateFolder } from '../api/folders'
import type { Folder, MergeSource } from '../api/types'
import { isSameEntry, wouldCreateFolderCycle } from '../AppDnd'

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
  const queryClient = useQueryClient()
  const updateLink = useUpdateLink()
  const updateNote = useUpdateNote()
  const createFolder = useCreateFolder()
  const updateFolder = useUpdateFolder()
  const [mergeError, setMergeError] = useState<string | null>(null)

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

  const reconcileMerge = useCallback(async () => {
    await Promise.allSettled([
      queryClient.invalidateQueries({ queryKey: ['entries'] }),
      queryClient.invalidateQueries({ queryKey: ['folders'] }),
      queryClient.invalidateQueries({ queryKey: ['links'] }),
    ])
  }, [queryClient])

  const onMergeEntries = useCallback(async (a: MergeSource, b: MergeSource) => {
    if (isSameEntry(a, b)) return
    setMergeError(null)
    let folder: Folder
    try {
      folder = await createFolder.mutateAsync({
        name: t('home.merge_new_folder_name'),
        parent_id: openFolder,
      })
    } catch {
      await reconcileMerge()
      setMergeError(t('home.merge_error'))
      return
    }

    const moves = await Promise.allSettled([
      moveEntryToFolder(a, folder.id),
      moveEntryToFolder(b, folder.id),
    ])
    if (moves.some((result) => result.status === 'rejected')) {
      await reconcileMerge()
      setMergeError(t('home.merge_partial_error'))
      return
    }
    onFolderCreated(folder)
  }, [createFolder.mutateAsync, moveEntryToFolder, onFolderCreated, openFolder, reconcileMerge, t])

  return {
    onMoveLinkToFolder,
    onMoveNoteToFolder,
    onMoveFolder,
    onMergeEntries,
    mergeError,
    dismissMergeError: () => setMergeError(null),
  }
}

export type AppDndController = ReturnType<typeof useAppDndController>
