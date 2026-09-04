import { useEffect, type Dispatch, type SetStateAction } from 'react'
import { useFetchUrlMetadata } from '../api/links'
import { hostOf, looksLikeUrl } from '../lib/url'

type PrefillTarget = {
  url: string
  /** When set (edit mode), auto-fetch is skipped. */
  skip: boolean
  setTitle: Dispatch<SetStateAction<string>>
  setDescription: Dispatch<SetStateAction<string>>
  setAutofillFailed: (failed: boolean) => void
  setAutofillPending: (pending: boolean) => void
  setOgPreview: (url: string | undefined) => void
}

function isCanceled(err: unknown): boolean {
  const value = err as { code?: string; name?: string }
  return value.code === 'ERR_CANCELED' || value.name === 'CanceledError'
}

function fillEmpty(setter: Dispatch<SetStateAction<string>>, next: string) {
  if (!next) return
  setter((cur) => (cur.trim() ? cur : next))
}

function hostnameOf(raw: string): string {
  return hostOf(raw) || hostOf('https://' + raw)
}

/** Debounced URL metadata prefill for LinkDialog create mode. */
export function useUrlMetadataPrefill({
  url,
  skip,
  setTitle,
  setDescription,
  setAutofillFailed,
  setAutofillPending,
  setOgPreview,
}: PrefillTarget) {
  const fetchMetadata = useFetchUrlMetadata()

  useEffect(() => {
    if (skip) {
      setAutofillPending(false)
      return
    }
    const trimmed = url.trim()
    if (!trimmed || !looksLikeUrl(trimmed)) {
      setAutofillPending(false)
      setOgPreview(undefined)
      return
    }

    const controller = new AbortController()
    setAutofillFailed(false)
    const timer = window.setTimeout(() => {
      setAutofillPending(true)
      fetchMetadata.mutate(
        { url: trimmed, signal: controller.signal },
        {
          onSuccess: (data) => {
            if (controller.signal.aborted) return
            setAutofillPending(false)
            fillEmpty(setTitle, (data.title ?? '').trim() || hostnameOf(trimmed))
            if ((data.description ?? '').trim()) fillEmpty(setDescription, data.description.trim())
            setOgPreview((data.og_image_url ?? '').trim() || undefined)
          },
          onError: (err) => {
            if (controller.signal.aborted || isCanceled(err)) {
              setAutofillPending(false)
              return
            }
            setAutofillPending(false)
            setAutofillFailed(true)
          },
        },
      )
    }, 500)

    return () => {
      window.clearTimeout(timer)
      controller.abort()
      setAutofillPending(false)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps -- fetchMetadata is stable from useMutation
  }, [url, skip])
}
