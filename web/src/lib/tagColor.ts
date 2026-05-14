// Tag colors are stored as a single CSS string — either a solid like "#6366F1"
// or a gradient like "linear-gradient(135deg, #6366F1, #EC4899)". Anything that
// uses `color-mix` or sets `color: …` (chip text/border) needs a SOLID value,
// so we extract the first stop. Backgrounds work with the original string.

const GRADIENT_ANGLE = 135

export function isGradient(color: string): boolean {
  return /^(linear|radial|conic)-gradient\(/i.test(color)
}

export function makeGradient(from: string, to: string): string {
  return `linear-gradient(${GRADIENT_ANGLE}deg, ${from}, ${to})`
}

// Returns the first color stop of a gradient, or the input itself if solid.
// Used to drive --chip-c so text/border still render correctly.
export function primaryColor(color: string): string {
  if (!isGradient(color)) return color
  const m = color.match(/#[0-9a-fA-F]{3,8}|rgba?\([^)]+\)|hsla?\([^)]+\)/)
  return m?.[0] ?? '#6366F1'
}

// Pulls (from, to) out of a stored gradient string so the dialog can pre-fill.
// Falls back to (color, color) for solids.
export function parseGradient(color: string): { from: string; to: string } {
  if (!isGradient(color)) return { from: color, to: color }
  const stops = color.match(/#[0-9a-fA-F]{3,8}|rgba?\([^)]+\)|hsla?\([^)]+\)/g) ?? []
  return { from: stops[0] ?? '#6366F1', to: stops[1] ?? stops[0] ?? '#EC4899' }
}

// ──────────────────────────────────────────────────────────────────────────
// HSL conversion — used by GradientPicker's hue-spectrum bar so dragging a
// thumb shifts only the HUE component of the stop, preserving the user's
// saturation/lightness choice (a deep navy stays deep when you rotate hue).

export type HSL = { h: number; s: number; l: number }

// Parse "#rrggbb" or "#rgb" into HSL. Returns gray (h=0,s=0,l=50) when the
// input isn't a valid hex (gradients, named colors, etc.); the caller can
// still drag to set a hue, just from a neutral starting point.
export function hexToHsl(hex: string): HSL {
  const clean = hex.trim().replace(/^#/, '')
  let r = 0
  let g = 0
  let b = 0
  if (clean.length === 3) {
    r = parseInt(clean[0] + clean[0], 16)
    g = parseInt(clean[1] + clean[1], 16)
    b = parseInt(clean[2] + clean[2], 16)
  } else if (clean.length === 6) {
    r = parseInt(clean.slice(0, 2), 16)
    g = parseInt(clean.slice(2, 4), 16)
    b = parseInt(clean.slice(4, 6), 16)
  } else {
    return { h: 0, s: 0, l: 50 }
  }
  if (Number.isNaN(r) || Number.isNaN(g) || Number.isNaN(b)) {
    return { h: 0, s: 0, l: 50 }
  }
  const rn = r / 255
  const gn = g / 255
  const bn = b / 255
  const max = Math.max(rn, gn, bn)
  const min = Math.min(rn, gn, bn)
  const l = (max + min) / 2
  let h = 0
  let s = 0
  if (max !== min) {
    const d = max - min
    s = l > 0.5 ? d / (2 - max - min) : d / (max + min)
    switch (max) {
      case rn:
        h = (gn - bn) / d + (gn < bn ? 6 : 0)
        break
      case gn:
        h = (bn - rn) / d + 2
        break
      case bn:
        h = (rn - gn) / d + 4
        break
    }
    h *= 60
  }
  return { h, s: s * 100, l: l * 100 }
}

export function hslToHex(h: number, s: number, l: number): string {
  const sn = Math.max(0, Math.min(100, s)) / 100
  const ln = Math.max(0, Math.min(100, l)) / 100
  const hn = ((h % 360) + 360) % 360
  const c = (1 - Math.abs(2 * ln - 1)) * sn
  const x = c * (1 - Math.abs(((hn / 60) % 2) - 1))
  const m = ln - c / 2
  let r1 = 0
  let g1 = 0
  let b1 = 0
  if (hn < 60) { r1 = c; g1 = x; b1 = 0 }
  else if (hn < 120) { r1 = x; g1 = c; b1 = 0 }
  else if (hn < 180) { r1 = 0; g1 = c; b1 = x }
  else if (hn < 240) { r1 = 0; g1 = x; b1 = c }
  else if (hn < 300) { r1 = x; g1 = 0; b1 = c }
  else { r1 = c; g1 = 0; b1 = x }
  const toHex = (v: number) => {
    const n = Math.round((v + m) * 255)
    return n.toString(16).padStart(2, '0')
  }
  return `#${toHex(r1)}${toHex(g1)}${toHex(b1)}`
}
