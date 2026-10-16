import type { Initiative } from '~/types/api'

/** Mirrors the Go validation of initiatives (portal/initiatives.go): the server stays the authority. */
export const PLATFORM_INITIATIVE_KEY = 'platform'
export const INITIATIVE_KEY_MAX = 63
/** Go checks `len()`, so the limit is in bytes, not characters. */
export const INITIATIVE_NAME_MAX_BYTES = 200
export const INITIATIVE_KEY_PATTERN = /^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$/

export const INITIATIVE_KEY_RULES
  = 'Lowercase letters, digits, hyphens and underscores; it must start and end with a letter or digit. Up to 63 characters.'
const NAME_TOO_LONG = 'The name is too long (200 bytes at most) or has invalid characters.'

/** Copy for the validation codes of the initiative fields (`<field>.<code>`), shared by client and server errors. */
export const INITIATIVE_FIELD_COPY: Readonly<Record<string, string>> = {
  'key.required': 'Enter a key.',
  'key.invalid': INITIATIVE_KEY_RULES,
  'name.invalid': NAME_TOO_LONG,
}

/** The name shown for an initiative: the key when the name is empty. */
export function initiativeLabel(initiative: Pick<Initiative, 'key' | 'name'>): string {
  return initiative.name || initiative.key
}

export function validateInitiativeKey(key: string): string | null {
  if (key === '') return INITIATIVE_FIELD_COPY['key.required']!
  if (key.length > INITIATIVE_KEY_MAX || !INITIATIVE_KEY_PATTERN.test(key)) return INITIATIVE_KEY_RULES
  return null
}

export function validateInitiativeName(name: string): string | null {
  return new TextEncoder().encode(name).length > INITIATIVE_NAME_MAX_BYTES ? NAME_TOO_LONG : null
}
