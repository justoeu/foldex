import { useEffect, useState } from 'react'
import type { Folder } from '../api/types'
import { isGradient, parseGradient } from '../lib/tagColor'
import { suggestColor } from '../lib/suggestColor'
import type { ColorMode } from '../components/ColorModeFields'
import type { FolderDialogValues } from '../lib/folderDialogPayload'

export function useFolderDialogForm(
  open: boolean,
  folder?: Folder | null,
  /** Colours already in use, so a new folder does not open on one of them. */
  takenColors: readonly string[] = [],
) {
  const [name, setName] = useState('')
  const [mode, setMode] = useState<ColorMode>('solid')
  const [solid, setSolid] = useState('#6366F1')
  const [gradFrom, setGradFrom] = useState('#6366F1')
  const [gradTo, setGradTo] = useState('#EC4899')
  const [parentChoice, setParentChoice] = useState<number | null>(null)
  const [parentDirty, setParentDirty] = useState(false)
  const [password, setPassword] = useState('')
  const [passwordEditing, setPasswordEditing] = useState(false)
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [removePassword, setRemovePassword] = useState(false)
  const [hint, setHint] = useState('')
  const [passwordError, setPasswordError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    // Suggested only for a NEW folder. Doing it on EDIT would silently
    // repaint a folder the user opened to rename, and they would have to
    // notice the swatch changed to undo it.
    const color = folder?.color ?? suggestColor(takenColors)
    const gradient = isGradient(color) ? parseGradient(color) : null
    setName(folder?.name ?? '')
    setMode(gradient ? 'gradient' : 'solid')
    setSolid(gradient?.from ?? color)
    setGradFrom(gradient?.from ?? color)
    setGradTo(gradient?.to ?? '#EC4899')
    setParentChoice(folder?.parent_id ?? null)
    setParentDirty(false)
    setPassword('')
    resetPasswordEdit()
    setHint(folder?.password_hint ?? '')
    setSaveError(null)
    // takenColors is deliberately NOT a dependency: it is a snapshot taken when
    // the dialog opens. As a dep, any change to the folder list while the
    // dialog is open would re-run this and overwrite a colour the user had just
    // picked, with no visible cause.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, folder])

  const resetPasswordEdit = () => {
    setPasswordEditing(false)
    setCurrentPassword('')
    setNewPassword('')
    setRemovePassword(false)
    setPasswordError(null)
  }

  const values: FolderDialogValues = {
    name,
    mode,
    solid,
    gradFrom,
    gradTo,
    parentChoice,
    parentDirty,
    password,
    passwordEditing,
    currentPassword,
    newPassword,
    removePassword,
    hint,
  }

  return {
    ...values,
    passwordError,
    saveError,
    setName,
    setMode,
    setSolid,
    setGradFrom,
    setGradTo,
    setParentChoice,
    setParentDirty,
    setPassword,
    setPasswordEditing,
    setCurrentPassword,
    setNewPassword,
    setRemovePassword,
    setHint,
    setPasswordError,
    setSaveError,
    resetPasswordEdit,
  }
}
