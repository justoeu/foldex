import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import { removeLinkImage, uploadLinkImage, useCreateLink, useUpdateLink } from '../api/links'
import { apiErrorCode } from '../lib/apiError'
import { buildLinkCreatePayload, buildLinkUpdatePayload, type LinkDialogValues, type SelectedTag } from '../lib/linkDialogPayload'
import type { Link } from '../api/types'

type ImageState = {
  file: File | null
  removed: boolean
  busy: boolean
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
    if (!linkId || (!options.image.file && !options.image.removed)) return true
    options.image.setBusy(true)
    try {
      if (options.image.file) await uploadLinkImage(linkId, options.image.file)
      else if (options.link) await removeLinkImage(linkId)
      invalidateImageQueries(queryClient)
      return true
    } catch (error) {
      options.image.setUploadError(uploadErrorMessage(error, t('link_dialog.image_error_generic')))
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
  if (code === 'url_taken') return t('link_dialog.error_url_taken')
  if (code === 'slug_taken') return t('link_dialog.error_slug_taken')
  if (code === 'tag_name_taken') return t('link_dialog.error_tag_taken')
  const status = (error as { response?: { status?: number } })?.response?.status
  if (code === 'conflict' || status === 409) return t('link_dialog.error_conflict')
  const message = (error as { response?: { data?: { error?: { message?: string } } } })?.response?.data?.error?.message
  return message || t('link_dialog.error_generic')
}

function uploadErrorMessage(error: unknown, fallback: string): string {
  const value = error as { response?: { data?: { error?: { message?: string } } }; message?: string }
  return value.response?.data?.error?.message ?? value.message ?? fallback
}
