import { INITIATIVE_KEY_PATTERN } from '~/utils/initiative'

/** Mirrors the Go validation of groups (portal/authz.go `maxGroupKey`, `maxGroupName`): the server stays the authority. */
export const GROUP_KEY_MAX = 63
/** Go checks `len()`, so the limit is in bytes, not characters. */
export const GROUP_NAME_MAX_BYTES = 200

export const GROUP_KEY_RULES
  = 'Lowercase letters, digits, hyphens and underscores; it must start and end with a letter or digit. Up to 63 characters.'
const GROUP_NAME_TOO_LONG = 'The name is too long (200 bytes at most) or has invalid characters.'

/** Copy for the validation codes of the group fields (`<field>.<code>`), shared by client and server errors. */
export const GROUP_FIELD_COPY: Readonly<Record<string, string>> = {
  'key.required': 'Enter a key.',
  'key.invalid': GROUP_KEY_RULES,
  'name.invalid': GROUP_NAME_TOO_LONG,
}

/** The name shown for a group: the key when the name is empty. */
export function groupLabel(group: { key: string, name: string }): string {
  return group.name || group.key
}

export function validateGroupKey(key: string): string | null {
  if (key === '') return GROUP_FIELD_COPY['key.required']!
  if (key.length > GROUP_KEY_MAX || !INITIATIVE_KEY_PATTERN.test(key)) return GROUP_KEY_RULES
  return null
}

export function validateGroupName(name: string): string | null {
  return new TextEncoder().encode(name).length > GROUP_NAME_MAX_BYTES ? GROUP_NAME_TOO_LONG : null
}

/** `GET /groups?q=` accepts at most 64 characters (longer is a 422); the search box caps the input to match. */
export const GROUP_SEARCH_MAX = 64

/** List path for a (trimmed) search text; the same `q` must be sent on every page, which `usePagedList` does by calling this again. */
export function groupsPath(query: string): string {
  return query ? `/groups?q=${encodeURIComponent(query)}` : '/groups'
}
