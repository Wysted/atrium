import type { Problem } from '~/types/api'

/**
 * Same-origin client for the portal API under /api/v1/. The session lives in a cookie that Traefik ForwardAuth
 * resolves, so there are no tokens and no CSRF headers (the backend relies on Fetch Metadata). Response bodies
 * are never logged.
 */
const API_BASE = '/api/v1'

export type ApiErrorKind
  = | 'unauthorized'
    | 'forbidden'
    | 'not_found'
    | 'conflict'
    | 'invalid'
    | 'unavailable'
    | 'server'
    | 'network'
    | 'unknown'

function kindOf(status: number): ApiErrorKind {
  switch (status) {
    case 401: return 'unauthorized'
    case 403: return 'forbidden'
    case 404: return 'not_found'
    case 409: return 'conflict'
    case 400:
    case 415:
    case 422: return 'invalid'
    case 503: return 'unavailable'
    default: return status >= 500 ? 'server' : 'unknown'
  }
}

export class ApiError extends Error {
  readonly status: number
  readonly kind: ApiErrorKind
  /** Parsed `application/problem+json` body, or null when the response had none. */
  readonly problem: Problem | null
  /** True when a 401 triggered a page reload so ForwardAuth can send the user to login. */
  readonly reloading: boolean

  constructor(status: number, problem: Problem | null, reloading = false) {
    // Fixed text: never put server-provided detail in the message (it may end up in a log).
    super(`API request failed with status ${status}`)
    this.name = 'ApiError'
    this.status = status
    this.kind = status === 0 ? 'network' : kindOf(status)
    this.problem = problem
    this.reloading = reloading
  }
}

function parseProblem(data: unknown): Problem | null {
  if (typeof data !== 'object' || data === null) return null
  const p = data as Record<string, unknown>
  if (typeof p.status !== 'number' || typeof p.title !== 'string') return null
  return data as Problem
}

/**
 * Session gone (401). ForwardAuth answers a navigation without a session with a 302 to login, so reloading the
 * current page is the whole recovery; the SPA never builds a login URL (`login_url` is not followed: it only
 * points back at this host). Loop guard: at most one reload per window, kept in sessionStorage (plus a module flag so parallel 401s of the same page load all report `reloading`); after that
 * the caller shows a "reload" message instead of reloading again.
 */
const REAUTH_KEY = 'portal:reauth-at'
const REAUTH_WINDOW_MS = 30_000

// Set once a reload has been started in this page load: every further 401 (parallel requests) reports
// `reloading` too, instead of flashing the "reload" message right before the page goes away.
let reloadStarted = false

function reloadForLogin(): boolean {
  if (!import.meta.client) return false
  if (reloadStarted) return true
  try {
    const last = Number(window.sessionStorage.getItem(REAUTH_KEY))
    if (Number.isFinite(last) && Date.now() - last < REAUTH_WINDOW_MS) return false
    window.sessionStorage.setItem(REAUTH_KEY, String(Date.now()))
  }
  catch {
    return false // storage unavailable: without the guard a reload could loop
  }
  reloadStarted = true
  window.location.reload()
  return true
}

function toApiError(e: unknown): ApiError {
  const res = (e as { response?: { status?: number, _data?: unknown } } | null)?.response
  if (!res || typeof res.status !== 'number') return new ApiError(0, null)
  const problem = parseProblem(res._data)
  if (res.status === 401) return new ApiError(401, problem, reloadForLogin())
  return new ApiError(res.status, problem)
}

/** GET a JSON resource. Rejects with ApiError. `path` starts with `/` and is relative to /api/v1. */
export async function apiGet<T>(path: string): Promise<T> {
  try {
    return await $fetch<T>(`${API_BASE}${path}`, {
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
      retry: 0,
    })
  }
  catch (e) {
    throw toApiError(e)
  }
}

export function useApi() {
  return { get: apiGet }
}

/**
 * Client-side data fetching (the app is ssr:false, so nothing is rendered on a server and there is no
 * hydration to mismatch). The handler never throws: failures come back as `error`, typed, so there is no
 * dependence on how Nuxt wraps thrown errors.
 */
export function useApiData<T>(key: string, path: string) {
  const state = useAsyncData(
    key,
    async (): Promise<{ data: T, error: null } | { data: null, error: ApiError }> => {
      try {
        return { data: await apiGet<T>(path), error: null }
      }
      catch (e) {
        return { data: null, error: e instanceof ApiError ? e : new ApiError(0, null) }
      }
    },
    { server: false },
  )
  return {
    data: computed(() => state.data.value?.data ?? null),
    error: computed(() => state.data.value?.error ?? null),
    pending: state.pending,
    refresh: () => state.refresh(),
  }
}
