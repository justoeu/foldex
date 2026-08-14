import { useTranslation } from 'react-i18next'
import { useCreateFolder, useDeleteFolder, useUpdateFolder } from '../api/folders'
import type { Folder } from '../api/types'
import { apiErrorCode } from '../lib/apiError'
import {
  buildFolderCreatePayload,
  buildFolderUpdatePayload,
  folderHintMatchesPassword,
  type FolderDialogValues,
} from '../lib/folderDialogPayload'
import { useConfirm } from '../components/ConfirmDialog'
import { usePasswordPrompt, type FolderUnlock } from '../components/PasswordPromptDialog'

type FormErrors = {
  setPasswordError: (message: string | null) => void
  setSaveError: (message: string | null) => void
}

type SaveOptions = FormErrors & {
  folder?: Folder | null
  parentId?: number | null
  values: FolderDialogValues
  onClose: () => void
}

export function useFolderSaveController(options: SaveOptions) {
  const { t } = useTranslation()
  const create = useCreateFolder()
  const update = useUpdateFolder()

  const submit = async () => {
    if (!options.values.name.trim()) return
    options.setPasswordError(null)
    options.setSaveError(null)
    if (folderHintMatchesPassword(options.folder, options.values)) {
      options.setPasswordError(t('folder_dialog.hint_equals_password_error'))
      return
    }
    try {
      if (options.folder) {
        await update.mutateAsync({
          id: options.folder.id,
          body: buildFolderUpdatePayload(options.folder, options.values),
        })
      } else {
        await create.mutateAsync(buildFolderCreatePayload(options.values, options.parentId))
      }
      options.onClose()
    } catch (error) {
      if (apiErrorCode(error) === 'wrong_password') {
        options.setPasswordError(t('folder_dialog.wrong_password_error'))
        return
      }
      options.setSaveError(t('folder_dialog.save_error_generic'))
    }
  }

  return { submit, busy: create.isPending || update.isPending }
}

type DeleteOptions = FormErrors & {
  folder?: Folder | null
  unlockToken?: string
  onUnlocked?: (result: FolderUnlock) => void
  onClose: () => void
}

export function useFolderDeleteController(options: DeleteOptions) {
  const { t } = useTranslation()
  const remove = useDeleteFolder()
  const confirm = useConfirm()
  const promptPassword = usePasswordPrompt()

  const showDeleteError = (error: unknown) => {
    const code = apiErrorCode(error)
    if (code === 'descendant_protected') {
      const count = (error as { response?: { data?: { count?: unknown } } })?.response?.data?.count
      options.setSaveError(t('folder_dialog.delete_error_descendant_protected', {
        count: typeof count === 'number' ? count : 1,
      }))
      return
    }
    options.setSaveError(t(code === 'folder_locked'
      ? 'folder_dialog.delete_error_locked'
      : 'folder_dialog.delete_error_generic'))
  }

  const attemptDelete = async (cascade: boolean, token?: string): Promise<boolean> => {
    try {
      await remove.mutateAsync({ id: options.folder!.id, cascade, unlockToken: token })
      return true
    } catch (error) {
      showDeleteError(error)
      return false
    }
  }

  const deleteFolder = async (cascade: boolean) => {
    const folder = options.folder
    if (!folder) return
    options.setSaveError(null)
    try {
      await remove.mutateAsync({ id: folder.id, cascade, unlockToken: options.unlockToken })
    } catch (error) {
      if (apiErrorCode(error) !== 'folder_locked') {
        showDeleteError(error)
        return
      }
      const result = await promptPassword(folder)
      if (!result) return
      options.onUnlocked?.(result)
      if (!await attemptDelete(cascade, result.token)) return
    }
    options.onClose()
  }

  const requestDelete = async (cascade: boolean) => {
    const folder = options.folder
    if (!folder) return
    const approved = await confirm(deleteConfirmation(folder, cascade, t))
    if (approved) await deleteFolder(cascade)
  }

  return {
    busy: remove.isPending,
    deleteKeepingLinks: () => requestDelete(false),
    deleteCascade: () => requestDelete(true),
  }
}

function deleteConfirmation(
  folder: Folder,
  cascade: boolean,
  t: (key: string, options?: Record<string, unknown>) => string,
) {
  if (!cascade) {
    return {
      title: t('folder_dialog.delete_confirm_title'),
      message: t('folder_dialog.delete_confirm_body', { count: folder.link_count, name: folder.name }),
      confirmLabel: t('folder_dialog.delete_confirm_action'),
      destructive: true,
    }
  }
  return {
    title: t('folder_dialog.delete_cascade_confirm_title'),
    message: folder.link_count > 0
      ? t('folder_dialog.delete_cascade_confirm_body', { count: folder.link_count, name: folder.name })
      : t('folder_dialog.delete_cascade_confirm_body_empty', { name: folder.name }),
    confirmLabel: t('folder_dialog.delete_cascade_confirm_action'),
    destructive: true,
  }
}
