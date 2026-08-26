import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { Link } from '../api/types'

export function useLinkDialogImage(open: boolean, link: Link | null) {
  const { t } = useTranslation()
  const [uploadError, setUploadError] = useState<string | null>(null)
  const [dragging, setDragging] = useState(false)
  const [file, setFile] = useState<File | null>(null)
  const [preview, setPreview] = useState<string | null>(null)
  const [removed, setRemoved] = useState(false)
  const [busy, setBusy] = useState(false)
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
    setDragging(false)
    setFile(null)
    setPreview(null)
    setRemoved(false)
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
    setRemoved(false)
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
  }

  return {
    uploadError,
    dragging,
    file,
    preview,
    removed,
    busy,
    fileInputRef,
    pickerRef,
    setUploadError,
    setDragging,
    setBusy,
    selectFile,
    remove,
  }
}
