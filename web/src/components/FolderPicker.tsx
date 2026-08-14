import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { useFolderPickerController } from '../hooks/useFolderPickerController'
import type { FolderPickerRow } from '../lib/folderPicker'

type Props = {
  selected: number | null
  onChange: (id: number | null) => void
  parentId?: number | null
  excludeIds?: Set<number>
}

type Controller = ReturnType<typeof useFolderPickerController>

export function FolderPicker(props: Props) {
  const { t } = useTranslation()
  const picker = useFolderPickerController(props)
  return (
    <div ref={picker.rootRef} className="fx-folderpicker" data-open={picker.open ? 'true' : 'false'}>
      <Icon d={I.folder} size={14} />
      <input
        ref={picker.inputRef}
        className="fx-folderpicker-input"
        value={picker.inputValue}
        onChange={(event) => picker.onInputChange(event.target.value)}
        onFocus={() => picker.setOpen(true)}
        onClick={() => picker.setOpen(true)}
        onKeyDown={picker.onKeyDown}
        placeholder={picker.selectedFolder ? picker.selectedFolder.name : t('folder_picker.placeholder')}
        aria-label={t('folder_picker.input_aria')}
        aria-autocomplete="list"
        aria-expanded={picker.open}
        aria-controls="fx-folderpicker-list"
        autoComplete="off"
        disabled={picker.busy}
      />
      <button
        type="button"
        className="fx-folderpicker-chevron"
        onClick={picker.toggle}
        aria-label={t('folder_picker.toggle_aria')}
        tabIndex={-1}
      >
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" aria-hidden="true">
          <path d="M6 9l6 6 6-6" />
        </svg>
      </button>
      {picker.open && <FolderPickerOptions picker={picker} />}
    </div>
  )
}

function FolderPickerOptions({ picker }: { picker: Controller }) {
  const { t } = useTranslation()
  return (
    <ul id="fx-folderpicker-list" role="listbox" className="fx-folderpicker-list" aria-label={t('folder_picker.list_aria')}>
      {picker.rows.length === 0 && (
        <li className="fx-folderpicker-empty" role="presentation">{t('folder_picker.no_match')}</li>
      )}
      {picker.rows.map((row, index) => (
        <FolderPickerOption
          key={row.kind === 'folder' ? `f-${row.id}` : row.kind}
          row={row}
          index={index}
          active={index === picker.highlight}
          selected={isSelected(row, picker.selected)}
          onHighlight={picker.setHighlight}
          onCommit={picker.commit}
        />
      ))}
    </ul>
  )
}

function FolderPickerOption({
  row,
  index,
  active,
  selected,
  onHighlight,
  onCommit,
}: {
  row: FolderPickerRow
  index: number
  active: boolean
  selected: boolean
  onHighlight: (index: number) => void
  onCommit: (row: FolderPickerRow) => Promise<void>
}) {
  const { t } = useTranslation()
  return (
    <li
      role="option"
      aria-selected={selected}
      className={
        'fx-folderpicker-row' +
        (active ? ' fx-folderpicker-row-active' : '') +
        (row.kind === 'create' ? ' fx-folderpicker-row-create' : '') +
        (selected ? ' fx-folderpicker-row-chosen' : '')
      }
      onMouseDown={(event) => {
        event.preventDefault()
        void onCommit(row)
      }}
      onMouseEnter={() => onHighlight(index)}
    >
      <span className="fx-folderpicker-row-icon" aria-hidden="true">
        <Icon d={row.kind === 'create' ? I.plus : row.kind === 'folder' ? I.folder : I.x} size={row.kind === 'none' ? 11 : 13} />
      </span>
      <span className="fx-folderpicker-row-label">
        {row.kind === 'folder' && row.hasPassword && (
          <span className="fx-folder-lock-icon" aria-hidden="true" data-tooltip={t('folder_card.locked_tooltip')} data-tooltip-side="top">
            <Icon d={I.lock} size={12} />
          </span>
        )}
        {row.label}
      </span>
      {selected && (
        <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" aria-hidden="true">
          <path d="M5 12l5 5 9-12" />
        </svg>
      )}
    </li>
  )
}

function isSelected(row: FolderPickerRow, selected: number | null): boolean {
  if (row.kind === 'none') return selected === null
  return row.kind === 'folder' && row.id === selected
}
