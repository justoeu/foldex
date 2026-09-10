import { describe, expect, it, vi } from 'vitest'

import { artifactFilename, saveBackupArtifact } from './backupDownload'

/*
 * The half-written file.
 *
 * BackupSection.test.tsx covers the download the way an operator meets it —
 * click, password, bytes on disk. What it cannot reach is the failure that only
 * exists mid-stream: the request succeeded, chunks are already on disk, and THEN
 * the connection drops. A response that fails before the first write leaves
 * nothing to clean up, so it proves nothing about the cleanup.
 *
 * That case is the reason `streamArtifactToFile` aborts BOTH sides. Without the
 * abort the operator is left holding a truncated file that age refuses to open —
 * which reads as a corrupt backup rather than as a failed download, and is the
 * worst thing a backup feature can tell someone.
 */

type Recorded = { chunks: Uint8Array[]; closed: boolean; aborted: unknown[] }

function recordingWriter(sink: Recorded) {
  return {
    write: (c: Uint8Array) => { sink.chunks.push(c); return Promise.resolve() },
    close: () => { sink.closed = true; return Promise.resolve() },
    abort: (reason?: unknown) => { sink.aborted.push(reason); return Promise.resolve() },
  }
}

function pickerFor(sink: Recorded) {
  return vi.fn().mockResolvedValue({
    createWritable: () => Promise.resolve({ getWriter: () => recordingWriter(sink) }),
  })
}

/** A body that yields one chunk and then fails, like a dropped connection. */
function bodyThatDiesAfterOneChunk(failure: Error, cancelled: unknown[]) {
  let served = false
  return {
    getReader: () => ({
      read: () => {
        if (served) return Promise.reject(failure)
        served = true
        return Promise.resolve({ done: false, value: new TextEncoder().encode('age-') })
      },
      cancel: (reason?: unknown) => { cancelled.push(reason); return Promise.resolve() },
    }),
  }
}

describe('saveBackupArtifact', () => {
  it('abandons the file when the stream dies after bytes were already written', async () => {
    const sink: Recorded = { chunks: [], closed: false, aborted: [] }
    const cancelled: unknown[] = []
    const dropped = new Error('network dropped')
    const fetchImpl = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      body: bodyThatDiesAfterOneChunk(dropped, cancelled),
    } as unknown as Response)

    await expect(saveBackupArtifact(7, 'a-long-enough-pass', 'foldex.dump.age', {
      picker: pickerFor(sink),
      fetchImpl: fetchImpl as never,
    })).rejects.toThrow('network dropped')

    // A chunk did reach the disk — otherwise this test would be proving the
    // easy case again.
    expect(sink.chunks).toHaveLength(1)
    expect(sink.aborted).toEqual([dropped])
    expect(cancelled).toEqual([dropped])
    expect(sink.closed).toBe(false)
  })

  it('closes the file exactly once when the stream completes', async () => {
    const sink: Recorded = { chunks: [], closed: false, aborted: [] }
    const fetchImpl = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      body: new ReadableStream({
        start(c) { c.enqueue(new TextEncoder().encode('age-encrypted')); c.close() },
      }),
    } as unknown as Response)

    await saveBackupArtifact(7, 'a-long-enough-pass', 'foldex.dump', {
      picker: pickerFor(sink),
      fetchImpl: fetchImpl as never,
    })

    expect(sink.closed).toBe(true)
    expect(sink.aborted).toEqual([])
  })

  /* A 200 with no body is not a download; without this guard the file would be
     closed empty and look like a backup of nothing. */
  it('refuses a response that carries no stream', async () => {
    const sink: Recorded = { chunks: [], closed: false, aborted: [] }
    const fetchImpl = vi.fn().mockResolvedValue({ ok: true, status: 200, body: null } as unknown as Response)

    await expect(saveBackupArtifact(7, 'a-long-enough-pass', 'x.age', {
      picker: pickerFor(sink),
      fetchImpl: fetchImpl as never,
    })).rejects.toThrow(/not streamable/)
    expect(sink.closed).toBe(false)
    expect(sink.aborted).toHaveLength(1)
  })

  /* The picker opens before the request precisely so a cancelled dialog costs
     nothing — no request means no download spent from the budget of three. */
  it('spends no download when the operator cancels the save dialog', async () => {
    const fetchImpl = vi.fn()
    const picker = vi.fn().mockRejectedValue(new DOMException('user aborted', 'AbortError'))

    await expect(saveBackupArtifact(7, 'a-long-enough-pass', 'x.age', {
      picker: picker as never,
      fetchImpl: fetchImpl as never,
    })).rejects.toThrow(/aborted/)
    expect(fetchImpl).not.toHaveBeenCalled()
  })
})

describe('artifactFilename', () => {
  it('takes the last segment of an object key', () => {
    expect(artifactFilename('backups/dump/2026/09/09/foldex.dump.age')).toBe('foldex.dump.age')
  })

  // A key that ends in a slash, or is empty, still has to name a file the
  // browser can save.
  it('names something savable for a key that ends in nothing', () => {
    expect(artifactFilename('backups/dump/')).toBe('dump')
    expect(artifactFilename('')).toBe('foldex-backup.age')
  })
})
