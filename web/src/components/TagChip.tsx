import { useTranslation } from 'react-i18next'
import { isGradient, primaryColor } from '../lib/tagColor'
import type { Tag } from '../api/types'

type Props = {
  tag: Pick<Tag, 'name' | 'color'>
  onClick?: () => void
  active?: boolean
  closable?: boolean
  onClose?: () => void
}

export function TagChip({ tag, onClick, active, closable, onClose }: Props) {
  const { t } = useTranslation()
  const cls = 'fx-chip' + (active ? ' fx-chip-active' : '')
  const style = { ['--chip-c' as never]: primaryColor(tag.color) }
  const dotStyle = isGradient(tag.color) ? { background: tag.color } : undefined

  const closeBtn = closable && (
    <span
      role="button"
      className="fx-chip-close"
      aria-label={t('common.remove_tag_aria', { name: tag.name })}
      onClick={(e) => {
        e.stopPropagation()
        onClose?.()
      }}
    >
      ×
    </span>
  )

  if (onClick || closable) {
    return (
      <button type="button" className={cls} style={style} onClick={onClick}>
        <span className="fx-chip-dot" style={dotStyle} />
        {tag.name}
        {closeBtn}
      </button>
    )
  }
  return (
    <span className={cls} style={style}>
      <span className="fx-chip-dot" style={dotStyle} />
      {tag.name}
    </span>
  )
}
