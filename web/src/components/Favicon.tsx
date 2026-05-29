import { useEffect, useState } from 'react'
import { safeImageUrl } from '../lib/url'
import type { Link } from '../api/types'

type Props = {
  link: Pick<Link, 'url' | 'title' | 'favicon_url'>
  size?: number
}

// Renders the real favicon when the backend resolved one. Falls back to a
// gradient tile with the first letter of the hostname (the design uses
// per-host gradients; we generate one deterministically here).
//
// Also falls back to the letter tile when the favicon URL fails to load
// at runtime (404, CORS block, etc.). Without this, the browser would
// render its built-in broken-image icon — visually jarring, defeats the
// whole point of the favicon row.
export function Favicon({ link, size = 32 }: Props) {
  const host = safeHost(link.url)
  const letter = (host[0] ?? link.title[0] ?? '?').toUpperCase()
  const { bg, fg } = paletteFor(host)
  const [errored, setErrored] = useState(false)

  // Reset the error flag when the URL changes (e.g. preview worker re-runs
  // and stamps a new favicon_url on the link).
  useEffect(() => {
    setErrored(false)
  }, [link.favicon_url])

  const safeSrc = safeImageUrl(link.favicon_url)
  const showImg = !!safeSrc && !errored

  return (
    <div
      className="fx-favicon"
      style={{
        width: size,
        height: size,
        background: showImg ? 'transparent' : bg,
        color: fg,
        fontSize: Math.round(size * 0.46),
      }}
    >
      {showImg ? (
        <img
          src={safeSrc}
          alt=""
          referrerPolicy="no-referrer"
          loading="lazy"
          decoding="async"
          width={size}
          height={size}
          onError={() => setErrored(true)}
          style={{ width: size, height: size, borderRadius: 8, objectFit: 'cover' }}
        />
      ) : (
        letter
      )}
    </div>
  )
}

function safeHost(u: string) {
  try {
    return new URL(u).hostname.replace(/^www\./, '')
  } catch {
    return u
  }
}

// Cheap deterministic palette so each host gets a stable favicon color
// when no real one was fetched.
const PALETTES = [
  { bg: 'linear-gradient(135deg, #6366F1, #4F46E5)', fg: '#fff' },
  { bg: 'linear-gradient(135deg, #8B5CF6, #A78BFA)', fg: '#fff' },
  { bg: 'linear-gradient(135deg, #0EA5E9, #38BDF8)', fg: '#fff' },
  { bg: 'linear-gradient(135deg, #EC4899, #F472B6)', fg: '#fff' },
  { bg: 'linear-gradient(135deg, #F59E0B, #FBBF24)', fg: '#1A1530' },
  { bg: 'linear-gradient(135deg, #10B981, #34D399)', fg: '#1A1530' },
  { bg: 'linear-gradient(135deg, #FFE600, #FFB800)', fg: '#1A1530' },
  { bg: 'linear-gradient(135deg, #64748B, #94A3B8)', fg: '#fff' },
]

function paletteFor(seed: string) {
  let h = 0
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) >>> 0
  return PALETTES[h % PALETTES.length]
}
