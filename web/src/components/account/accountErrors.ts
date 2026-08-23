import { apiErrorCode as errCode } from '../../lib/apiError'
import { MIN_PASSWORD_LEN } from '../../auth/types'

type Translate = (key: string, opts?: Record<string, unknown>) => string

/**
 * Maps a server error code to copy, for every credential action on the account
 * page.
 *
 * One mapper rather than a switch per component: the password, the Google link
 * and the unlink all reach the same handlers and can answer the same codes, and
 * a second copy drifts on whichever surface nobody re-tests. `password_too_short`
 * carries the count so the message states the floor instead of implying one.
 */
export function accountErrorMessage(err: unknown, t: Translate): string {
  switch (errCode(err)) {
    case 'invalid_credentials':
      return t('account.wrong_password')
    case 'password_required':
      // The 409 the server answers when unlinking would strip the last
      // credential. The UI hides that button, so reaching this means a password
      // was removed in another tab.
      return t('account.password_required_first')
    case 'password_exists':
      return t('account.password_already_set')
    case 'invalid_code':
      return t('auth_errors.invalid_code')
    case 'password_too_short':
      return t('auth_errors.password_too_short', { count: MIN_PASSWORD_LEN })
    case 'password_too_long':
      return t('auth_errors.password_too_long')
    case 'not_linked':
      return t('account.google_not_connected')
    case 'too_many_attempts':
      return t('auth_errors.too_many_attempts')
    default:
      return t('auth_errors.generic')
  }
}
