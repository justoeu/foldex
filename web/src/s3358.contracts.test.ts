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

/** Nested conditional expression: `cond ? a : cond2 ? b`. Optional `?:` types are not matches. */
export function nestedTernaryHits(src: string): string[] {
  const cleaned = src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/\/\/.*$/gm, '')
    .replace(/(['"`])(?:\\.|(?!\1)[\s\S])*\1/g, '""')
  const hits: string[] = []
  const re = /\?(?![:.\d])[^?{};]{0,220}:\s*[^?{};]{0,160}\?/g
  let m: RegExpExecArray | null
  while ((m = re.exec(cleaned))) {
    const snippet = m[0].replace(/\s+/g, ' ').trim()
    if (snippet.includes('?:')) continue
    hits.push(snippet.slice(0, 160))
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
})
