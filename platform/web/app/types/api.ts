/**
 * Payloads of the portal API (`/api/v1/`). Mirror of platform/api/internal/portalhttp (ground truth:
 * handlers_test.go) and platform/docs/contract.md, block 6. Lists are never null.
 */

/** GET /api/v1/me and /me/apps embed this reference to an initiative. */
export interface InitiativeRef {
  id: string
  key: string
  name: string
}

export interface MeUser {
  id: string
  email: string
  name: string
}

export interface MePermissions {
  platform_admin: boolean
  /** Initiatives where the caller has a non-expired `manages_access` grant. */
  managed_initiatives: InitiativeRef[]
}

/** GET /api/v1/me */
export interface MeResponse {
  user: MeUser
  permissions: MePermissions
}

/** One entry of the home: an app the caller can open. */
export interface HomeApp {
  id: string
  name: string
  /** Hostnames of the app deployments, never null. */
  hosts: string[]
  initiative: InitiativeRef
  /** Role keys the caller holds on the app, never null. */
  roles: string[]
}

/** GET /api/v1/me/apps */
export interface HomeResponse {
  apps: HomeApp[]
}

export interface ProblemFieldError {
  field: string
  code: string
}

/**
 * RFC 9457 problem document (`application/problem+json`).
 * `login_url` is the portal extension on 401 (ForwardAuth); `errors` is set on 422.
 */
export interface Problem {
  type: string
  title: string
  status: number
  detail?: string
  login_url?: string
  errors?: ProblemFieldError[]
}

/** `initiatives.status` (portal.InitiativeStatusActive / Inactive). */
export type InitiativeStatus = 'active' | 'inactive'

/** One initiative of the administration API: list items, GET, POST (201) and PATCH answer with this shape. */
export interface Initiative {
  id: string
  /** Immutable after creation. */
  key: string
  /** May be empty: the UI shows the key then. */
  name: string
  status: InitiativeStatus
  created_at: string
}

/** GET /api/v1/initiatives?cursor=&limit= . `next_cursor` is absent on the last page and opaque otherwise. */
export interface InitiativeList {
  initiatives: Initiative[]
  next_cursor?: string
}

/** POST /api/v1/initiatives. `name` may be omitted or empty. */
export interface CreateInitiativeBody {
  key: string
  name?: string
}

/** PATCH /api/v1/initiatives/{id}. At least one field; the key is immutable (sending it is a 400). */
export interface PatchInitiativeBody {
  name?: string
  status?: InitiativeStatus
}

/** GET/POST/PATCH of a role: `/initiatives/{id}/roles`, `/roles/{id}`. The key is immutable. */
export interface Role {
  id: string
  initiative_id: string
  key: string
  manages_access: boolean
}

/** GET /api/v1/initiatives/{id}/roles?cursor=&limit= */
export interface RoleList {
  roles: Role[]
  next_cursor?: string
}

/** POST /api/v1/initiatives/{id}/roles. `key` <= 32 chars with the initiative key pattern. */
export interface CreateRoleBody {
  key: string
  manages_access: boolean
}

/** PATCH /api/v1/roles/{id}: `manages_access` is the only field. */
export interface PatchRoleBody {
  manages_access: boolean
}

/** The role embedded in a grant (no `initiative_id`). */
export interface GrantRole {
  id: string
  key: string
  manages_access: boolean
}

export interface GrantUser {
  id: string
  email: string
  name: string
}

/** A group reference: embedded in a grant and listed by GET /groups (which adds `created_at`). */
export interface GrantGroup {
  id: string
  key: string
  name: string
}

/** One grant. Exactly one of `user` and `group` is set, the other is null. */
export interface Grant {
  id: string
  initiative_id: string
  role: GrantRole
  user: GrantUser | null
  group: GrantGroup | null
  /** RFC 3339; null = never. Expired grants are listed too. */
  expires_at: string | null
  /** User id of the granter; null when that user no longer exists. */
  granted_by: string | null
  granted_at: string
}

/** GET /api/v1/initiatives/{id}/grants?cursor=&limit= (oldest first). */
export interface GrantList {
  grants: Grant[]
  next_cursor?: string
}

/** POST /api/v1/initiatives/{id}/grants. Exactly one of `email` and `group_id`; `expires_at` must be in the future. */
export interface CreateGrantBody {
  email?: string
  group_id?: string
  role_id: string
  expires_at?: string
}

export interface Group {
  id: string
  key: string
  name: string
  created_at: string
}

/** GET /api/v1/groups?cursor=&limit= (platform admins and managers; no members). */
export interface GroupList {
  groups: Group[]
  next_cursor?: string
}
