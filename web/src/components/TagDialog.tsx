import { useState, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { ColorModeFields, type ColorMode } from './ColorModeFields'
import { useCreateTag, useUpdateTag } from '../api/tags'
import { useEscape } from '../hooks/useEscape'
import { useFocusTrap } from '../hooks/useFocusTrap'
import { isGradient, makeGradient, parseGradient } from '../lib/tagColor'
import type { Tag } from '../api/types'

type Props = {
  open: boolean
  onClose: () => void
  // When set, the dialog is in EDIT mode: pre-fills from the tag and calls
  // useUpdateTag on submit. When null/undefined, it creates a new tag.
  tag?: Tag | null
}

export function TagDialog({ open, onClose, tag }: Props) {
  const { t } = useTranslation()
  const isEdit = !!tag
  const [name, setName] = useState('')
  const [mode, setMode] = useState<ColorMode>('solid')
  const [solid, setSolid] = useState('#6366F1')
  const [gradFrom, setGradFrom] = useState('#6366F1')
  const [gradTo, setGradTo] = useState('#EC4899')
  const [icon, setIcon] = useState('')
  const create = useCreateTag()
  const update = useUpdateTag()

  useEffect(() => {
    if (!open) return
    if (tag) {
      setName(tag.name)
      setIcon(tag.icon ?? '')
      if (isGradient(tag.color)) {
        const { from, to } = parseGradient(tag.color)
        setMode('gradient')
        setGradFrom(from)
        setGradTo(to)
        setSolid(from)
      } else {
        setMode('solid')
        setSolid(tag.color)
        setGradFrom(tag.color)
        setGradTo('#EC4899')
      }
    } else {
      setName('')
      setMode('solid')
      setSolid('#6366F1')
      setGradFrom('#6366F1')
      setGradTo('#EC4899')
      setIcon('')
    }
  }, [open, tag])

  useEscape(onClose, open)
  const dialogRef = useRef<HTMLDivElement>(null)
  useFocusTrap(dialogRef, open)
  if (!open) return null

  const finalColor = mode === 'gradient' ? makeGradient(gradFrom, gradTo) : solid

  const submit = async () => {
    const trimmed = name.trim()
    if (!trimmed) return
    if (isEdit && tag) {
      await update.mutateAsync({
        id: tag.id,
        body: { name: trimmed, color: finalColor, icon: icon || null },
      })
    } else {
      await create.mutateAsync({ name: trimmed, color: finalColor, icon: icon || null })
    }
    onClose()
  }

  const busy = create.isPending || update.isPending

  return (
    <div
      ref={dialogRef}
      className="fx-overlay fx-overlay-modal"
      role="dialog"
      aria-modal="true"
      aria-label={isEdit ? t('tag_dialog.kicker_edit') : t('tag_dialog.kicker_create')}
    >
      <div className="fx-modal" style={{ maxWidth: 480 }}>
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker">{isEdit ? t('tag_dialog.kicker_edit') : t('tag_dialog.kicker_create')}</div>
            <h2 className="fx-modal-title">{isEdit ? t('tag_dialog.edit_title', { name: tag?.name ?? '' }) : t('tag_dialog.create_title')}</h2>
          </div>
          <button className="fx-confirm-x" onClick={onClose} aria-label={t('common.close')}>
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-modal-body" style={{ gridTemplateColumns: '1fr' }}>
          <div className="fx-modal-col">
            <label className="fx-field">
              <span className="fx-field-label">{t('tag_dialog.name_label')}</span>
              <div className="fx-input">
                <input
                  autoFocus
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={t('tag_dialog.name_placeholder')}
                  aria-label={t('tag_dialog.name_aria')}
                />
              </div>
            </label>

            <ColorModeFields
              mode={mode}
              onModeChange={setMode}
              solid={solid}
              onSolidChange={setSolid}
              gradFrom={gradFrom}
              gradTo={gradTo}
              onGradientChange={(f, to) => {
                setGradFrom(f)
                setGradTo(to)
              }}
              i18nPrefix="tag_dialog"
            />

            <label className="fx-field">
              <span className="fx-field-label">{t('tag_dialog.icon_label')}</span>
              <div className="fx-input">
                <input
                  value={icon}
                  onChange={(e) => setIcon(e.target.value)}
                  placeholder={t('tag_dialog.icon_placeholder')}
                  maxLength={3}
                  aria-label={t('tag_dialog.icon_aria')}
                />
              </div>
            </label>
          </div>
        </div>

        <footer className="fx-modal-foot">
          <button className="fx-confirm-btn" onClick={onClose}>
            {t('common.cancel')}
          </button>
          <button
            className="fx-confirm-btn fx-confirm-btn-primary"
            onClick={submit}
            disabled={!name.trim() || busy}
          >
            <Icon d={isEdit ? I.check : I.plus} size={13} stroke={2.2} />{' '}
            {isEdit ? t('tag_dialog.submit_save') : t('tag_dialog.submit_create')}
          </button>
        </footer>
      </div>
    </div>
  )
}
