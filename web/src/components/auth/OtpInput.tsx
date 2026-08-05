import { useCallback, useEffect, useRef, useState, type ClipboardEvent, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'

export const OTP_LENGTH = 6

type Props = {
  value: string
  onChange: (next: string) => void
  /** Fired once when the field reaches OTP_LENGTH digits. */
  onComplete?: (code: string) => void
  disabled?: boolean
  autoFocus?: boolean
  /** Marks every cell invalid and clears the field, for a rejected code. */
  invalid?: boolean
}

/**
 * Six single-character cells behaving as one field.
 *
 * Rendered as separate inputs rather than one styled text box because that is
 * what users recognise as "the code from the app", and because it makes each
 * digit an independent focus target on a phone. The cost is that every keyboard
 * and paste behaviour a single input gives for free has to be rebuilt here —
 * which is exactly why this component carries the densest test file in the
 * auth surface.
 */
export function OtpInput({ value, onChange, onComplete, disabled, autoFocus, invalid }: Props) {
  const { t } = useTranslation()
  const refs = useRef<Array<HTMLInputElement | null>>([])
  // Guards onComplete against firing twice for the same code. Without it, the
  // final keystroke and a re-render triggered by the parent's own state update
  // both see a full value, and the form submits twice — which on a single-use
  // code means the second request always fails and shows an error over a login
  // that actually succeeded.
  const [completed, setCompleted] = useState('')

  const digits = value.padEnd(OTP_LENGTH, ' ').slice(0, OTP_LENGTH).split('')

  const focusCell = useCallback((i: number) => {
    const el = refs.current[Math.max(0, Math.min(OTP_LENGTH - 1, i))]
    el?.focus()
    el?.select()
  }, [])

  useEffect(() => {
    if (autoFocus) focusCell(0)
  }, [autoFocus, focusCell])

  useEffect(() => {
    if (value.length === OTP_LENGTH && completed !== value) {
      setCompleted(value)
      onComplete?.(value)
    }
    if (value.length < OTP_LENGTH && completed) setCompleted('')
  }, [value, completed, onComplete])

  function setDigits(next: string) {
    onChange(next.replace(/\D/g, '').slice(0, OTP_LENGTH))
  }

  function handleInput(index: number, raw: string) {
    const typed = raw.replace(/\D/g, '')
    if (!typed) return

    // Typing into a cell REPLACES that position and appends the rest, so that
    // holding a key or pasting into a middle cell behaves predictably instead
    // of interleaving with what is already there.
    const chars = value.split('')
    for (let i = 0; i < typed.length && index + i < OTP_LENGTH; i++) {
      chars[index + i] = typed[i]
    }
    setDigits(chars.join('').slice(0, OTP_LENGTH))
    focusCell(index + typed.length)
  }

  function handleKeyDown(index: number, e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Backspace') {
      e.preventDefault()
      if (value[index]) {
        // Clear from this cell onwards and STAY.
        //
        // Two behaviours were wrong before this. Deleting and jumping back in
        // one press makes correcting a single wrong digit impossible — the user
        // always loses the one before it too. And clearing ONLY this position
        // cannot work while `value` is a compact string: blanking a middle
        // character and rejoining shifts every later digit one cell to the
        // left, so the display silently stops matching what was typed.
        // Truncating is the honest resolution — the user is retyping from here
        // anyway — and it keeps `value` a plain prefix the parent can submit.
        setDigits(value.slice(0, index))
        return
      }
      setDigits(value.slice(0, Math.max(0, index - 1)))
      focusCell(index - 1)
      return
    }
    if (e.key === 'ArrowLeft') {
      e.preventDefault()
      focusCell(index - 1)
    }
    if (e.key === 'ArrowRight') {
      e.preventDefault()
      focusCell(index + 1)
    }
  }

  /**
   * Accepts a pasted code in whatever shape it arrives.
   *
   * Authenticator apps show "123 456", mail clients linkify "123-456", and
   * password managers paste "123456". Stripping non-digits handles all three;
   * refusing them would make the most common way of entering the code fail.
   */
  function handlePaste(e: ClipboardEvent<HTMLInputElement>) {
    const text = e.clipboardData.getData('text')
    if (!text) return
    e.preventDefault()
    const cleaned = text.replace(/\D/g, '').slice(0, OTP_LENGTH)
    if (!cleaned) return
    setDigits(cleaned)
    focusCell(cleaned.length)
  }

  return (
    <div
      className={'fx-auth-otp' + (invalid ? ' fx-auth-otp-invalid' : '')}
      role="group"
      aria-label={t('auth_otp.field_label')}
    >
      {digits.map((d, i) => (
        <input
          key={i}
          ref={(el) => {
            refs.current[i] = el
          }}
          className="fx-auth-otp-cell"
          // `text` with a numeric inputMode, not `number`: a number input shows
          // spinners, silently accepts "e" and "-", and drops leading zeros —
          // all fatal for a code where "012345" is a valid value.
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          maxLength={1}
          // ONLY the first cell advertises one-time-code. Safari fills every
          // input carrying this attribute with the SAME digit when it
          // autofills from Messages, so six annotated cells produce "111111".
          autoComplete={i === 0 ? 'one-time-code' : 'off'}
          aria-label={t('auth_otp.digit_label', { n: i + 1, total: OTP_LENGTH })}
          aria-invalid={invalid || undefined}
          disabled={disabled}
          value={d.trim()}
          onChange={(e) => handleInput(i, e.target.value)}
          onKeyDown={(e) => handleKeyDown(i, e)}
          onPaste={handlePaste}
          onFocus={(e) => e.currentTarget.select()}
        />
      ))}
    </div>
  )
}
