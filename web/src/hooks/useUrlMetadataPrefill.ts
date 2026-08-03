import { useEffect, type Dispatch, type SetStateAction } from 'react'
import { useFetchUrlMetadata } from '../api/links'
import { looksLikeUrl } from '../lib/url'

type PrefillTarget = {
  url: string
  /** When set (edit mode), auto-fetch is skipped. */
  skip: boolean
  setTitle: Dispatch<SetStateAction<string>>
  setDescription: Dispatch<SetStateAction<string>>
  setAutofillFailed: (failed: boolean) => void
}

/** Debounced URL metadata prefill for LinkDialog create mode. */
export function useUrlMetadataPrefill({
  url,
  skip,
  setTitle,
  setDescription,
  setAutofillFailed,
}: PrefillTarget) {
  const fetchMetadata = useFetchUrlMetadata()

  useEffect(() => {
    if (skip) return
    const trimmed = url.trim()
    if (!trimmed || !looksLikeUrl(trimmed)) return

    const controller = new AbortController()
    setAutofillFailed(false)
    const timer = window.setTimeout(() => {
      fetchMetadata.mutate(
        { url: trimmed, signal: controller.signal },
        {
          onSuccess: (data) => {
            if (data.title) {
              setTitle((cur) => (cur.trim() ? cur : data.title))
            }
            if (data.description) {
              setDescription((cur) => (cur.trim() ? cur : data.description))
            }
          },
          onError: (_err) => {
            const code = (_err as { code?: string })?.code
            setAutofillFailed(code !== 'ERR_CANCELED')
          },
        },
      )
    }, 500)

    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps -- fetchMetadata is stable from useMutation
  }, [url, skip])
}
