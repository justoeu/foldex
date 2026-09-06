import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  validateImport,
  useApplyImport,
  type ImportFormat,
  type ImportMode,
  type ImportResult,
  type ImportValidation,
} from '../api/importer'
import { apiErrorText } from '../lib/apiError'

export type ImportPreviewPhase = 'loading' | 'ready' | 'applying' | 'done' | 'error'

export function effectiveImportCounts(
  validation: ImportValidation | null,
  excluded: Set<string>,
): { links: number; folders: number; conflicts: number } {
  if (!validation) return { links: 0, folders: 0, conflicts: 0 }
  let links = validation.ungrouped.links
  let conflicts = validation.ungrouped.conflicts
  let folders = 0
  for (const folder of validation.folders) {
    if (excluded.has(folder.path)) continue
    links += folder.count
    conflicts += folder.conflicts
    folders++
  }
  return { links, folders, conflicts }
}

export function deriveImportPreviewPhase(state: {
  loading: boolean
  applying: boolean
  report: ImportResult | null
  errMsg: string | null
  validation: ImportValidation | null
}): ImportPreviewPhase {
  if (state.report) return 'done'
  if (state.applying) return 'applying'
  if (state.loading) return 'loading'
  if (state.errMsg && !state.validation) return 'error'
  return 'ready'
}

export function useImportPreview(file: File, format: ImportFormat) {
  const { t } = useTranslation()
  const [validation, setValidation] = useState<ImportValidation | null>(null)
  const [loading, setLoading] = useState(true)
  const [errMsg, setErrMsg] = useState<string | null>(null)
  const [mode, setMode] = useState<ImportMode>('skip')
  const [excluded, setExcluded] = useState<Set<string>>(new Set())
  const [applying, setApplying] = useState(false)
  const [report, setReport] = useState<ImportResult | null>(null)
  const validationAbortRef = useRef<AbortController | null>(null)
  const applyAbortRef = useRef<AbortController | null>(null)
  const applyLockedRef = useRef(false)
  const applyImport = useApplyImport()

  const phase = deriveImportPreviewPhase({ loading, applying, report, errMsg, validation })

  useEffect(() => {
    const controller = new AbortController()
    validationAbortRef.current = controller
    setLoading(true)
    setValidation(null)
    setExcluded(new Set())
    setErrMsg(null)
    validateImport(file, format, controller.signal)
      .then((v) => { if (!controller.signal.aborted) setValidation(v) })
      .catch((e) => {
        if (!controller.signal.aborted) setErrMsg(apiErrorText(e, t('common.unknown_error')))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
        if (validationAbortRef.current === controller) validationAbortRef.current = null
      })
    return () => {
      controller.abort()
      if (validationAbortRef.current === controller) validationAbortRef.current = null
    }
  }, [file, format, t])

  useEffect(() => () => applyAbortRef.current?.abort(), [file, format])

  const effectiveCounts = useMemo(
    () => effectiveImportCounts(validation, excluded),
    [validation, excluded],
  )

  const apply = async () => {
    if (applyLockedRef.current) return
    applyLockedRef.current = true
    const controller = new AbortController()
    applyAbortRef.current = controller
    setApplying(true)
    setErrMsg(null)
    try {
      const r = await applyImport.mutateAsync({
        file,
        format,
        mode,
        excludeFolders: Array.from(excluded),
        signal: controller.signal,
      })
      if (!controller.signal.aborted) setReport(r)
    } catch (e: unknown) {
      if (!controller.signal.aborted) {
        applyLockedRef.current = false
        setErrMsg(apiErrorText(e, t('common.unknown_error')))
      }
    } finally {
      if (applyAbortRef.current === controller) applyAbortRef.current = null
      if (!controller.signal.aborted) setApplying(false)
    }
  }

  const abort = () => {
    validationAbortRef.current?.abort()
    applyAbortRef.current?.abort()
  }

  const requestClose = () => {
    if (applyLockedRef.current || report) return false
    validationAbortRef.current?.abort()
    return true
  }

  const toggle = (path: string) => {
    setExcluded((prev) => {
      const next = new Set(prev)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }
  const selectAll = () => setExcluded(new Set())
  const selectNone = () => setExcluded(new Set((validation?.folders ?? []).map((f) => f.path)))

  return {
    phase,
    validation,
    mode,
    setMode,
    excluded,
    toggle,
    selectAll,
    selectNone,
    effectiveCounts,
    errMsg,
    report,
    apply,
    abort,
    requestClose,
    canApply: phase === 'ready' && effectiveCounts.links > 0,
    canClose: phase !== 'applying' && phase !== 'done',
  }
}
