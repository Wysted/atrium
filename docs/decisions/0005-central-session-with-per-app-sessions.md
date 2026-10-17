# 0005. Central session with per-app sessions

- Status: Accepted
- Date: 2026-10-14

## Context and Problem

Apps live on different hosts under one parent domain. Login happens once on the auth host, but each app host needs its own credential, and the handoff between hosts must resist login CSRF and session fixation.

## Considered Options

- A central session on the auth host plus an app session per app host, linked by one-time login codes (chosen).
- `SameSite=Strict`: would break the return from Google and links from mail or chat.

## Decision Outcome

Cookie values are 256-bit random and only their SHA-256 is stored. All cookies are host-only (`HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`, no `Domain`) and use the `__Host-Http-` prefix (draft-ietf-httpbis-layered-cookies; Chrome 140+, Firefox 143+; browsers without support still apply the `__Host-` rules, so it is never weaker). A `login_code` (30 s, single use, atomic consume) is accepted only by the browser that holds the matching `__Host-Http-platform_bind` cookie. A new session id is issued on every login. Revocation is deletion; app sessions cascade from the central one. Logout shows a "signed out" page instead of redirecting to login, because Google keeps its own session and would sign the user back in silently. IP and User-Agent changes are logged and alerted, never used to cut the session (NAT and mobile networks).

## Consequences

- A disabled user loses access within the 30 s decision cache; a session lasts at most 8 h plus idle limits.
- Rate limits on `/login` and `/oauth2/callback`; expired flows, codes and sessions are purged by a job.
- Reference: [Google login and sessions](../reference/login.md#rules).
