// apiErrorCode pulls the `error.code` out of the uniform backend error envelope
// ({ error: { code, message } }) carried on an axios rejection. Returns
// undefined when the shape doesn't match (network error, non-envelope 5xx),
// so callers can fall back to a generic message.
export function apiErrorCode(e: unknown): string | undefined {
  return (e as { response?: { data?: { error?: { code?: string } } } })?.response?.data?.error?.code
}

/**
 * The server's own message for the same envelope.
 *
 * Needed where a rule is CONFIGURABLE and the client's copy would state the
 * wrong number: the password floor is owner-configurable (ADR-35), so an
 * instance demanding twenty characters would still read "at least 8" if the
 * message were built from the client constant. Returns undefined when the
 * shape does not match, so the caller keeps its own wording as the fallback.
 */
export function apiErrorMessage(e: unknown): string | undefined {
  const msg = (e as { response?: { data?: { error?: { message?: string } } } })
    ?.response?.data?.error?.message
  return typeof msg === 'string' && msg.trim() !== '' ? msg : undefined
}
