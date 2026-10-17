/** Fixed 409 copy of the lockout guards of `PATCH /users/{id}` (portal.ErrSelfDisable, portal.ErrLastAdmin). */
export const SELF_DISABLE_COPY = 'You cannot disable your own account.'
export const LAST_ADMIN_COPY = 'This is the last active platform admin.'

/** A person shows by email; a robot (empty email) by name. */
export function subjectLabel(user: { email: string, name: string }): string {
  return user.email || user.name
}
