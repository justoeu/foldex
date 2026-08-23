import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import type { Availability, AvailabilityReason } from '../hooks/useAvailability'

/** State → [tone, icon, copy key]. A lookup rather than the ternary chain this
 *  started as: `state` and `reason` are two independent dimensions, and
 *  collapsing them into one expression is what let `reason: 'empty'` fall into
 *  the shape arm by accident of the final `else`. */
const BY_STATE = {
  checking: ['wait', null, 'common.avail_checking'],
  error: ['wait', null, 'common.avail_error'],
  free: ['ok', I.check, 'common.avail_free'],
} as const

const BY_REASON: Record<AvailabilityReason, string> = {
  taken: 'common.avail_taken',
  reserved: 'common.avail_reserved',
  shape: 'common.avail_shape',
  empty: 'common.avail_shape',
  pending: 'common.avail_pending',
}

/**
 * The line under an identifier field: what the server said about the value
 * being typed.
 *
 * It says WHY, never just "unavailable" — a reserved name and a taken one need
 * different fixes, and a malformed one needs neither. `idle` renders nothing at
 * all rather than a placeholder, so the row does not jump as the user types.
 *
 * `role="status"` and not `alert`: this narrates a field the user is currently
 * looking at, and interrupting a screen reader on every debounce would make the
 * field unusable for exactly the people who cannot see the check mark.
 *
 * Lives at the components root, not under `account/`: the admin's create-user
 * dialog uses it too, and its copy is under `common.` for the same reason.
 */
export function AvailabilityHint({
  result,
  shapeText,
}: {
  result: Availability
  /** Overrides the generic shape message where the field can state its own
   *  rule. A refusal blocks the save, so the specific message the save path
   *  would have shown is otherwise unreachable. */
  shapeText?: string
}) {
  const { t } = useTranslation()
  if (result.state === 'idle') return null

  const [tone, icon, key] =
    result.state === 'refused'
      ? (['bad', I.x, BY_REASON[result.reason]] as const)
      : result.state === 'warn'
        ? (['wait', I.alert, BY_REASON[result.reason]] as const)
        : BY_STATE[result.state]

  const text =
    shapeText && key === 'common.avail_shape' ? shapeText : t(key)

  return (
    <span className={`fx-avail fx-avail-${tone}`} role="status">
      {icon && <Icon d={icon} size={13} />}
      {text}
    </span>
  )
}
