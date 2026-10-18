# Google login and sessions

How users sign in with Google and how sessions, cookies and one-time codes work. Rationale: [ADR 0004](../decisions/0004-google-only-login-with-hd-allowlist.md), [ADR 0005](../decisions/0005-central-session-with-per-app-sessions.md).

## Google OIDC

1. Authorization Code + PKCE (S256) + `state` + `nonce`; scopes `openid email profile`; `access_type=online`;
   `prompt=select_account`; `hd` sent only as a UI hint. Single redirect URI: `https://auth.apps.<domain>/oauth2/callback`.
2. The Google Cloud OAuth consent screen is configured as **Internal** (external accounts are rejected by Google).
3. ID token validation:
   - `coreos/go-oidc` v3 `Verify`: signature, `iss`, `aud`, `exp`.
   - Explicit checks (go-oidc does not do them): `nonce` (constant-time), `email_verified == true`,
     `hd` present and in the allowed-domains list (personal accounts carry no `hd`).
   - Identity is `sub` (mapped to `users.google_sub`); email is never an identifier.
4. No Google token is stored (no refresh token, no access token).
5. The Google issuer is accepted with and without scheme by `coreos/go-oidc` v3 only when the provider is created with
   `https://accounts.google.com` (special case in `verify.go`); `SkipIssuerCheck` is never used.

## Configuration and availability

Variables, formats and files: [configuration.md](configuration.md#platform-api).

- The three Google variables go together; without Google, `PLATFORM_FAKE_LOGIN=true` (development only, ignored with a warning when Google is configured) is required.
- The Google issuer is fixed to `https://accounts.google.com`; it is never configurable through the environment.
- Google discovery runs in the background with backoff and never blocks startup. Until it is ready, `/login`
  without a session and `/oauth2/callback` answer 503 with `Retry-After`. `/readyz` and `/forwardauth` do not
  depend on Google: users with live sessions keep working during a Google outage.
- The flow is consumed only when the callback `state` matches it (`UPDATE ... WHERE id AND state`), so a forged
  callback with a foreign `state`, or `?error=` without the right `state`, never cancels a login in progress.
  The login cookie is cleared only when the flow was actually burned.
- Callback outcomes, always with generic pages that never reflect request parameters:
  - 400: no matching flow, missing code, Google rejected the code (`invalid_grant` and other 4xx), invalid ID token,
    `?error=` with the right `state`.
  - 403: `hd` missing or not allowed, `email_verified` not true, `sub` empty or using the reserved `fake:` prefix,
    disabled user.
  - 503 with `Retry-After`: Google unreachable, 5xx or 429 from the token endpoint, redirects from Google
    (never followed, so the client secret is never re-sent), deadline exceeded, or Google keys unavailable.
- Two tabs: a second `/login` replaces the login cookie, so the latest flow completes and the older tab gets a 400
  without affecting it.

`client_secret` rotation: Google allows two active secrets per OAuth client; there is no automation, only a manual runbook
(add a secret, deploy it, disable the old one, delete it): [Google client secret rotation](../runbooks/google-client-secret-rotation.md).

## Session tables

| Table | Cookie | Lifetime |
|---|---|---|
| `oauth_flows` | `__Host-Http-platform_login` (auth host) | 10 min; holds `state`, `nonce`, PKCE verifier, `rd`, `bind` hash; consumed atomically |
| `sessions` | `__Host-Http-platform_session` (auth host) | 8 h absolute + 30-60 min idle; `last_seen_at` updated with throttling, refreshed by any app request |
| `app_sessions` | `__Host-Http-platform_app` (each app host) | Never beyond the central session; FK to `sessions` with `ON DELETE CASCADE` |
| `login_codes` | none (URL of the callback) | 30 s, single use; bound to session, app, target host and `bind` hash |

## Rules

1. Cookie values are 256-bit random; only their SHA-256 is stored. All cookies: `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`,
   no `Domain` (`Strict` would break the return from Google and links from mail or chat).
2. A new session id is issued on every login (no session fixation).
3. Single use is atomic: `UPDATE ... SET consumed_at = now() WHERE consumed_at IS NULL RETURNING ...`.
4. Login CSRF / session swapping: a `login_code` is accepted only by the browser holding the matching
   `__Host-Http-platform_bind` cookie set on the app host when the login started.
5. `rd` must target a host registered in `deployments` (https only); anything else is rejected.
6. Revocation is deletion: user (all `sessions`, cascading to `app_sessions`), app (its `app_sessions`).
   A disabled user loses access within the 30 s decision cache; this is the defense against suspended
   Workspace accounts (Google sends no RISC events for Workspace users).
7. Logout: `POST /_platform/logout` with an `Origin`/`Sec-Fetch-Site` check revokes the central session (and its
   app sessions) in the database, then shows a "signed out" page on the auth host instead of redirecting to login
   (Google keeps its own session and would sign the user back in silently).
8. Rate limits on `/login` and `/oauth2/callback`; expired flows, codes and sessions are purged by a job.
9. Audit: logins, logouts, denials; users can list and close their active sessions.
10. IP / User-Agent changes are logged and alerted, never used to cut the session (NAT and mobile networks).

## Cookies

All platform cookies use the `__Host-Http-` prefix. Names: `__Host-Http-platform_session`, `__Host-Http-platform_app`,
`__Host-Http-platform_login`, `__Host-Http-platform_bind`. The server always sets `Secure`, `HttpOnly`, `Path=/` and no
`Domain`. Support for the `Http` part: Chrome 140+, Firefox 143+. Browsers without it still apply the `__Host-` rules because the name starts with
`__Host-`, so the cookie is never weaker.

## Pinned dependencies

golang-jwt/jwt v5.3.1, coreos/go-oidc v3.21.0, golang.org/x/oauth2 v0.37.0, jackc/pgx v5.11.0.

## Limitations

- `hd` for users of a secondary domain is undocumented by Google: the allowlist is a configurable list of `hd` values,
  rejections log `hd` and email, and it must be tested with a real secondary-domain user before production. Email
  aliases and user alias domains never reach the ID token: Google signs in only with the primary address.
- Suspended Workspace users: the cutoff is the local `disabled` flag plus the 8 h session cap. Offboarding must disable
  the user in the portal: that revokes their sessions and API tokens ([API tokens](api.md#tokens)). A user suspended in
  Google but not disabled in the portal keeps working tokens up to the personal cap (30 days by default). A faster cutoff
  would need the Admin SDK Directory API.
