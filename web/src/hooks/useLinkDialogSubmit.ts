import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import { captureLinkScreenshot, removeLinkImage, uploadLinkImage, useCreateLink, useUpdateLink } from '../api/links'
import { apiErrorCode } from '../lib/apiError'
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
  link: Link | null
  values: LinkDialogValues
  selected: SelectedTag[]
  image: ImageState
  setSaveError: (message: string | null) => void
  onClose: () => void
}

export function useLinkDialogSubmit(options: Options) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const create = useCreateLink()
  const update = useUpdateLink()

  const persist = async (): Promise<number | null> => {
    if (options.link) {
      await update.mutateAsync({
        id: options.link.id,
        body: buildLinkUpdatePayload(options.link, options.values, options.selected),
      })
      return options.link.id
    }
    const link = await create.mutateAsync(buildLinkCreatePayload(options.values, options.selected))
    return link?.id ?? null
  }

  const syncImage = async (linkId: number | null) => {
    if (!linkId) return true
    const { file, removed, captureOnSave } = options.image
    if (!file && !removed && !captureOnSave) return true
    options.image.setBusy(true)
    try {
      if (file) await uploadLinkImage(linkId, file)
      else if (captureOnSave) await captureLinkScreenshot(linkId)
      else if (options.link) await removeLinkImage(linkId)
      invalidateImageQueries(queryClient)
      return true
    } catch (error) {
      // Capture-on-save is a hint, not a save gate: the row already exists.
      if (captureOnSave && !file) return true
      options.image.setUploadError(uploadErrorMessage(error, t))
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

function uploadErrorMessage(
  error: unknown,
  t: (key: string) => string,
): string {
  const code = apiErrorCode(error)
  if (code === 'storage_unavailable') return t('link_dialog.image_error_storage')
  if (code === 'screenshot_failed' || code === 'private_target' || code === 'invalid_scheme') {
    return t('link_dialog.image_error_screenshot')
  }
  const value = error as { response?: { data?: { error?: { message?: string } }; status?: number } }
  // Chi's empty 404 (route omitted) has no envelope; a JSON not_found is INV-050.
  if (value.response?.status === 404 && !code) return t('link_dialog.image_error_storage')
  return value.response?.data?.error?.message ?? t('link_dialog.image_error_generic')
}
