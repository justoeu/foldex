import { describe, expect, it } from 'vitest'
import backupSrc from './components/BackupRestoreDialog.tsx?raw'
import previewSrc from './components/ImportPreviewDialog.tsx?raw'
import previewHookSrc from './hooks/useImportPreview.ts?raw'
import pickerSrc from './components/ConflictModePicker.tsx?raw'
import fieldsSrc from './components/ColorModeFields.tsx?raw'
import gradSrc from './components/GradientPicker.tsx?raw'
import adminSrc from './api/admin.ts?raw'
import authSrc from './api/auth.ts?raw'
import folderPayloadSrc from './lib/folderDialogPayload.ts?raw'
import createUserSrc from './components/admin/CreateUserDialog.tsx?raw'
import sessionsSrc from './components/account/SessionsSection.tsx?raw'
import typesSrc from './auth/types.ts?raw'
import activitySrc from './components/account/ActivitySection.tsx?raw'
import homeSrc from './components/HomeView.tsx?raw'
import settingsSrc from './pages/SettingsPage.tsx?raw'
import linkDialogSrc from './components/LinkDialog.tsx?raw'
import tokensSrc from './api/tokens.ts?raw'
import noteControllerSrc from './components/useNoteDialogController.ts?raw'

describe('web deadcode contracts', () => {
  it('ConflictModePicker dual-encoding is shared', () => {
    expect(pickerSrc).toMatch(/var\(--fx-danger\)/)
    expect(pickerSrc).toMatch(/var\(--fx-accent\)/)
    expect(backupSrc).toMatch(/ConflictModePicker/)
    expect(previewSrc).toMatch(/ConflictModePicker/)
    expect(backupSrc).not.toMatch(/function ModeOption/)
    expect(previewSrc).not.toMatch(/function ModeOption/)
    expect(backupSrc).toMatch(/apiErrorMessage|apiErrorText/)
    expect(previewHookSrc).toMatch(/apiErrorMessage|apiErrorText/)
    expect(backupSrc).not.toMatch(/function extractErr/)
    expect(previewSrc).not.toMatch(/function extractErr/)
    expect(previewHookSrc).not.toMatch(/function extractErr/)
  })

  it('solid and gradient pickers share one swatch list', () => {
    expect(fieldsSrc).toMatch(/DEFAULT_ENTITY_COLORS/)
    expect(gradSrc).toMatch(/DEFAULT_ENTITY_COLORS/)
    expect(gradSrc).not.toMatch(/const DEFAULT_COLORS/)
  })

  it('createUser is typed like updateUser', () => {
    const create = adminSrc.slice(
      adminSrc.indexOf('export async function createUser'),
      adminSrc.indexOf('export async function deleteUser'),
    )
    const update = adminSrc.slice(
      adminSrc.indexOf('export async function updateUser'),
      adminSrc.indexOf('export async function createUser'),
    )
    expect(update).toMatch(/http\.patch<AuthUser>/)
    expect(create).toMatch(/http\.post<AuthUser>/)
    expect(create).not.toMatch(/as AuthUser/)
  })

  it('create-user role options come from ASSIGNABLE_ROLES', () => {
    expect(createUserSrc).toMatch(/ASSIGNABLE_ROLES/)
    expect(createUserSrc).not.toMatch(/const ASSIGNABLE/)
  })

  it('session-list documentation matches a reachable route', () => {
    expect(sessionsSrc).toMatch(/\/api\/auth\/sessions/)
    expect(sessionsSrc).not.toMatch(/exposes no endpoint/)
  })

  it('frontend role inventory has one production constant', () => {
    expect(typesSrc).toMatch(/export const ASSIGNABLE_ROLES/)
    expect(typesSrc).not.toMatch(/ALL_ROLES/)
  })

  it('own-activity cache key has one definition', () => {
    expect(adminSrc.match(/activityQueryKey/g)?.length).toBe(1)
    expect(activitySrc).toMatch(/activityQueryKey/)
    expect(activitySrc).not.toMatch(/queryKey:\s*\[\s*'activity'\s*\]/)
  })

  it('HomeView is unused-local clean', () => {
    expect(homeSrc).not.toMatch(/useQueryClient/)
    expect(homeSrc).not.toMatch(/queryClient/)
  })

  it('isAccountTab is reached by the hub', () => {
    expect(settingsSrc).toMatch(/isAccountTab/)
  })

  it('LinkTagsField is not a one-line alias', () => {
    expect(linkDialogSrc).not.toMatch(/function LinkTagsField/)
    expect(linkDialogSrc).toMatch(/<TagPicker picker=\{tags\} i18nPrefix="link_dialog"/)
  })

  it('createToken does not keep an unused expiry parameter', () => {
    expect(tokensSrc).not.toMatch(/expiresInDays/)
  })

  it('useNoteDialogController uses the shared apiError helpers', () => {
    expect(noteControllerSrc).toMatch(/apiErrorMessage/)
    expect(noteControllerSrc).not.toMatch(/function responseMessage/)
    expect(noteControllerSrc).not.toMatch(/function responseStatus/)
  })

  it('api modules do not import upward from hooks', () => {
    expect(authSrc).not.toMatch(/from ['"]\.\.\/hooks/)
    expect(adminSrc).not.toMatch(/from ['"]\.\.\/hooks/)
  })

  it('lib modules do not import upward from components', () => {
    expect(folderPayloadSrc).not.toMatch(/from ['"]\.\.\/components/)
  })
})
