import { render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { usePasteUrl } from './usePasteUrl'

function Probe({ onUrl }: { onUrl: (u: string) => void }) {
  usePasteUrl(onUrl)
  return <div data-testid="probe" />
}

// jsdom doesn't ship ClipboardEvent as a global, so we dispatch a plain
// bubbling `paste` event from the target node. dispatchEvent naturally
// sets `e.target` for us — the hook only reads `e.target` and
// `e.clipboardData.getData('text/plain')`, so monkey-patching just the
// clipboardData getter is enough.
function paste(target: EventTarget, text: string) {
  const ev = new Event('paste', { bubbles: true, cancelable: true })
  Object.defineProperty(ev, 'clipboardData', {
    value: { getData: () => text },
  })
  target.dispatchEvent(ev)
  return ev
}

describe('usePasteUrl', () => {
  it('fires onUrl with a URL when nothing else is focused', () => {
    const onUrl = vi.fn()
    render(<Probe onUrl={onUrl} />)
    paste(document.body, 'https://example.com/x')
    expect(onUrl).toHaveBeenCalledWith('https://example.com/x')
  })

  it('is a no-op when the paste target is an INPUT', () => {
    const onUrl = vi.fn()
    render(<Probe onUrl={onUrl} />)
    const input = document.createElement('input')
    document.body.appendChild(input)
    paste(input, 'https://example.com')
    expect(onUrl).not.toHaveBeenCalled()
  })

  it('is a no-op when the paste target is a TEXTAREA', () => {
    const onUrl = vi.fn()
    render(<Probe onUrl={onUrl} />)
    const ta = document.createElement('textarea')
    document.body.appendChild(ta)
    paste(ta, 'https://example.com')
    expect(onUrl).not.toHaveBeenCalled()
  })

  it('is a no-op when contentEditable target is focused', () => {
    const onUrl = vi.fn()
    render(<Probe onUrl={onUrl} />)
    const div = document.createElement('div')
    // jsdom's HTMLElement.isContentEditable getter doesn't follow the
    // contentEditable attribute reliably — it can return false even after
    // `div.contentEditable = 'true'`. Override the getter directly so the
    // test exercises the hook's `el.isContentEditable` branch deterministically.
    Object.defineProperty(div, 'isContentEditable', { value: true })
    document.body.appendChild(div)
    paste(div, 'https://example.com')
    expect(onUrl).not.toHaveBeenCalled()
  })

  it('is a no-op when an overlay (.fx-overlay) is mounted', () => {
    const onUrl = vi.fn()
    render(<Probe onUrl={onUrl} />)
    const overlay = document.createElement('div')
    overlay.className = 'fx-overlay'
    document.body.appendChild(overlay)
    paste(document.body, 'https://example.com')
    expect(onUrl).not.toHaveBeenCalled()
    overlay.remove()
  })

  it('ignores non-URL clipboard payloads', () => {
    const onUrl = vi.fn()
    render(<Probe onUrl={onUrl} />)
    paste(document.body, 'just some text')
    expect(onUrl).not.toHaveBeenCalled()
  })
})
