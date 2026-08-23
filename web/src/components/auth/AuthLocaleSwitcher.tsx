import { useTranslation } from 'react-i18next'
import { SUPPORTED_LOCALES } from '../../i18n'
import { useLocaleChoice } from '../../i18n/useLocaleChoice'

/**
 * The language switcher every auth screen carries, as a flat row of flags.
 *
 * A row rather than the topbar's dropdown because of who reads it: someone on
 * the login or reset screen may be looking at a language they do not speak, and
 * a menu labelled "Language" in that language is a control they have to guess
 * at before they can open it. Flags are recognisable without reading anything,
 * and with three locales the whole set fits inline — no disclosure step at all.
 *
 * The flag is decorative and the CODE is the label: a flag is a country, not a
 * language, and several platforms (Windows, notably) render regional-indicator
 * pairs as bare letters. So each button is named by its language and its code
 * stays visible beside the glyph, which is also what keeps the control usable
 * when the emoji does not draw.
 */
export function AuthLocaleSwitcher() {
  const { t } = useTranslation()
  const { current, choose } = useLocaleChoice()

  return (
    <div className="fx-auth-locales" role="group" aria-label={t('topbar.language')}>
      {SUPPORTED_LOCALES.map((l) => {
        const active = l.code === current.code
        return (
          <button
            key={l.code}
            type="button"
            className="fx-auth-locale"
            // aria-pressed, not aria-current: this is a set of toggles where
            // exactly one is on, and a screen reader announces the state of the
            // unselected ones too — so the user hears which language they are
            // NOT in without having to move through the whole row.
            aria-pressed={active}
            aria-label={l.label}
            onClick={() => choose(l.code)}
          >
            <span className="fx-auth-locale-flag" aria-hidden="true">
              {l.flag}
            </span>
            <span className="fx-auth-locale-code" aria-hidden="true">
              {l.code.toUpperCase()}
            </span>
          </button>
        )
      })}
    </div>
  )
}
