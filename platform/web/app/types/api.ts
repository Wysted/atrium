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
  /** Areas the caller supervises, by key; always `[]` under a token. Gates the supervisor screens. */
  supervised_areas: SupervisedArea[]
}

/** An area as `/me` lists it (`permissions.supervised_areas`). */
export interface SupervisedArea {
  id: string
  key: string
  name: string
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

/**
 * One grant. Exactly one of `user` and `group` is set, the other is null. A robot is a `user` with an empty
 * `email` (the robot's name is in `name`).
 */
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

/** POST /api/v1/initiatives/{id}/grants. Exactly one of `email`, `group_id` and `robot_id`; `expires_at` must be in the future. */
export interface CreateGrantBody {
  email?: string
  group_id?: string
  robot_id?: string
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

/** `apps.status` (portal.AppStatusActive / AppStatusDisabled). */
export type AppStatus = 'active' | 'disabled'

/** `deployments.last_probe_status` (portal.ProbeOK, ProbeUnprotected, ProbeUnexpected, ProbeUnreachable). */
export type ProbeStatus = 'ok' | 'unprotected' | 'unexpected' | 'unreachable'

/** A probe status as received: the vocabulary may grow, so the UI falls back to a neutral "Unknown result" (`probeStatusCopy`). */
export type ReceivedProbeStatus = ProbeStatus | (string & {})

/**
 * A host of an app (portalhttp `deploymentJSON`). The telemetry fields are null until the deployment is seen or
 * probed (`last_probe_status` is one of `ProbeStatus` when set).
 */
export interface Deployment {
  id: string
  app_id: string
  host: string
  created_at: string
  last_seen_at: string | null
  last_probe_at: string | null
  last_probe_status: ReceivedProbeStatus | null
}

/**
 * An app with its deployments (`appJSON`): list items, POST (201) and PATCH answer with this shape. `id` is
 * `app_` plus a ULID, generated by the server, immutable; so is `initiative_id`. The payload carries neither the
 * initiative key nor any "portal app" flag. `name` may be empty (the UI shows the id).
 */
export interface App {
  id: string
  initiative_id: string
  name: string
  status: AppStatus
  created_at: string
  /** True only for the portal's own app: the API refuses to disable it or to remove its last host (409). */
  portal_app: boolean
  /** Ordered by host, never null. */
  deployments: Deployment[]
}

/** GET /api/v1/initiatives/{id}/apps?cursor=&limit= (oldest first). */
export interface AppList {
  apps: App[]
  next_cursor?: string
}

/** POST /api/v1/initiatives/{id}/apps. `name` may be omitted or empty (<= 200 bytes). Not idempotent. */
export interface CreateAppBody {
  name?: string
}

/** PATCH /api/v1/apps/{id}. At least one field; the id and the initiative are immutable (sending them is a 400). */
export interface PatchAppBody {
  name?: string
  status?: AppStatus
}

/** POST /api/v1/apps/{id}/deployments (201 answers a `Deployment`). Idempotent by host. */
export interface CreateDeploymentBody {
  host: string
}

/** POST /api/v1/groups. `key` follows the initiative key pattern (<= 63); `name` <= 200 bytes, may be empty. */
export interface CreateGroupBody {
  key: string
  name?: string
}

/** An area (platform admins only): a set of people whose robots and tokens its supervisors manage. `GET /areas`. */
export interface Area {
  id: string
  key: string
  name: string
  created_at: string
}

/** GET /api/v1/areas?cursor=&limit= (keyset). */
export interface AreaList {
  areas: Area[]
  next_cursor?: string
}

/** Which list of an area: `GET /areas/{id}/<kind>` answers `{ <kind>: [...] }`. */
export type AreaPersonKind = 'members' | 'supervisors'

export interface AreaPersonList {
  members?: GroupMember[]
  supervisors?: GroupMember[]
  next_cursor?: string
}

/** `users.status` (portal.UserStatusActive / UserStatusDisabled). */
export type UserStatus = 'active' | 'disabled'

/** One member of a group (platform admins only). `user_id` is the member's id; disabled users are listed too. */
export interface GroupMember {
  user_id: string
  email: string
  name: string
  status: UserStatus
  added_at: string
}

/** GET /api/v1/groups/{id}/members?cursor=&limit= (ordered by user id). */
export interface GroupMemberList {
  members: GroupMember[]
  next_cursor?: string
}

/** POST /api/v1/groups/{id}/members (201 answers a `GroupMember`). The email must match exactly one active user. */
export interface AddMemberBody {
  email: string
}

/** A user as the administration API shows it (`adminUserJSON`). */
export interface AdminUser {
  id: string
  email: string
  name: string
  status: UserStatus
  created_at: string
}

/** GET /api/v1/users?email= . Never paged: every user with that email (not unique), at most 50. */
export interface AdminUserList {
  users: AdminUser[]
}

/** PATCH /api/v1/users/{id} (answers an `AdminUser`). Disabling also deletes the user's sessions. */
export interface PatchUserBody {
  status: UserStatus
}

/** DELETE /api/v1/users/{id}/sessions answers 200 with the number of central sessions removed. */
export interface RevokeSessionsResult {
  revoked: number
}

/**
 * One audit row (`auditEventJSON`). `actor_*` and `initiative_id` are null when unknown or global; a robot has
 * `actor_email` null and its name in `actor_name`. `before` and `after` are arbitrary JSON documents (or null):
 * data, never markup.
 */
export interface AuditEvent {
  id: string
  occurred_at: string
  actor_id: string | null
  actor_email: string | null
  actor_name: string | null
  /** The API token that made the request; null (or absent) for a browser session. */
  token_id?: string | null
  initiative_id: string | null
  action: string
  target_type: string
  target_id: string
  before: unknown
  after: unknown
}

/** GET /api/v1/audit?initiative_id=&cursor=&limit= (newest first). */
export interface AuditList {
  events: AuditEvent[]
  next_cursor?: string
}

/** Finding codes of the app health report (portal.Problem*); a stable vocabulary. */
export type HealthProblemCode
  = | 'app_disabled'
    | 'initiative_inactive'
    | 'no_grants'
    | 'no_manager'
    | 'unprotected'
    | 'unexpected_redirect'
    | 'unreachable'
    | 'never_probed'
    | 'probe_stale'
    | 'probe_disabled'
    | 'never_seen'
    | 'not_seen_recently'

/** One finding (`problemJSON`). `host` is null for a finding about the app as a whole. */
export interface HealthProblem {
  /** Known codes, or any string the API may add later: the UI falls back to `UNKNOWN_PROBLEM_COPY` (`problemCopy`). */
  code: HealthProblemCode | (string & {})
  host: string | null
}

/**
 * GET /api/v1/apps/{id}/health (platform admins and managers of the app's initiative; anyone else gets 404).
 * `problems` is never null: app-level findings first, then each deployment in host order. The verdict is the
 * server's: the client does not recompute thresholds.
 */
export interface AppHealth {
  app: App
  initiative_status: InitiativeStatus
  grant_count: number
  effective_manager_grants: number
  problems: HealthProblem[]
  checked_at: string
}

/** A host refused as `unknown_host` in the last 7 days that has no deployment (`orphanHostJSON`). */
export interface OrphanHost {
  host: string
  /** Recorded denials (at most one per host every 10 minutes per replica): how long it has been hit. */
  count: number
  last_denied_at: string
}

/** GET /api/v1/health/orphan-hosts (platform admins only; most recent first, at most 100, no cursor). */
export interface OrphanHostList {
  hosts: OrphanHost[]
}

/** `api_tokens.scope`: a ceiling on the owner's permissions (`read` = GET/HEAD only). */
export type TokenScope = 'read' | 'admin'

/** A token as listed (`tokenJSON`): never the secret or the hash. `revoked_at` null while not revoked. */
export interface ApiToken {
  id: string
  name: string
  scope: TokenScope
  created_at: string
  expires_at: string
  last_used_at: string | null
  revoked_at: string | null
}

/** GET /api/v1/me/tokens and /robots/{id}/tokens (newest first; expired and revoked ones stay listed for 30 days). */
export interface TokenList {
  tokens: ApiToken[]
  next_cursor?: string
}

/** The 201 of a token creation: the only response that carries the secret, once. */
export interface CreatedToken extends ApiToken {
  secret: string
}

/** POST /api/v1/me/tokens and /robots/{id}/tokens. `expires_in` is in seconds; omitted = the server's cap for the owner kind. */
export interface CreateTokenBody {
  name: string
  scope: TokenScope
  expires_in?: number
}

/** What a manager sees of a robot (`robotRefJSON`): enough to grant. */
export interface RobotRef {
  id: string
  name: string
  status: UserStatus
}

/** A robot as platform admins see it (`robotJSON`). The owner is informational, not authority. */
export interface Robot extends RobotRef {
  owner_user_id: string | null
  /** Resolved by the API; null with no owner. The name may be empty. */
  owner_email: string | null
  owner_name: string | null
  created_at: string
  last_used_at: string | null
}

/** GET /api/v1/robots for platform admins. */
export interface RobotList {
  robots: Robot[]
  next_cursor?: string
}

/** GET /api/v1/robots for managers (also the shape of the grant picker's source). */
export interface RobotRefList {
  robots: RobotRef[]
  next_cursor?: string
}

/** POST /api/v1/robots. */
export interface CreateRobotBody {
  name: string
  owner_user_id?: string
}

/** PATCH /api/v1/robots/{id}. At least one field; `owner_user_id: ''` clears the owner. */
export interface PatchRobotBody {
  name?: string
  status?: UserStatus
  owner_user_id?: string
}
