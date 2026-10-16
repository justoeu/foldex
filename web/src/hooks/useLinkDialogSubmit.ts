import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import { captureLinkScreenshot, removeLinkImage, uploadLinkImage, useCreateLink, useUpdateLink } from '../api/links'
import { apiErrorCode } from '../lib/apiError'
import { linkImageErrorMessage } from './useLinkDialogImage'
import { tagNameTakenErrorKey, type SelectedTag } from '../lib/dialogTags'
import { buildLinkCreatePayload, buildLinkUpdatePayload, type LinkDialogValues } from '../lib/linkDialogPayload'
import type { Link } from '../api/types'

type ImageState = {
  file: File | null
  removed: boolean
  busy: boolean
  captureOnSave: boolean
  setBusy: (busy: boolean) => void
  setUploadError: (message: string | null) => void
}

type Options = {
  open: boolean
  link: Link | null
  values: LinkDialogValues
  selected: SelectedTag[]
  image: ImageState
  setSaveError: (message: string | null) => void
  onClose: () => void
  onPersisted?: (link: Link) => void
}

export function useLinkDialogSubmit(options: Options) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const create = useCreateLink()
  const update = useUpdateLink()
  // Create then image-upload is two requests. Remembering the created row
  // makes a retry PATCH that row (title/tags included) instead of POSTing a
  // second bookmark.
  const [created, setCreated] = useState<Link | null>(null)
  const editedId = options.link?.id ?? null

  useEffect(() => {
    setCreated(null)
  }, [options.open, editedId])

  const persist = async (): Promise<number | null> => {
    if (options.link) {
      const saved = await update.mutateAsync({
        id: options.link.id,
        body: buildLinkUpdatePayload(options.link, options.values, options.selected),
      })
      if (saved) options.onPersisted?.(saved)
      return options.link.id
    }
    if (created) {
      const saved = await update.mutateAsync({
        id: created.id,
        body: buildLinkUpdatePayload(created, options.values, options.selected),
      })
      if (saved) {
        setCreated(saved)
        options.onPersisted?.(saved)
      }
      return created.id
    }
    const link = await create.mutateAsync(buildLinkCreatePayload(options.values, options.selected))
    setCreated(link ?? null)
    return link?.id ?? null
  }

  const syncImage = async (linkId: number | null) => {
    if (!linkId) return true
    const { file, removed, captureOnSave } = options.image
    if (!file && !removed && !captureOnSave) return true
    options.image.setBusy(true)
    try {
      const folderId = options.link?.folder_id ?? created?.folder_id
      if (file) await uploadLinkImage(linkId, file, folderId)
      else if (captureOnSave) await captureLinkScreenshot(linkId, folderId)
      else if (options.link) await removeLinkImage(linkId, folderId)
      invalidateImageQueries(queryClient)
      return true
    } catch (error) {
      // Capture-on-save is a hint, not a save gate: the row already exists.
      if (captureOnSave && !file) return true
      options.image.setUploadError(linkImageErrorMessage(error, t))
      return false
    } finally {
      options.image.setBusy(false)
    }
  }

  const submit = async () => {
    if (!options.values.url.trim()) return
    options.setSaveError(null)
    let linkId: number | null
    try {
      linkId = await persist()
    } catch (error) {
      options.setSaveError(saveErrorMessage(error, t))
      return
    }
    if (!await syncImage(linkId)) return
    options.onClose()
  }

  return {
    submit,
    busy: create.isPending || update.isPending || options.image.busy,
    createdId: created?.id ?? null,
  }
}

function invalidateImageQueries(queryClient: ReturnType<typeof useQueryClient>): void {
  queryClient.invalidateQueries({ queryKey: ['links'] })
  queryClient.invalidateQueries({ queryKey: ['entries'] })
  queryClient.invalidateQueries({ queryKey: ['folders'] })
}

function saveErrorMessage(
  error: unknown,
  t: (key: string, options?: Record<string, unknown>) => string,
): string {
  const code = apiErrorCode(error)
  const tagErrorKey = tagNameTakenErrorKey(error, 'link')
  if (tagErrorKey) return t(tagErrorKey)
  if (code === 'url_taken') return t('link_dialog.error_url_taken')
  if (code === 'slug_taken') return t('link_dialog.error_slug_taken')
  const status = (error as { response?: { status?: number } })?.response?.status
  if (code === 'conflict' || status === 409) return t('link_dialog.error_conflict')
  const message = (error as { response?: { data?: { error?: { message?: string } } } })?.response?.data?.error?.message
  return message || t('link_dialog.error_generic')
}
