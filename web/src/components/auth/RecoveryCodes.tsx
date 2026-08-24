import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useCopy } from '../../hooks/useCopy'

/**
 * The one and only display of a freshly minted recovery sheet.
 *
 * The server stores only a server-keyed digest of each code, so this really is the sole
 * opportunity — which is why the continue button is gated behind an explicit
 * acknowledgement rather than being a plain "OK". A user who clicks through
 * without copying them has no second chance, and the checkbox is what turns
 * "we warned you" into "you confirmed".
 */
export function RecoveryCodes({ codes, onDone }: { codes: string[]; onDone: () => void }) {
  const { t } = useTranslation()
  const [acknowledged, setAcknowledged] = useState(false)
  const { copied, copy } = useCopy()
  const sheet = codes.join('\n')

  function download() {
    const blob = new Blob([codes.join('\n') + '\n'], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'foldex-recovery-codes.txt'
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <div className="fx-auth-form">
      {/* Page translation uploads visible text to a third party; every item
          below is a credential. See CreateUserDialog for the full note. */}
      <ul className="fx-auth-codes" data-testid="recovery-codes" translate="no">
        {codes.map((c) => (
          <li key={c} className="fx-auth-code">
            {c}
          </li>
        ))}
      </ul>

      <div className="fx-auth-alt">
        <button type="button" className="fx-auth-link" onClick={() => void copy(sheet)}>
          {copied(sheet) ? t('twofa.copied') : t('twofa.copy')}
        </button>
        <button type="button" className="fx-auth-link" onClick={download}>
          {t('twofa.download')}
        </button>
      </div>

      <label className="fx-auth-check">
        <input
          type="checkbox"
          checked={acknowledged}
          onChange={(e) => setAcknowledged(e.target.checked)}
        />
        <span>{t('twofa.codes_ack')}</span>
      </label>

      <button
        type="button"
        className="fx-auth-submit"
        disabled={!acknowledged}
        onClick={onDone}
      >
        {t('twofa.codes_continue')}
      </button>
    </div>
  )
}
