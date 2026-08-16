import { useEffect, useMemo, useRef, useState, type Dispatch, type KeyboardEvent, type RefObject, type SetStateAction } from 'react'
import { useTranslation } from 'react-i18next'
import { useCreateFolder, useFolders } from '../api/folders'
import { buildFolderPickerRows, nextFolderHighlight, type FolderPickerRow } from '../lib/folderPicker'

type Options = {
  selected: number | null
  onChange: (id: number | null) => void
  parentId?: number | null
  excludeIds?: Set<number>
}

export function useFolderPickerController({ selected, onChange, parentId, excludeIds }: Options) {
  const { t } = useTranslation()
  const { data: allFolders = [] } = useFolders()
  const createFolder = useCreateFolder()
  const [open, setOpen] = useState(false)
  const [filter, setFilter] = useState('')
  const [highlight, setHighlight] = useState(0)
  const [createError, setCreateError] = useState<string | null>(null)
  const rootRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)

  const folders = useMemo(
    () => excludeIds?.size ? allFolders.filter((folder) => !excludeIds.has(folder.id)) : allFolders,
    [allFolders, excludeIds],
  )
  const selectedFolder = useMemo(
    () => folders.find((folder) => folder.id === selected) ?? null,
    [folders, selected],
  )
  const rows = useMemo(
    () => buildFolderPickerRows(folders, filter, {
      create: (name) => t('folder_picker.create_inline', { name }),
      createEmpty: t('folder_picker.create_empty'),
      none: t('folder_picker.none'),
    }),
    [filter, folders, t],
  )

  useEffect(() => {
    if (highlight >= rows.length) setHighlight(Math.max(0, rows.length - 1))
  }, [highlight, rows.length])

  const close = (clearFilter = false) => {
    setOpen(false)
    if (clearFilter) setFilter('')
    setCreateError(null)
  }

  useCloseFolderPickerOnOutside(open, rootRef, setOpen, setFilter, setCreateError)
  const { busy, commit } = useFolderPickerCommit({
    filter,
    parentId,
    onChange,
    inputRef,
    createFolder: createFolder.mutateAsync,
    createErrorMessage: t('folder_picker.create_error'),
    setCreateError,
    close,
  })

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    handleFolderPickerKey(event, rows, highlight, setOpen, setHighlight, close, commit)
  }

  const onInputChange = (value: string) => {
    setFilter(value)
    setCreateError(null)
    setOpen(true)
    setHighlight(0)
  }

  const toggle = () => {
    setOpen((value) => {
      if (value) setCreateError(null)
      return !value
    })
    inputRef.current?.focus()
  }

  return {
    rootRef,
    inputRef,
    open,
    filter,
    highlight,
    busy,
    createError,
    rows,
    selected,
    selectedFolder,
    inputValue: open ? filter : (selectedFolder?.name ?? ''),
    setOpen,
    setHighlight,
    onInputChange,
    onKeyDown,
    commit,
    toggle,
  }
}

function useCloseFolderPickerOnOutside(
  open: boolean,
  rootRef: RefObject<HTMLDivElement | null>,
  setOpen: Dispatch<SetStateAction<boolean>>,
  setFilter: Dispatch<SetStateAction<string>>,
  setCreateError: Dispatch<SetStateAction<string | null>>,
): void {
  useEffect(() => {
    if (!open) return
    const onMouseDown = (event: MouseEvent) => {
      if (rootRef.current?.contains(event.target as Node)) return
      setOpen(false)
      setFilter('')
      setCreateError(null)
    }
    window.addEventListener('mousedown', onMouseDown)
    return () => window.removeEventListener('mousedown', onMouseDown)
  }, [open, rootRef, setCreateError, setFilter, setOpen])
}

function useFolderPickerCommit({
  filter,
  parentId,
  onChange,
  inputRef,
  createFolder,
  createErrorMessage,
  setCreateError,
  close,
}: {
  filter: string
  parentId?: number | null
  onChange: (id: number | null) => void
  inputRef: RefObject<HTMLInputElement | null>
  createFolder: (body: { name: string; parent_id: number | null }) => Promise<{ id: number }>
  createErrorMessage: string
  setCreateError: Dispatch<SetStateAction<string | null>>
  close: (clearFilter?: boolean) => void
}) {
  const [busy, setBusy] = useState(false)
  const commit = async (row: FolderPickerRow) => {
    if (row.kind !== 'create') {
      setCreateError(null)
      onChange(row.kind === 'none' ? null : row.id)
      close(true)
      return
    }
    const name = filter.trim()
    if (!name) {
      inputRef.current?.focus()
      return
    }
    setCreateError(null)
    setBusy(true)
    try {
      const folder = await createFolder({ name, parent_id: parentId ?? null })
      onChange(folder.id)
      close(true)
    } catch {
      setCreateError(createErrorMessage)
      inputRef.current?.focus()
    } finally {
      setBusy(false)
    }
  }
  return { busy, commit }
}

function handleFolderPickerKey(
  event: KeyboardEvent<HTMLInputElement>,
  rows: FolderPickerRow[],
  highlight: number,
  setOpen: Dispatch<SetStateAction<boolean>>,
  setHighlight: Dispatch<SetStateAction<number>>,
  close: (clearFilter?: boolean) => void,
  commit: (row: FolderPickerRow) => Promise<void>,
): void {
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    if (event.key === 'ArrowDown') setOpen(true)
    const direction = event.key === 'ArrowDown' ? 'ArrowDown' : 'ArrowUp'
    setHighlight((current) => nextFolderHighlight(current, direction, rows.length))
    return
  }
  if (event.key === 'Enter') {
    event.preventDefault()
    const row = rows[highlight]
    if (row) void commit(row)
    return
  }
  if (event.key === 'Escape') {
    event.preventDefault()
    close(true)
    return
  }
  if (event.key === 'Tab') close()
}
