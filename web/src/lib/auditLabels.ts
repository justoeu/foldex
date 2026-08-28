import type { TFunction } from 'i18next'

/**
 * The translated name of an action.
 *
 * The raw identifier is the fallback, so an action added server-side renders as
 * its own id rather than a blank cell. The vocabulary is closed and the server
 * owns it (ADR-46), so this is the one place the two representations meet.
 */
export function actionLabel(t: TFunction, action: string): string {
  return t(`admin.action_${action.replace(/\./g, '_')}`, action)
}
