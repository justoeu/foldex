import type { TFunction } from 'i18next'

/**
 * The translated name of an action.
 *
 * The raw identifier is the fallback, so an action added server-side renders as
 * its own id rather than a blank cell. The vocabulary is closed and the server
 * owns it (ADR-46), so this is the one place the two representations meet.
 */
export function actionLabel(t: TFunction, action: string): string {
  return t(`admin.action_${action.replaceAll('.', '_')}`, action)
}

/** Coarse action families for visual classification (dot colors, filters). */
export type AuditActionCategory = 'create' | 'click' | 'login' | 'edit' | 'delete' | 'other'

export function actionCategory(action: string): AuditActionCategory {
  // Canonical vocabulary is <entity>.<verb> (link.created); legacy rows used
  // bare verb prefixes, so both shapes classify the same way.
  if (action.endsWith('.created') || action.startsWith('create')) return 'create'
  if (action === 'link.clicked') return 'click'
  if (action.startsWith('login.')) return 'login'
  if (action.endsWith('.updated') || action.startsWith('update') || action.startsWith('edit')) return 'edit'
  if (
    action.endsWith('.deleted') ||
    action.endsWith('.revoked') ||
    action.startsWith('delete')
  ) {
    return 'delete'
  }
  return 'other'
}
