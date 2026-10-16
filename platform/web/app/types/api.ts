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
