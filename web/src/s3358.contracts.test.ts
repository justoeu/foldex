import { describe, expect, it } from 'vitest'
import availabilityHint from './components/AvailabilityHint.tsx?raw'
import usernameRow from './components/account/UsernameRow.tsx?raw'
import emailRow from './components/account/EmailRow.tsx?raw'
import sectionCard from './components/account/SectionCard.tsx?raw'
import useAvailability from './hooks/useAvailability.ts?raw'
import folderDialog from './components/FolderDialog.tsx?raw'
import folderPicker from './components/FolderPicker.tsx?raw'
import folderCard from './components/FolderCard.tsx?raw'
import conflictModePicker from './components/ConflictModePicker.tsx?raw'
import backupRestore from './components/BackupRestoreDialog.tsx?raw'
import backupSection from './components/admin/BackupSection.tsx?raw'
import backupSchedule from './components/admin/BackupScheduleEditor.tsx?raw'
import auditSignals from './components/admin/AuditSignals.tsx?raw'
import statsPage from './pages/StatsPage.tsx?raw'
import linkDialog from './components/LinkDialog.tsx?raw'
import importPreview from './components/ImportPreviewDialog.tsx?raw'
import sw from './sw.ts?raw'

/** Nested `cond ? a : b` in either arm. Skips `?.`, `??`, and `?:`. */
export function nestedTernaryHits(src: string): string[] {
  const cleaned = src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/\/\/.*$/gm, '')
    .replace(/(['"`])(?:\\.|(?!\1)[\s\S])*\1/g, '""')
  const hits: string[] = []
  const falseArm = /\?[^\n?:]{1,80}:[^\n?:]{1,80}\?[^\n?:]{0,80}:/g
  let m: RegExpExecArray | null
  while ((m = falseArm.exec(cleaned))) {
    hits.push(m[0].replace(/\s+/g, ' ').trim().slice(0, 160))
  }
  type Frame = { kind: 'paren' | 'brace' | 'bracket' | 'ternary'; i: number }
  const stack: Frame[] = []
  let i = 0
  while (i < cleaned.length) {
    const ch = cleaned[i]
    const nxt = cleaned[i + 1]
    if (ch === '?' && (nxt === '.' || nxt === '?' || nxt === ':')) {
      i += 2
      continue
    }
    if (ch === '(') { stack.push({ kind: 'paren', i }); i++; continue }
    if (ch === '{') { stack.push({ kind: 'brace', i }); i++; continue }
    if (ch === '[') { stack.push({ kind: 'bracket', i }); i++; continue }
    if (ch === ')' || ch === '}' || ch === ']') {
      const want = ch === ')' ? 'paren' : ch === '}' ? 'brace' : 'bracket'
      while (stack.length > 0 && stack[stack.length - 1].kind !== want) stack.pop()
      if (stack.length > 0) stack.pop()
      i++
      continue
    }
    if (ch === '?') {
      if (stack.some((f) => f.kind === 'ternary')) {
        hits.push(cleaned.slice(Math.max(0, i - 24), i + 48).replace(/\s+/g, ' ').trim().slice(0, 160))
      }
      stack.push({ kind: 'ternary', i })
      i++
      continue
    }
    if (ch === ':') {
      if (stack.at(-1)?.kind === 'ternary') stack.pop()
      i++
      continue
    }
    i++
  }
  return hits
}

const accountFiles = {
  'AvailabilityHint.tsx': availabilityHint,
  'UsernameRow.tsx': usernameRow,
  'EmailRow.tsx': emailRow,
  'SectionCard.tsx': sectionCard,
  'useAvailability.ts': useAvailability,
}

const remainingFiles = {
  'LinkDialog.tsx': linkDialog,
  'ImportPreviewDialog.tsx': importPreview,
  'sw.ts': sw,
}

const auditStatsFiles = {
  'AuditSignals.tsx': auditSignals,
  'StatsPage.tsx': statsPage,
}

const backupFiles = {
  'ConflictModePicker.tsx': conflictModePicker,
  'BackupRestoreDialog.tsx': backupRestore,
  'BackupSection.tsx': backupSection,
  'BackupScheduleEditor.tsx': backupSchedule,
}

const folderFiles = {
  'FolderDialog.tsx': folderDialog,
  'FolderPicker.tsx': folderPicker,
  'FolderCard.tsx': folderCard,
}

describe('S3358 contracts', () => {
  it('detects false-arm nesting and ignores a single ternary', () => {
    expect(nestedTernaryHits('a ? b : c ? d : e').length).toBeGreaterThan(0)
    expect(nestedTernaryHits('a ? b : c')).toEqual([])
    expect(nestedTernaryHits('a ? (b ? c : d) : e').length).toBeGreaterThan(0)
  })

  it('account files have no nested ternaries', () => {
    for (const [name, src] of Object.entries(accountFiles)) {
      expect(nestedTernaryHits(src), name).toEqual([])
    }
  })

  it('folder files have no nested ternaries', () => {
    for (const [name, src] of Object.entries(folderFiles)) {
      expect(nestedTernaryHits(src), name).toEqual([])
    }
  })

  it('backup files have no nested ternaries', () => {
    for (const [name, src] of Object.entries(backupFiles)) {
      expect(nestedTernaryHits(src), name).toEqual([])
    }
  })

  it('audit and stats files have no nested ternaries', () => {
    for (const [name, src] of Object.entries(auditStatsFiles)) {
      expect(nestedTernaryHits(src), name).toEqual([])
    }
  })

  it('remaining dialog and SW files have no nested ternaries', () => {
    for (const [name, src] of Object.entries(remainingFiles)) {
      expect(nestedTernaryHits(src), name).toEqual([])
    }
  })

  it('production web/src has no nested ternaries', () => {
    const modules = import.meta.glob('./**/*.{ts,tsx}', { query: '?raw', eager: true, import: 'default' }) as Record<string, string>
    const leftover: string[] = []
    for (const [path, src] of Object.entries(modules)) {
      if (path.includes('.test.') || path.includes('/test/')) continue
      for (const hit of nestedTernaryHits(src)) leftover.push(`${path}: ${hit}`)
    }
    expect(leftover).toEqual([])
  })
})
