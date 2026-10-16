export type PickedFileWriter = {
  write: (chunk: Uint8Array) => Promise<void>
  close: () => Promise<void>
  abort: (reason?: unknown) => Promise<void>
}

export type SaveFilePicker = (options: {
  suggestedName: string
  types: { description: string; accept: Record<string, string[]> }[]
}) => Promise<{ createWritable: () => Promise<{ getWriter: () => PickedFileWriter }> }>

export type StreamResponseToPickedFileArgs = {
  picker: SaveFilePicker
  suggestedName: string
  types: { description: string; accept: Record<string, string[]> }[]
  fetchResponse: () => Promise<Response>
  responseError: (response: Response) => Promise<Error>
  notStreamableMessage: string
}

export type StreamedToPickedFile = {
  response: Response
  bytesWritten: number
  startedAt: number
}

/**
 * Streams a fetch response straight into the file chosen via the File System
 * Access API, chunk by chunk, so a multi-gigabyte payload never lands in tab
 * memory. The picker opens BEFORE the request: a file dialog is a user
 * gesture browsers refuse after a network await, and a request spent before
 * the dialog would already have consumed server work for nothing.
 *
 * On any failure both sides are torn down — the reader is cancelled and the
 * writer aborted — so no half-written file is left on disk looking like a
 * complete download. Neither teardown failure hides the original error.
 */
export async function streamResponseToPickedFile(
  args: StreamResponseToPickedFileArgs,
): Promise<StreamedToPickedFile> {
  const handle = await args.picker.call(window, {
    suggestedName: args.suggestedName,
    types: args.types,
  })
  const writer = (await handle.createWritable()).getWriter()
  const startedAt = performance.now()
  let reader: ReadableStreamDefaultReader<Uint8Array> | null = null
  let bytesWritten = 0
  try {
    const response = await args.fetchResponse()
    if (!response.ok) throw await args.responseError(response)
    if (!response.body) throw new Error(args.notStreamableMessage)
    reader = response.body.getReader()
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      bytesWritten += value.byteLength
      await writer.write(value)
    }
    await writer.close()
    return { response, bytesWritten, startedAt }
  } catch (error) {
    await Promise.allSettled([
      reader?.cancel(error),
      writer.abort(error),
    ])
    throw error
  }
}
