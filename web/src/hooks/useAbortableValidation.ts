import { useCallback, useEffect, useRef, useState, type DependencyList } from 'react'
import { useTranslation } from 'react-i18next'
import { apiErrorText } from '../lib/apiError'

export type AbortableValidation<T> = {
  data: T | null
  loading: boolean
  errMsg: string | null
  setErrMsg: (message: string | null) => void
  abort: () => void
}

/**
 * Runs an abortable validation whenever `deps` change. Each run owns an
 * AbortController: a superseded or unmounted run is aborted and its late
 * resolution, rejection and loading reset are all dropped, so cancellation
 * never surfaces as an error and a stale result never overwrites a fresh
 * one. `onReset` fires at the start of every run, alongside the data/error
 * reset, for caller state tied to the previous validation.
 */
export function useAbortableValidation<T>(
  run: (signal: AbortSignal) => Promise<T>,
  deps: DependencyList,
  options?: { onReset?: () => void },
): AbortableValidation<T> {
  const { t } = useTranslation()
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [errMsg, setErrMsg] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)
  const onReset = options?.onReset

  useEffect(() => {
    const controller = new AbortController()
    abortRef.current = controller
    setLoading(true)
    setData(null)
    onReset?.()
    setErrMsg(null)
    run(controller.signal)
      .then((v) => {
        if (!controller.signal.aborted) setData(v)
      })
      .catch((e) => {
        if (!controller.signal.aborted) setErrMsg(apiErrorText(e, t('common.unknown_error')))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
        if (abortRef.current === controller) abortRef.current = null
      })
    return () => {
      controller.abort()
      if (abortRef.current === controller) abortRef.current = null
    }
    // Callers pass the full dependency list for `run`/`onReset` captures.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps)

  const abort = useCallback(() => {
    abortRef.current?.abort()
  }, [])

  return { data, loading, errMsg, setErrMsg, abort }
}
