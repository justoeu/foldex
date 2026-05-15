import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { useCreateFolder, useFolders } from '../api/folders'

type Props = {
  // Currently selected folder id, or null for "no folder".
  selected: number | null
  // Called with the new selection. `null` = unset, otherwise an existing
  // folder id (possibly one this picker just created inline).
  onChange: (id: number | null) => void
  // When the user creates a folder inline, this is the parent_id we
  // attach it to. Defaults to null (root). Set to the current folder id
  // when the dialog was opened from inside a folder so the new folder is
  // a sibling, not a stray root entry.
  parentId?: number | null
}

// Autocomplete combobox for picking a folder. Three sources of options:
//   1. "+ Create folder \"X\"" — first row, shown only when the typed
//      filter has no exact match. Selecting it creates the folder via
//      useCreateFolder and selects the resulting id in one step.
//   2. "No folder" — always second, lets the user clear the selection.
//   3. All existing folders, filtered by the typed input (case-insensitive
//      substring match).
//
// Keyboard:
//   ArrowUp/ArrowDown — move highlight within the visible rows
//   Enter             — pick the highlighted row
//   Escape            — close the dropdown without changing the selection
//   Tab               — close the dropdown (lets focus flow naturally)
//
// The component never renders the underlying folder.id in the UI — only
// `f.name` — to keep with the §4 "internal ids never appear in the URL/UI"
// invariant.
export function FolderPicker({ selected, onChange, parentId }: Props) {
  const { t } = useTranslation()
  const { data: folders = [] } = useFolders()
  const createFolder = useCreateFolder()

  const [open, setOpen] = useState(false)
  const [filter, setFilter] = useState('')
  const [highlight, setHighlight] = useState(0)
  const [busy, setBusy] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)

  const selectedFolder = useMemo(
    () => folders.find((f) => f.id === selected) ?? null,
    [folders, selected],
  )

  // Filter the existing list against the typed input.
  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase()
    if (!q) return folders
    return folders.filter((f) => f.name.toLowerCase().includes(q))
  }, [folders, filter])

  // "Create" row appears only if (a) the user typed something and (b) it
  // doesn't already exist. We compare against ALL folders (not just the
  // filtered list) so the create option vanishes the instant the typed
  // value matches an existing name exactly, even if the filter would
  // hide it.
  const trimmedFilter = filter.trim()
  const exactMatch = folders.some((f) => f.name.toLowerCase() === trimmedFilter.toLowerCase())
  const showCreateRow = trimmedFilter.length > 0 && !exactMatch

  // Final ordered options the user can highlight + click. Tuple of
  // (kind, label, value) where value lets the click handler dispatch.
  type Row =
    | { kind: 'create'; label: string }
    | { kind: 'none'; label: string }
    | { kind: 'folder'; id: number; label: string }
  const rows: Row[] = useMemo(() => {
    const r: Row[] = []
    if (showCreateRow) {
      r.push({ kind: 'create', label: t('link_dialog.folder_picker_create_inline', { name: trimmedFilter }) })
    }
    r.push({ kind: 'none', label: t('link_dialog.folder_none') })
    for (const f of filtered) r.push({ kind: 'folder', id: f.id, label: f.name })
    return r
  }, [filtered, showCreateRow, t, trimmedFilter])

  // Keep highlight inside bounds when the rows list shrinks/grows.
  useEffect(() => {
    if (highlight >= rows.length) setHighlight(Math.max(0, rows.length - 1))
  }, [rows.length, highlight])

  // Close on outside click.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false)
        setFilter('')
      }
    }
    window.addEventListener('mousedown', onDown)
    return () => window.removeEventListener('mousedown', onDown)
  }, [open])

  const commit = async (row: Row) => {
    if (row.kind === 'create') {
      setBusy(true)
      try {
        const folder = await createFolder.mutateAsync({
          name: trimmedFilter,
          parent_id: parentId ?? null,
        })
        onChange(folder.id)
      } finally {
        setBusy(false)
      }
    } else if (row.kind === 'none') {
      onChange(null)
    } else {
      onChange(row.id)
    }
    setOpen(false)
    setFilter('')
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setOpen(true)
      setHighlight((h) => Math.min(rows.length - 1, h + 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setHighlight((h) => Math.max(0, h - 1))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const row = rows[highlight]
      if (row) void commit(row)
    } else if (e.key === 'Escape') {
      e.preventDefault()
      setOpen(false)
      setFilter('')
    } else if (e.key === 'Tab') {
      setOpen(false)
    }
  }

  // Value shown in the input: while open, the typed filter; while
  // closed, the selected folder's name (or empty for "no folder").
  const inputValue = open ? filter : (selectedFolder?.name ?? '')

  return (
    <div ref={ref} className="fx-folderpicker" data-open={open ? 'true' : 'false'}>
      <Icon d={I.folder} size={14} />
      <input
        ref={inputRef}
        className="fx-folderpicker-input"
        value={inputValue}
        onChange={(e) => {
          setFilter(e.target.value)
          setOpen(true)
          setHighlight(showCreateRow ? 0 : 0)
        }}
        onFocus={() => setOpen(true)}
        onClick={() => setOpen(true)}
        onKeyDown={onKeyDown}
        placeholder={
          selectedFolder ? selectedFolder.name : t('link_dialog.folder_picker_placeholder')
        }
        aria-label={t('link_dialog.folder_aria')}
        aria-autocomplete="list"
        aria-expanded={open}
        aria-controls="fx-folderpicker-list"
        autoComplete="off"
        disabled={busy}
      />
      <button
        type="button"
        className="fx-folderpicker-chevron"
        onClick={() => {
          setOpen((v) => !v)
          inputRef.current?.focus()
        }}
        aria-label={t('link_dialog.folder_picker_toggle_aria')}
        tabIndex={-1}
      >
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" aria-hidden="true">
          <path d="M6 9l6 6 6-6" />
        </svg>
      </button>

      {open && (
        <ul
          id="fx-folderpicker-list"
          role="listbox"
          className="fx-folderpicker-list"
          aria-label={t('link_dialog.folder_label')}
        >
          {rows.length === 0 && (
            <li className="fx-folderpicker-empty" role="presentation">
              {t('link_dialog.folder_picker_no_match')}
            </li>
          )}
          {rows.map((row, i) => {
            const active = i === highlight
            const isSelectedNone = row.kind === 'none' && selected === null
            const isSelectedFolder = row.kind === 'folder' && row.id === selected
            const isChosen = isSelectedNone || isSelectedFolder
            return (
              <li
                key={row.kind === 'folder' ? `f-${row.id}` : row.kind}
                role="option"
                aria-selected={isChosen}
                className={
                  'fx-folderpicker-row' +
                  (active ? ' fx-folderpicker-row-active' : '') +
                  (row.kind === 'create' ? ' fx-folderpicker-row-create' : '') +
                  (isChosen ? ' fx-folderpicker-row-chosen' : '')
                }
                // mousedown so the input doesn't blur before the click
                // reaches us (would close the dropdown via outside-click).
                onMouseDown={(e) => {
                  e.preventDefault()
                  void commit(row)
                }}
                onMouseEnter={() => setHighlight(i)}
              >
                <span className="fx-folderpicker-row-icon" aria-hidden="true">
                  {row.kind === 'create' ? (
                    <Icon d={I.plus} size={13} />
                  ) : row.kind === 'folder' ? (
                    <Icon d={I.folder} size={13} />
                  ) : (
                    <Icon d={I.x} size={11} />
                  )}
                </span>
                <span className="fx-folderpicker-row-label">{row.label}</span>
                {isChosen && (
                  <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" aria-hidden="true">
                    <path d="M5 12l5 5 9-12" />
                  </svg>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
