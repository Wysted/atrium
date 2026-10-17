import type { HealthProblemCode, ProbeStatus } from '~/types/api'

/** Fixed copy of the integration health findings. The wording follows docs/reference/metrics.md ("Health of an app"); it is generic on purpose. */
export interface ProblemCopy {
  label: string
  /** What it means and what an app owner should check. */
  explanation: string
  tone: 'danger' | 'warning' | 'neutral'
}

export const PROBLEM_COPY: Record<HealthProblemCode, ProblemCopy> = {
  app_disabled: {
    label: 'App disabled',
    explanation: 'The app is disabled, so access is denied to everyone. A platform administrator can enable it.',
    tone: 'warning',
  },
  initiative_inactive: {
    label: 'Initiative inactive',
    explanation: 'The initiative is inactive, so access to all its apps is denied to everyone. A platform administrator can activate it.',
    tone: 'warning',
  },
  no_grants: {
    label: 'No grants',
    explanation: 'Nobody holds an unexpired grant in this initiative, so no one can reach the app. An initiative manager or a platform administrator can grant access to a user or a group.',
    tone: 'warning',
  },
  no_manager: {
    label: 'No manager',
    explanation: 'No active user manages access in this initiative, directly or through a group. An initiative manager or a platform administrator can grant a role that manages access.',
    tone: 'warning',
  },
  unprotected: {
    label: 'Unprotected',
    explanation: 'The last probe was served without a redirect to sign in, so the host may be reachable without ForwardAuth. Check that the route for this host uses the ForwardAuth middleware. If the host was never seen either, the route may be missing or wrong.',
    tone: 'danger',
  },
  unexpected_redirect: {
    label: 'Unexpected redirect',
    explanation: 'The last probe was redirected somewhere other than the sign-in page (for example a permanent redirect or another host). Check the redirects configured for this host.',
    tone: 'danger',
  },
  unreachable: {
    label: 'Unreachable',
    explanation: 'The last probe could not connect to the host (DNS, TCP, TLS or timeout). Check that the host resolves, its certificate is valid and the route is up.',
    tone: 'danger',
  },
  never_probed: {
    label: 'Not probed yet',
    explanation: 'The host has not been probed yet. Probes run periodically: check again after the next round.',
    tone: 'neutral',
  },
  probe_stale: {
    label: 'Probe result is old',
    explanation: 'The last probe result is older than three probe intervals, so probe rounds are not completing. A platform administrator can check the portal probe.',
    tone: 'warning',
  },
  probe_disabled: {
    label: 'Probe disabled',
    explanation: 'The portal does not probe hosts, so protection by ForwardAuth is not being checked. Any probe result shown may be old.',
    tone: 'neutral',
  },
  never_seen: {
    label: 'Never seen',
    explanation: 'No authorized request has ever reached this host through ForwardAuth. Check that the route sends traffic through the ForwardAuth middleware and that the host is the one users open.',
    tone: 'warning',
  },
  not_seen_recently: {
    label: 'Not seen recently',
    explanation: 'No authorized request in the last 24 hours. This can be normal for a rarely used app; otherwise check the route and DNS of this host.',
    tone: 'warning',
  },
}

/** Copy for a code this version does not know (the API vocabulary may grow). */
export const UNKNOWN_PROBLEM_COPY: ProblemCopy = {
  label: 'Unknown finding',
  explanation: 'The portal reported a finding this page does not know. Reload the page, or ask a platform administrator.',
  tone: 'neutral',
}

export function problemCopy(code: string): ProblemCopy {
  return PROBLEM_COPY[code as HealthProblemCode] ?? UNKNOWN_PROBLEM_COPY
}

/** Label and tone of the stored result of the last probe (`null`: no probe yet). */
export function probeStatusCopy(status: ProbeStatus | (string & {}) | null): { label: string, tone: 'success' | 'danger' | 'warning' | 'neutral' } {
  switch (status) {
    case 'ok': return { label: 'Redirects to sign-in', tone: 'success' }
    case 'unprotected': return { label: 'Unprotected', tone: 'danger' }
    case 'unexpected': return { label: 'Unexpected redirect', tone: 'danger' }
    case 'unreachable': return { label: 'Unreachable', tone: 'danger' }
    case null: return { label: 'Not probed', tone: 'neutral' }
    default: return { label: 'Unknown result', tone: 'neutral' }
  }
}
