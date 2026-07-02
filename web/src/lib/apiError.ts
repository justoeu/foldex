// apiErrorCode pulls the `error.code` out of the uniform backend error envelope
// ({ error: { code, message } }) carried on an axios rejection. Returns
// undefined when the shape doesn't match (network error, non-envelope 5xx),
// so callers can fall back to a generic message.
export function apiErrorCode(e: unknown): string | undefined {
  return (e as { response?: { data?: { error?: { code?: string } } } })?.response?.data?.error?.code
}
