import { useEffect, useState } from 'react'

/**
 * A tiny one-line toast, bottom-center, for clipboard flows.
 *
 * The incognito copy needs to TELL the user what to do next (the browser
 * cannot open an incognito window, so the paste is on them) — without a
 * message the click looks like it did nothing. Event-based rather than a
 * context provider because every trigger is a sibling of every other trigger;
 * a CustomEvent needs no tree plumbing and one host covers them all.
 */

const COPY_TOAST_EVENT = 'fx-copy-toast'

export function showCopyToast(message: string): void {
  window.dispatchEvent(new CustomEvent(COPY_TOAST_EVENT, { detail: message }))
}

export function CopyToastHost() {
  const [message, setMessage] = useState('')

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined
    const onToast = (e: Event) => {
      setMessage((e as CustomEvent<string>).detail)
      clearTimeout(timer)
      timer = setTimeout(() => setMessage(''), 3200)
    }
    window.addEventListener(COPY_TOAST_EVENT, onToast)
    return () => {
      window.removeEventListener(COPY_TOAST_EVENT, onToast)
      clearTimeout(timer)
    }
  }, [])

  if (!message) return null
  return (
    <div className="fx-copytoast" role="status" aria-live="polite">
      {message}
    </div>
  )
}
