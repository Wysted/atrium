import { ApiError } from '~/composables/useApi'
import type { App } from '~/types/api'

/** Mirrors the Go validation of apps (portal/apps.go `validAppName`): the server stays the authority. */
export const APP_NAME_MAX_BYTES = 200

const APP_NAME_INVALID = 'The name is too long (200 bytes at most) or has invalid characters.'

/** Copy for the validation codes of the app and deployment fields (`<field>.<code>`). */
export const APP_FIELD_COPY: Readonly<Record<string, string>> = {
  'name.invalid': APP_NAME_INVALID,
  'host.required': 'Enter a host.',
  'host.invalid': 'Enter a lowercase host name such as app.example.com: no scheme, port, path, spaces or trailing dot, and not an IP address.',
  'host.reserved': 'This host is reserved for the sign-in service.',
}

export const HOST_HINT = 'A DNS name such as app.example.com. Letters are lowercased for you.'

export const APP_CREATE_AMBIGUOUS_COPY = 'The app may have been created. The list was reloaded; if it is missing, reload again in a moment before trying again.'
export const PORTAL_APP_DISABLE_COPY = 'The portal app cannot be disabled.'
export const PORTAL_APP_LAST_HOST_COPY = 'The portal app needs at least one host.'
export const HOST_TAKEN_COPY
  = 'This host is already used by another app. If you just retried, it may have been added by your earlier attempt: check the list.'

/** The name shown for an app: its id when the name is empty. */
export function appLabel(app: Pick<App, 'id' | 'name'>): string {
  return app.name || app.id
}

/** Go checks `len()` and rejects invalid UTF-8 and NUL, so the limit is in bytes. */
export function validateAppName(name: string): string | null {
  if (new TextEncoder().encode(name).length > APP_NAME_MAX_BYTES || name.includes('\0')) return APP_NAME_INVALID
  return null
}

/**
 * Go's `hostname.Valid` accepts lowercase only, so uppercase would be a 422. The UI normalizes explicitly
 * (trim, then ASCII lowercase) and validates the result. ASCII only on purpose: `toLowerCase()` would turn the
 * Kelvin sign U+212A into `k` and let a non-ASCII value through that Go would never accept.
 */
export function normalizeHost(input: string): string {
  return input.trim().replace(/[A-Z]/g, c => c.toLowerCase())
}

/** Error for a normalized host, or null when it is valid (same rules as Go; the reserved auth host is server-side). */
export function validateHost(host: string): string | null {
  if (host === '') return APP_FIELD_COPY['host.required']!
  return isHostname(host) ? null : APP_FIELD_COPY['host.invalid']!
}

/**
 * True when a failed POST may have been committed on the server: the request was sent and we did not get a
 * definite answer (network error, 503, other 5xx, unknown). 4xx answers are definite. App creation is not
 * idempotent, so none of these is retried: the list is reloaded instead.
 */
export function isAmbiguousFailure(e: unknown): boolean {
  if (!(e instanceof ApiError)) return true
  return e.kind === 'network' || e.kind === 'unavailable' || e.kind === 'server' || e.kind === 'unknown'
}

/** True when `host` is the host the portal is being used from (`location.host` with port, or `hostname` without). */
export function isCurrentHost(host: string): boolean {
  if (typeof window === 'undefined') return false
  const h = host.toLowerCase()
  return h === window.location.host.toLowerCase() || h === window.location.hostname.toLowerCase()
}
