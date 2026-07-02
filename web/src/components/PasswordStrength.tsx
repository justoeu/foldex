import { useTranslation } from 'react-i18next'
import { Icon, I } from './icons'
import { passwordChecks, passwordScore } from '../lib/passwordStrength'

// The complexity matrix + strength bar shown under a password field. Guidance
// only — it never blocks submit on its own (the caller decides), it just makes
// the criteria and current strength visible.
export function PasswordStrength({ value }: { value: string }) {
  const { t } = useTranslation()
  if (!value) return null

  const score = passwordScore(value)
  const checks = passwordChecks(value)
  const labels = ['', t('password_strength.weak'), t('password_strength.fair'), t('password_strength.good'), t('password_strength.strong')]
  const colors = ['', '#EF4444', '#F59E0B', '#EAB308', '#10B981']

  const criteria: { ok: boolean; label: string }[] = [
    { ok: checks.length, label: t('password_strength.rule_length') },
    { ok: checks.longer, label: t('password_strength.rule_longer') },
    { ok: checks.lower && checks.upper, label: t('password_strength.rule_case') },
    { ok: checks.digit, label: t('password_strength.rule_digit') },
    { ok: checks.symbol, label: t('password_strength.rule_symbol') },
  ]

  return (
    <div className="fx-pwstrength" aria-live="polite">
      <div className="fx-pwstrength-bars">
        {[1, 2, 3, 4].map((seg) => (
          <span
            key={seg}
            className="fx-pwstrength-seg"
            style={{ background: seg <= score ? colors[score] : 'var(--fx-border)' }}
          />
        ))}
        <span className="fx-pwstrength-label" style={{ color: colors[score] }}>
          {labels[score]}
        </span>
      </div>
      <ul className="fx-pwstrength-rules">
        {criteria.map((c) => (
          <li key={c.label} className={c.ok ? 'fx-pwrule-ok' : ''}>
            <Icon d={c.ok ? I.check : I.x} size={11} /> {c.label}
          </li>
        ))}
      </ul>
    </div>
  )
}
