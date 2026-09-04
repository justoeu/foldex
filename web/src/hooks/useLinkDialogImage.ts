import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { captureLinkScreenshot } from '../api/links'
import { apiErrorCode } from '../lib/apiError'
import type { Link } from '../api/types'

export function useLinkDialogImage(open: boolean, link: Link | null) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [uploadError, setUploadError] = useState<string | null>(null)
  const [captureWarning, setCaptureWarning] = useState<string | null>(null)
  const [dragging, setDragging] = useState(false)
  const [file, setFile] = useState<File | null>(null)
  const [preview, setPreview] = useState<string | null>(null)
  const [removed, setRemoved] = useState(false)
  const [busy, setBusy] = useState(false)
  const [previewBroken, setPreviewBroken] = useState(false)
  const [captureOnSave, setCaptureOnSave] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)
  // The VISIBLE drop zone. Separate from fileInputRef, which points at the
  // hidden <input type="file"> — focusing that one does nothing and scrolls
  // nowhere, because a display:none element has no box.
  const pickerRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) {
      setFile(null)
      setPreview(null)
      return
    }
    setUploadError(null)
    setCaptureWarning(null)
    setDragging(false)
    setFile(null)
    setPreview(null)
    setRemoved(false)
    setPreviewBroken(false)
    setCaptureOnSave(false)
  }, [open, link])

  useEffect(() => () => {
    if (preview) URL.revokeObjectURL(preview)
  }, [preview])

  const selectFile = (nextFile: File) => {
    if (!nextFile.type.startsWith('image/')) {
      setUploadError(t('link_dialog.image_error_type'))
      return
    }
    setUploadError(null)
    setCaptureWarning(null)
    setRemoved(false)
    setPreviewBroken(false)
    setCaptureOnSave(false)
    setFile(nextFile)
    setPreview((current) => {
      if (current) URL.revokeObjectURL(current)
      return URL.createObjectURL(nextFile)
    })
  }

  const remove = () => {
    setFile(null)
    setPreview(null)
    setRemoved(true)
    setPreviewBroken(false)
    setCaptureOnSave(false)
    setCaptureWarning(null)
  }

  const captureScreenshot = async (): Promise<boolean> => {
    if (!link) {
      setUploadError(null)
      setCaptureWarning(null)
      setCaptureOnSave(true)
      setRemoved(false)
      return true
    }
    setBusy(true)
    setUploadError(null)
    setCaptureWarning(null)
    try {
      const { url } = await captureLinkScreenshot(link.id)
      setRemoved(false)
      setPreviewBroken(false)
      setCaptureWarning(null)
      setFile(null)
      setPreview((current) => {
        if (current?.startsWith('blob:')) URL.revokeObjectURL(current)
        return url
      })
      queryClient.invalidateQueries({ queryKey: ['links'] })
      queryClient.invalidateQueries({ queryKey: ['entries'] })
      queryClient.invalidateQueries({ queryKey: ['folders'] })
      return true
    } catch (error) {
      setCaptureWarning(screenshotErrorMessage(error, t))
      return false
    } finally {
      setBusy(false)
    }
  }

  return {
    uploadError,
    captureWarning,
    dragging,
    file,
    preview,
    removed,
    busy,
    previewBroken,
    captureOnSave,
    fileInputRef,
    pickerRef,
    setUploadError,
    setDragging,
    setBusy,
    setPreviewBroken,
    selectFile,
    remove,
    captureScreenshot,
  }
}

function screenshotErrorMessage(
  error: unknown,
  t: (key: string) => string,
): string {
  const code = apiErrorCode(error)
  if (code === 'storage_unavailable') return t('link_dialog.image_error_storage')
  if (code === 'screenshot_failed' || code === 'private_target' || code === 'invalid_scheme') {
    return t('link_dialog.image_error_screenshot')
  }
  const value = error as { response?: { data?: { error?: { message?: string } }; status?: number } }
  if (value.response?.status === 404 && !code) return t('link_dialog.image_error_storage')
  return value.response?.data?.error?.message ?? t('link_dialog.image_error_screenshot')
}
