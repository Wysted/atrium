import { TOKEN_FIELD_COPY, validateTokenName } from '~/utils/token'

/** Robot names follow the token name rules (1 to TOKEN_NAME_MAX characters, no control characters). */
export const validateRobotName = validateTokenName

/** Fixed copy of the 422 codes of the robot fields, `<field>.<code>`. */
export const ROBOT_FIELD_COPY: Readonly<Record<string, string>> = {
  'name.required': TOKEN_FIELD_COPY['name.required']!,
  'name.invalid': TOKEN_FIELD_COPY['name.invalid']!,
  'owner_user_id.unknown': 'The owner must be an active person.',
  'owner_user_id.invalid': 'The owner must be an active person.',
}

export const ROBOT_NAME_TAKEN_COPY = 'A robot with this name already exists.'

/** The owner as shown: email, else name; null without an owner. */
export function robotOwnerLabel(robot: { owner_user_id: string | null, owner_email: string | null, owner_name: string | null }): string | null {
  if (!robot.owner_user_id) return null
  return robot.owner_email || robot.owner_name || 'Unknown'
}
