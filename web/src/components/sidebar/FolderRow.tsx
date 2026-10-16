import { useTranslation } from 'react-i18next'
import { Icon, I } from '../icons'
import type { FolderPickerRow } from '../../lib/folderPicker'

const PICKER_ROW_ICON = {
  create: I.plus,
  folder: I.folder,
  none: I.x,
} as const

function pickerRowIcon(kind: FolderPickerRow['kind']) {
  return PICKER_ROW_ICON[kind]
}

function pickerRowClass(active: boolean, kind: FolderPickerRow['kind'], selected: boolean): string {
  let className = 'fx-folderpicker-row'
  if (active) className += ' fx-folderpicker-row-active'
  if (kind === 'create') className += ' fx-folderpicker-row-create'
  if (selected) className += ' fx-folderpicker-row-chosen'
  return className
}

export function FolderLockButton() {
  const { t } = useTranslation()
  return (
    <span className="fx-folder-lock-icon" aria-hidden="true" data-tooltip={t('folder_card.locked_tooltip')} data-tooltip-side="top">
      <Icon d={I.lock} size={12} />
    </span>
  )
}

type Props = Readonly<{
  row: FolderPickerRow
  index: number
  active: boolean
  selected: boolean
  onHighlight: (index: number) => void
  onCommit: (row: FolderPickerRow) => Promise<void>
}>

export function FolderRow({ row, index, active, selected, onHighlight, onCommit }: Props) {
  return (
    <li
      role="option"
      aria-selected={selected}
      className={pickerRowClass(active, row.kind, selected)}
      onMouseDown={(event) => {
        event.preventDefault()
        void onCommit(row)
      }}
      onMouseEnter={() => onHighlight(index)}
    >
      <span className="fx-folderpicker-row-icon" aria-hidden="true">
        <Icon d={pickerRowIcon(row.kind)} size={row.kind === 'none' ? 11 : 13} />
      </span>
      <span className="fx-folderpicker-row-label">
        {row.kind === 'folder' && row.hasPassword && <FolderLockButton />}
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
