import type { ApiToken } from '~/types/api'
import type { SelectOption } from '~/types/ui'

/** Mirrors Go `maxTokenName` (characters); the server stays the authority. */
export const TOKEN_NAME_MAX = 64
/** Mirrors the per-owner cap of live tokens (portal.maxLiveTokens). */
export const TOKEN_LIMIT = 25

const DAY = 86_400
/** Value of the "server maximum" choice: `expires_in` is omitted and the server applies its cap for the owner kind. */
export const EXPIRY_MAXIMUM = 'max'
export const EXPIRY_DEFAULT = String(7 * DAY)

/** Lifetimes offered at creation; the server caps them (personal 30 days, robot 90 days by default) and has the last word. */
export const EXPIRY_OPTIONS: SelectOption[] = [
  { value: String(DAY), label: '1 day' },
  { value: String(7 * DAY), label: '7 days' },
  { value: String(30 * DAY), label: '30 days' },
  { value: EXPIRY_MAXIMUM, label: 'Longest the server allows' },
]
/** Robots may live longer than people by default, so they are also offered 90 days. */
export const ROBOT_EXPIRY_OPTIONS: SelectOption[] = [
  ...EXPIRY_OPTIONS.slice(0, -1),
  { value: String(90 * DAY), label: '90 days' },
  EXPIRY_OPTIONS.at(-1)!,
]
export const TOKEN_UNCERTAIN_COPY = 'The result is unknown. Check the list before creating another token.'

export const SCOPE_OPTIONS: SelectOption[] = [
  { value: 'read', label: 'Read: can only read (GET)' },
  { value: 'admin', label: 'Admin: can also change things' },
]

/** Fixed copy of the 422 codes of a token creation, `<field>.<code>`. */
export const TOKEN_FIELD_COPY: Readonly<Record<string, string>> = {
  'name.required': 'Enter a name.',
  'name.invalid': `Use up to ${TOKEN_NAME_MAX} characters, without control characters.`,
  'scope.required': 'Choose a scope.',
  'scope.invalid': 'Choose a scope.',
  'scope.not_allowed': 'The admin scope needs an owner who is a platform admin or manages access in an initiative. Choose read.',
  'expires_in.invalid': 'Choose an expiry.',
  'expires_in.too_large': 'This is longer than the server allows. Choose a shorter expiry.',
}

export const TOKEN_LIMIT_COPY = `No more tokens can be created: the owner already has ${TOKEN_LIMIT} live tokens, or is disabled. Revoke one first.`

export type TokenState = 'active' | 'expired' | 'revoked'

/** Revoked wins over expired. Informational: the API decides whether a token works. */
export function tokenState(token: Pick<ApiToken, 'revoked_at' | 'expires_at'>, now = Date.now()): TokenState {
  if (token.revoked_at) return 'revoked'
  return isPast(token.expires_at, now) ? 'expired' : 'active'
}

export function validateTokenName(name: string): string | null {
  if (name === '') return TOKEN_FIELD_COPY['name.required']!
  return [...name].length > TOKEN_NAME_MAX ? TOKEN_FIELD_COPY['name.invalid']! : null
}
