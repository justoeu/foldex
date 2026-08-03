import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { GradientPicker } from './GradientPicker'

export type ColorMode = 'solid' | 'gradient'

export const DEFAULT_ENTITY_COLORS = [
  '#6366F1',
  '#0EA5E9',
  '#8B5CF6',
  '#EC4899',
  '#F59E0B',
  '#10B981',
  '#64748B',
  '#FFD400',
] as const

type Props = {
  mode: ColorMode
  onModeChange: (m: ColorMode) => void
  solid: string
  onSolidChange: (c: string) => void
  gradFrom: string
  gradTo: string
  onGradientChange: (from: string, to: string) => void
  /** i18n key prefix — tag_dialog or folder_dialog */
  i18nPrefix: 'tag_dialog' | 'folder_dialog'
}

export function ColorModeFields({
  mode,
  onModeChange,
  solid,
  onSolidChange,
  gradFrom,
  gradTo,
  onGradientChange,
  i18nPrefix,
}: Props) {
  const { t } = useTranslation()
  return (
    <div className="fx-field">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
        <span className="fx-field-label" style={{ margin: 0 }}>{t(`${i18nPrefix}.color_label`)}</span>
        <div className="fx-mode-toggle" role="tablist" aria-label={t(`${i18nPrefix}.color_mode_aria`)}>
          <button
            type="button"
            role="tab"
            aria-selected={mode === 'solid'}
            className={'fx-mode-tab' + (mode === 'solid' ? ' fx-mode-tab-active' : '')}
            onClick={() => onModeChange('solid')}
          >
            <Icon d={I.solid} size={11} /> {t(`${i18nPrefix}.color_solid`)}
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={mode === 'gradient'}
            className={'fx-mode-tab' + (mode === 'gradient' ? ' fx-mode-tab-active' : '')}
            onClick={() => onModeChange('gradient')}
          >
            <Icon d={I.gradient} size={11} /> {t(`${i18nPrefix}.color_gradient`)}
          </button>
        </div>
      </div>

      {mode === 'solid' ? (
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
          {DEFAULT_ENTITY_COLORS.map((c) => (
            <button
              key={c}
              type="button"
              onClick={() => onSolidChange(c)}
              aria-label={t('common.color_swatch_aria', { c })}
              style={{
                width: 26,
                height: 26,
                borderRadius: 8,
                background: c,
                border: c === solid ? '2px solid var(--fx-ink)' : '1px solid var(--fx-border)',
                cursor: 'pointer',
              }}
            />
          ))}
          <input
            type="color"
            value={solid}
            onChange={(e) => onSolidChange(e.target.value)}
            style={{
              width: 36,
              height: 28,
              border: 0,
              background: 'transparent',
              cursor: 'pointer',
            }}
            aria-label={t(`${i18nPrefix}.custom_color_aria`)}
          />
        </div>
      ) : (
        <GradientPicker from={gradFrom} to={gradTo} onChange={onGradientChange} />
      )}
    </div>
  )
}
