import { INITIATIVE_KEY_PATTERN } from '~/utils/initiative'

/** Mirrors the Go validation of role keys (portal/initiatives.go `maxRoleKey`): the server stays the authority. */
export const ROLE_KEY_MAX = 32

export const ROLE_KEY_RULES
  = 'Lowercase letters, digits, hyphens and underscores; it must start and end with a letter or digit. Up to 32 characters.'

/** Copy for the validation codes of the role fields (`<field>.<code>`), shared by client and server errors. */
export const ROLE_FIELD_COPY: Readonly<Record<string, string>> = {
  'key.required': 'Enter a key.',
  'key.invalid': ROLE_KEY_RULES,
}

export function validateRoleKey(key: string): string | null {
  if (key === '') return ROLE_FIELD_COPY['key.required']!
  if (key.length > ROLE_KEY_MAX || !INITIATIVE_KEY_PATTERN.test(key)) return ROLE_KEY_RULES
  return null
}

/** Fixed copy for the 409 of removing management power (turning `manages_access` off, revoking a manager grant). */
export const LAST_MANAGER_COPY = 'This is the last active manager of the initiative. Grant another manager first.'
