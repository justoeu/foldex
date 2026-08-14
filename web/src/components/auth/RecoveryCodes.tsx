import { useState } from 'react'
import { useTranslation } from 'react-i18next'

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
  const [copied, setCopied] = useState(false)

  async function copy() {
    try {
      await navigator.clipboard.writeText(codes.join('\n'))
      setCopied(true)
    } catch {
      // Clipboard access can be denied or absent (insecure context, older
      // browser). The codes are on screen either way, so failing quietly beats
      // an error the user can do nothing about.
    }
  }

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
      <ul className="fx-auth-codes" data-testid="recovery-codes">
        {codes.map((c) => (
          <li key={c} className="fx-auth-code">
            {c}
          </li>
        ))}
      </ul>

      <div className="fx-auth-alt">
        <button type="button" className="fx-auth-link" onClick={() => void copy()}>
          {copied ? t('twofa.copied') : t('twofa.copy')}
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
