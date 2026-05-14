import { useState, useEffect, useRef } from 'react'
import { Icon, I } from './icons'
import { GradientPicker } from './GradientPicker'
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

const DEFAULT_COLORS = [
  '#6366F1',
  '#0EA5E9',
  '#8B5CF6',
  '#EC4899',
  '#F59E0B',
  '#10B981',
  '#64748B',
  '#FFD400',
]

type Mode = 'solid' | 'gradient'

export function TagDialog({ open, onClose, tag }: Props) {
  const isEdit = !!tag
  const [name, setName] = useState('')
  const [mode, setMode] = useState<Mode>('solid')
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
      aria-label={isEdit ? 'Edit tag' : 'New tag'}
    >
      <div className="fx-modal" style={{ maxWidth: 480 }}>
        <header className="fx-modal-head">
          <div>
            <div className="fx-modal-kicker">{isEdit ? '✎ Editar tag' : '+ Nova tag'}</div>
            <h2 className="fx-modal-title">{isEdit ? `Editar "${tag?.name}"` : 'Criar tag'}</h2>
          </div>
          <button className="fx-confirm-x" onClick={onClose} aria-label="close">
            <Icon d={I.x} size={14} />
          </button>
        </header>

        <div className="fx-modal-body" style={{ gridTemplateColumns: '1fr' }}>
          <div className="fx-modal-col">
            <label className="fx-field">
              <span className="fx-field-label">Nome</span>
              <div className="fx-input">
                <input
                  autoFocus
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="ex: Jira, Dashboards…"
                  aria-label="tag name"
                />
              </div>
            </label>

            <div className="fx-field">
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
                <span className="fx-field-label" style={{ margin: 0 }}>Cor</span>
                <div className="fx-mode-toggle" role="tablist" aria-label="modo de cor">
                  <button
                    type="button"
                    role="tab"
                    aria-selected={mode === 'solid'}
                    className={'fx-mode-tab' + (mode === 'solid' ? ' fx-mode-tab-active' : '')}
                    onClick={() => setMode('solid')}
                  >
                    <Icon d={I.solid} size={11} /> Sólida
                  </button>
                  <button
                    type="button"
                    role="tab"
                    aria-selected={mode === 'gradient'}
                    className={'fx-mode-tab' + (mode === 'gradient' ? ' fx-mode-tab-active' : '')}
                    onClick={() => setMode('gradient')}
                  >
                    <Icon d={I.gradient} size={11} /> Gradiente
                  </button>
                </div>
              </div>

              {mode === 'solid' ? (
                <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
                  {DEFAULT_COLORS.map((c) => (
                    <button
                      key={c}
                      type="button"
                      onClick={() => setSolid(c)}
                      aria-label={`color ${c}`}
                      style={{
                        width: 26,
                        height: 26,
                        borderRadius: 8,
                        background: c,
                        border:
                          c === solid ? '2px solid var(--fx-ink)' : '1px solid var(--fx-border)',
                        cursor: 'pointer',
                      }}
                    />
                  ))}
                  <input
                    type="color"
                    value={solid}
                    onChange={(e) => setSolid(e.target.value)}
                    style={{
                      width: 36,
                      height: 28,
                      border: 0,
                      background: 'transparent',
                      cursor: 'pointer',
                    }}
                    aria-label="custom color"
                  />
                </div>
              ) : (
                <GradientPicker
                  from={gradFrom}
                  to={gradTo}
                  onChange={(f, t) => {
                    setGradFrom(f)
                    setGradTo(t)
                  }}
                />
              )}
            </div>

            <label className="fx-field">
              <span className="fx-field-label">Ícone (emoji, opcional)</span>
              <div className="fx-input">
                <input
                  value={icon}
                  onChange={(e) => setIcon(e.target.value)}
                  placeholder="🪲, 📊, 📚…"
                  maxLength={3}
                  aria-label="tag icon"
                />
              </div>
            </label>
          </div>
        </div>

        <footer className="fx-modal-foot">
          <button className="fx-confirm-btn" onClick={onClose}>
            Cancelar
          </button>
          <button
            className="fx-confirm-btn fx-confirm-btn-primary"
            onClick={submit}
            disabled={!name.trim() || busy}
          >
            <Icon d={isEdit ? I.check : I.plus} size={13} stroke={2.2} />{' '}
            {isEdit ? 'Salvar' : 'Criar tag'}
          </button>
        </footer>
      </div>
    </div>
  )
}
