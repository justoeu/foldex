import { useId, useState, type InputHTMLAttributes } from 'react'
import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'

type Props = Omit<InputHTMLAttributes<HTMLInputElement>, 'type'> & {
  /**
   * The input's own class, forwarded verbatim — `fx-auth-input` on the auth
   * screens, `fx-input` where the class sits on the input itself.
   *
   * There is deliberately NO default. Eight call sites put `.fx-input` on a
   * WRAPPER div and nest a bare input; a default would draw a second bordered
   * 42px pill inside the first one.
   */
  className?: string
}

/**
 * A password field with a reveal toggle.
 *
 * One component rather than an eye bolted onto each of the twenty-one password
 * inputs, because the details that make it safe are easy to get wrong once and
 * impossible to notice:
 *
 *  - `type="button"` — inside a form the default is `submit`, so the first
 *    click on the eye would post the login form with whatever is typed.
 *  - Revealed text suppresses spell-check and editor extensions. A `type="text"`
 *    field is fair game for Chrome's Enhanced Spell Check and Grammarly, which
 *    SEND its contents to a remote service, and mobile keyboards learn it into
 *    the personal dictionary. Behind these fields sit the master recovery
 *    password and folder unlock passwords.
 *  - State is per-field and always starts hidden. A revealed password must not
 *    survive a remount, which is exactly what a persisted preference would do
 *    on a shared screen.
 *
 * The toggle IS in the tab order. Keeping it out was tried and reverted: a
 * reveal a keyboard-only user cannot reach withholds it from the people most
 * likely to need it to check a long password they cannot see.
 *
 * `aria-pressed` carries the state and the label says what the NEXT click does,
 * so a screen reader user is told both. The field keeps whatever `autoComplete`
 * the caller passes; toggling `type` does not disturb a password manager,
 * because managers key on the name/autocomplete pair, not the current type.
 */
export function PasswordInput({ className, ...props }: Props) {
  const { t } = useTranslation()
  const [shown, setShown] = useState(false)
  const fallbackId = useId()
  const id = props.id ?? fallbackId

  return (
    <div className="fx-pwfield">
      <input
        {...props}
        id={id}
        className={className}
        type={shown ? 'text' : 'password'}
        // Revealing turns the field into `type="text"`, and a text field is fair
        // game for spell-checkers: Chrome's Enhanced Spell Check and editor
        // extensions SEND its contents to a remote service, and mobile keyboards
        // learn it into the personal dictionary. A show-password toggle is the
        // canonical trigger for that, and the values behind these twenty-one
        // fields are the worst possible set — the master recovery password,
        // folder unlock passwords, the 2FA step-up password. Suppressed here
        // rather than per call site, because a field that forgets is silent.
        spellCheck={false}
        autoCorrect="off"
        autoCapitalize="off"
        data-gramm="false"
        data-gramm_editor="false"
        data-enable-grammarly="false"
      />
      <button
        type="button"
        className="fx-pweye"
        aria-controls={id}
        aria-pressed={shown}
        aria-label={shown ? t('auth.password_hide') : t('auth.password_show')}
        onClick={() => setShown((v) => !v)}
      >
        <Icon d={shown ? I.eyeOff : I.eye} size={15} />
      </button>
    </div>
  )
}
