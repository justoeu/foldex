import { useCallback, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { http } from '../api/client'
import { entityUnlockHeaders, invalidateLibrary } from '../api/entries'
import { useUpdateLink } from '../api/links'
import { useUpdateNote } from '../api/notes'
import { useUpdateFolder } from '../api/folders'
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

  // One mutation for the whole merge: routing the create and the two moves
  // through useCreateFolder/useUpdateLink/useUpdateNote fired three
  // sequential rounds of the same library invalidations (folders, entries,
  // links) within milliseconds. Raw calls here invalidate exactly once,
  // when everything has settled.
  const merge = useMutation({
    mutationFn: async ({ a, b }: { a: MergeSource; b: MergeSource }) => {
      const { data: folder } = await http.post<Folder>('/api/folders', {
        name: t('home.merge_new_folder_name'),
        parent_id: openFolder,
      })
      const move = (source: MergeSource) => http.patch(
        source.kind === 'link' ? `/api/links/${source.id}` : `/api/notes/${source.id}`,
        { folder_id: folder.id },
        // Same contract useUpdateLink/useUpdateNote carry: moving OUT of a
        // locked folder needs its unlock token.
        { headers: entityUnlockHeaders(queryClient, source.kind, source.id) },
      )
      const moves = await Promise.allSettled([move(a), move(b)])
      return { folder, partial: moves.some((r) => r.status === 'rejected') }
    },
    onSettled: () => invalidateLibrary(queryClient, { folders: true, links: true, entries: true }),
  })

  const onMergeEntries = useCallback(async (a: MergeSource, b: MergeSource) => {
    if (isSameEntry(a, b)) return
    setMergeError(null)
    try {
      const { folder, partial } = await merge.mutateAsync({ a, b })
      if (partial) {
        setMergeError(t('home.merge_partial_error'))
        return
      }
      onFolderCreated(folder)
    } catch {
      setMergeError(t('home.merge_error'))
    }
  }, [merge.mutateAsync, onFolderCreated, t])

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
