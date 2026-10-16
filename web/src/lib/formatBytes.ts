const UNITS = ['B', 'KB', 'MB', 'GB', 'TB'] as const

export function formatBytes(b: number): string {
  if (b < 1024) return `${b} ${UNITS[0]}`
  let n = b / 1024
  let i = 1
  while (n >= 1024 && i < UNITS.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(n >= 10 ? 0 : 1)} ${UNITS[i]}`
}
