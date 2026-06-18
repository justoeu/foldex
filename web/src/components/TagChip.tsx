import { memo, type CSSProperties } from 'react'
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

// memo guards re-render storms: every LinkCard/Row mounts up to 3 TagChips
// (cards) or 2 (compact) and a 200-card grid otherwise re-renders 600 chips
// on every parent state change. Props are stable: tag is a cached reference,
// onClick/active are typically literals or useCallback'd.
export const TagChip = memo(TagChipImpl)
TagChip.displayName = 'TagChip'

function TagChipImpl({ tag, onClick, active, closable, onClose }: Props) {
  const { t } = useTranslation()
  const cls = 'fx-chip' + (active ? ' fx-chip-active' : '')
  // CSS custom properties aren't in the React.CSSProperties index type;
  // the canonical workaround is to cast the whole object via CSSProperties
  // rather than the property key. Same trick used at every other site that
  // assigns a `--fx-*` var via inline style.
  const style = { '--chip-c': primaryColor(tag.color) } as CSSProperties
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
