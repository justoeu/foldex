import { describe, it, expect, vi } from 'vitest'
import { waitFor } from '@testing-library/react'
import {
  buildImageUploadHandler,
  noteImageErrorMessage,
} from './useNoteDialogController'

function fakeView() {
  return {
    isDestroyed: false,
    state: {
      selection: { from: 0, to: 0 },
      schema: { nodes: { image: { create: () => ({}) } } },
      tr: { replaceWith: () => ({}) },
    },
    dispatch: vi.fn(),
  } as unknown as Parameters<ReturnType<typeof buildImageUploadHandler>>[0]
}

const storageError = {
  response: {
    status: 503,
    data: { error: { code: 'storage_unavailable', message: 'object store down' } },
  },
}

// BP-MEN-001: the handler flattened every failure to the literal
// 'upload_failed' and the caller ignored it anyway — a storage outage, an
// oversized file and an expired session all showed the same generic note,
// while the link dialog mapped the same codes to specific messages.
describe('buildImageUploadHandler', () => {
  it('hands the raw error to onError so the caller can map codes', async () => {
    const onError = vi.fn()
    const handler = buildImageUploadHandler(
      () => Promise.reject(storageError),
      onError,
    )
    handler(fakeView(), new File(['x'], 'a.png', { type: 'image/png' }))
    await waitFor(() => expect(onError).toHaveBeenCalled())
    expect(onError).toHaveBeenCalledWith(storageError)
  })
})

describe('noteImageErrorMessage', () => {
  const t = (key: string) => key

  it('maps storage_unavailable to the storage-specific key', () => {
    expect(noteImageErrorMessage(storageError, t)).toBe('note_dialog.image_error_storage')
  })

  it('prefers the server message for unmapped codes', () => {
    const other = {
      response: { status: 400, data: { error: { code: 'invalid_image', message: 'not an image' } } },
    }
    expect(noteImageErrorMessage(other, t)).toBe('not an image')
  })

  it('falls back to the generic key when there is no message', () => {
    expect(noteImageErrorMessage(new Error('network'), t)).toBe('note_dialog.image_error_generic')
  })
})
