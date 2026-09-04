import { useEffect, useRef, useState } from 'react'
import { useUrlMetadataPrefill } from './useUrlMetadataPrefill'
import type { CheckInterval } from '../lib/time'
import type { Link } from '../api/types'

export function useLinkDialogForm(
  open: boolean,
  link: Link | null,
  initialUrl?: string,
  defaultFolderId?: number | null,
) {
  const [url, setUrl] = useState('')
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [pinned, setPinned] = useState(false)
  const [folderId, setFolderId] = useState<number | null>(null)
  const [checkInterval, setCheckInterval] = useState<CheckInterval | null>(null)
  const [autofillFailed, setAutofillFailed] = useState(false)
  const [autofillPending, setAutofillPending] = useState(false)
  const [ogPreview, setOgPreview] = useState<string | undefined>(undefined)
  const [saveError, setSaveError] = useState<string | null>(null)
  const urlInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!open) return
    setUrl(link?.url ?? initialUrl ?? '')
    setTitle(link?.title ?? '')
    setDescription(link?.description ?? '')
    setPinned(link?.pinned ?? false)
    setFolderId(link?.folder_id ?? defaultFolderId ?? null)
    setCheckInterval(link?.check_interval ?? null)
    setAutofillFailed(false)
    setAutofillPending(false)
    setOgPreview(undefined)
    setSaveError(null)
  }, [open, link, initialUrl, defaultFolderId])

  useUrlMetadataPrefill({
    url,
    skip: !!link,
    setTitle,
    setDescription,
    setAutofillFailed,
    setAutofillPending,
    setOgPreview,
  })

  const values = {
    url,
    title,
    description,
    pinned,
    folderId,
    checkInterval,
  }
  return {
    ...values,
    autofillFailed,
    autofillPending,
    ogPreview,
    saveError,
    urlInputRef,
    setUrl,
    setTitle,
    setDescription,
    setPinned,
    setFolderId,
    setCheckInterval,
    setSaveError,
  }
}
