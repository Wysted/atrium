/**
 * Hostnames come from the API (trusted server data, validated by the same DNS rules as `deployments.host`),
 * but the SPA still checks them before building an href. It mirrors `hostname.Valid` in
 * platform/api/internal/hostname: lowercase labels of 1-63 letters, digits or hyphens (no leading/trailing
 * hyphen), separated by dots, at most 253 characters, no trailing dot, and a last label that is not all digits
 * (it would be an IP address or look like one). No scheme, port, path, userinfo or whitespace can pass, so
 * `https://${host}/` can only ever point at that host.
 */
const MAX_HOSTNAME_LENGTH = 253
const ALL_DIGITS = /^[0-9]+$/
const LABEL = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/

export function isHostname(value: unknown): value is string {
  if (typeof value !== 'string' || value.length === 0 || value.length > MAX_HOSTNAME_LENGTH) return false
  const labels = value.split('.')
  if (!labels.every(label => LABEL.test(label))) return false
  return !ALL_DIGITS.test(labels[labels.length - 1]!)
}

/** `https://<host>/` for a valid hostname, otherwise null. */
export function hostUrl(host: unknown): string | null {
  return isHostname(host) ? `https://${host}/` : null
}
