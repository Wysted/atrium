/** Copy for the validation codes of an email field (add member, user lookup). */
export const EMAIL_FIELD_COPY: Readonly<Record<string, string>> = {
  'email.required': 'Enter an email address.',
  'email.invalid': 'Enter a valid email address.',
  'email.no_unique_active_user': 'No single active user has this email, so it cannot be used here.',
}

/** Light client check; Go (`validEmail`) is the authority: one `@`, not first or last, no whitespace. */
export function validateEmail(email: string): string | null {
  if (email === '') return EMAIL_FIELD_COPY['email.required']!
  const at = email.indexOf('@')
  if (at <= 0 || at === email.length - 1 || email.split('@').length !== 2 || /\s/.test(email)) {
    return EMAIL_FIELD_COPY['email.invalid']!
  }
  return null
}
