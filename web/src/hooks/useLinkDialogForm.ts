import { useEffect, useRef, useState } from 'react'
import { useUrlMetadataPrefill } from './useUrlMetadataPrefill'
import { slugifyClient } from '../lib/slugify'
import type { CheckInterval } from '../lib/time'
import type { LinkDialogValues } from '../lib/linkDialogPayload'
import type { Link } from '../api/types'

export function useLinkDialogForm(
  open: boolean,
  link: Link | null,
  initialUrl?: string,
  defaultFolderId?: number | null,
) {
  const [url, setUrl] = useState('')
  const [title, setTitle] = useState('')
  const [slug, setSlug] = useState('')
  const [slugDirty, setSlugDirty] = useState(false)
  const [description, setDescription] = useState('')
  const [pinned, setPinned] = useState(false)
  const [folderId, setFolderId] = useState<number | null>(null)
  const [checkInterval, setCheckInterval] = useState<CheckInterval | null>(null)
  const [autofillFailed, setAutofillFailed] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const urlInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!open) return
    setUrl(link?.url ?? initialUrl ?? '')
    setTitle(link?.title ?? '')
    setSlug(link?.slug ?? '')
    setSlugDirty(!!link?.slug)
    setDescription(link?.description ?? '')
    setPinned(link?.pinned ?? false)
    setFolderId(link?.folder_id ?? defaultFolderId ?? null)
    setCheckInterval(link?.check_interval ?? null)
    setAutofillFailed(false)
    setSaveError(null)
  }, [open, link, initialUrl])

  useEffect(() => {
    if (!slugDirty) setSlug(slugifyClient(title))
  }, [slugDirty, title])

  useUrlMetadataPrefill({
    url,
    skip: !!link,
    setTitle,
    setDescription,
    setAutofillFailed,
  })

  const values: LinkDialogValues = {
    url,
    title,
    slug,
    slugDirty,
    description,
    pinned,
    folderId,
    checkInterval,
  }
  return {
    ...values,
    autofillFailed,
    saveError,
    urlInputRef,
    setUrl,
    setTitle,
    setSlug,
    setSlugDirty,
    setDescription,
    setPinned,
    setFolderId,
    setCheckInterval,
    setSaveError,
  }
}
