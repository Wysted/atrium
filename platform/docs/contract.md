# platform-api contract

Status: v1 complete (2026-10-14). Blocks were closed one at a time; research notes live in Engram (`architecture/portal-assertion-research`).

| Block | Topic | Status |
|---|---|---|
| 1 | Assertion JWT received by apps | Closed (2026-10-14) |
| 2 | ForwardAuth contract (Traefik <-> platform-api) | Closed (2026-10-14), verified in spike |
| 3 | Signing keys, rotation, JWKS | Closed (2026-10-14) |
| 4 | PostgreSQL data model | Closed (2026-10-14) |
| 5 | Google login and sessions | Closed (2026-10-14) |

## 1. Assertion JWT

Injected by Traefik into every authorized request as the `Platform-Assertion` header
(listed in ForwardAuth `authResponseHeaders`). It is an identity assertion for one app,
not a bearer token to forward.

### JOSE header

| Field | Value |
|---|---|
| `alg` | `ES256` (only allowed value) |
| `kid` | Required; selects the key in the JWKS |
| `typ` | `platform-assertion+jwt` (RFC 8725 §3.11 explicit typing) |

### Claims

| Claim | Value | Notes |
|---|---|---|
| `iss` | `https://auth.apps.<domain>` | Exact match |
| `aud` | Opaque, immutable app id (`app_<ulid>`) | Never a hostname or slug; injected into each app by config |
| `sub` | Internal user UUID | Never the email or the Google `sub` |
| `email`, `name` | Profile data | Informational; never used as identifiers |
| `iat`, `nbf`, `exp` | `nbf = iat`, `exp = iat + 90s` | Re-issued on every request |
| `jti` | Unique per issuance | Log correlation only; apps keep no replay list |
| `sid` | Public session id | Never the cookie value; cannot authenticate |
| `roles` | `["editor", ...]` | Only role keys of the initiative that owns `aud`; capped count |

### App-side validation (implemented once in `sdk/go`)

1. Reject unless `alg == ES256` and `typ == platform-assertion+jwt` (checked explicitly: golang-jwt v5 and jwx do not validate `typ`).
2. Select the key by `kid` from a fixed JWKS URL; ignore `jku`/`x5u`. Unknown `kid`: refetch once, rate limited, then reject.
3. Verify signature, `iss`, `aud == own app id`, `exp`/`nbf`/`iat` with 30 s leeway; `exp` and `aud` are mandatory.
4. Read identity only from the verified assertion; ignore `X-Forwarded-User` and similar headers.
5. Exempt the health path (`/healthz`) from validation: kubelet probes do not go through Traefik.
6. Never log the assertion and never forward it to other services.

### SDK failure behavior

- Invalid assertion: 401 (`application/problem+json`).
- Signing keys unavailable (JWKS unreachable and no usable cache): 503, never 401; this is a platform failure, not a bad token.
- If a JWKS refetch fails after the cache expired, the last keys keep being used for up to 15 minutes (stale-if-error);
  failures are memoized only for a short backoff, and a refetch is never tied to the request that triggered it.
- Redirects from the JWKS endpoint are never followed. The JWKS travels over in-cluster HTTP; its integrity
  depends on the NetworkPolicy (apps may only talk to platform-api), and internal TLS is a later hardening step.

### Platform-side guarantees

1. Every request has any client-sent `Platform-Assertion` removed before ForwardAuth runs
   (Traefik only overwrites it when platform-api returns it, and some 2xx responses carry none).
2. Apps are reachable only from Traefik (NetworkPolicy); the signed assertion is the defense if that fails.

## 2. ForwardAuth contract

### Traefik configuration

Attached to the `websecure` entrypoint (applies to every router, no opt-out), in this order:

1. `strip-assertion` (`headers` middleware): removes any client-sent `Platform-Assertion`.
2. `forwardauth`: calls `platform-api /forwardauth` with
   - `authResponseHeaders: [Platform-Assertion]`
   - `authRequestHeaders` as an allowlist: `Cookie`, `Accept`, `Sec-Fetch-Mode`, `Sec-Fetch-Site`, `Origin`,
     `Access-Control-Request-Method`, `Access-Control-Request-Headers`, `X-Requested-With`, `User-Agent`
     (`Sec-Fetch-Site` and `Origin` are needed for the logout CSRF check)
   - bounded `maxResponseBodySize`
   - client `X-Forwarded-*` never trusted (entrypoint `forwardedHeaders` without `trustedIPs`)

Traefik always calls `/forwardauth` with GET; the original method arrives only in `X-Forwarded-Method` (verified in the spike).

### Listeners

platform-api serves two listeners so that `/forwardauth` is never reachable through a public route:

| Listener | Routes | Reachable from |
|---|---|---|
| internal | `/forwardauth`, `/.well-known/jwks.json`, `/healthz`, `/readyz` | Traefik (ForwardAuth) and app namespaces (JWKS) via NetworkPolicy |
| public | auth host browser routes: `/login`, `/oauth2/callback`, `/logout`, signed-out page, `/.well-known/jwks.json` | Traefik IngressRoute for the auth host only |

`/readyz` fails when the database is unreachable, when no signer is loaded, or on `KEKMismatch`.

### Inputs read by platform-api

`X-Forwarded-Host`, `X-Forwarded-Method`, `X-Forwarded-Uri`, `Cookie`, `Accept`, `Sec-Fetch-Mode`,
`Origin`, `Access-Control-Request-Method`. Ignored: `X-Forwarded-Prefix`, `X-Forwarded-Port`, `X-Forwarded-Proto`.
The app is resolved only from `X-Forwarded-Host` (exact, lowercase) against `deployments.host`.

### Decision table

| Case | Response |
|---|---|
| Host is the auth host | 200, no assertion |
| Host not in deployments | 403 |
| CORS preflight (`OPTIONS` + `Origin` + `Access-Control-Request-Method`) | 200, no assertion (CORS is the app's job) |
| `/_platform/callback?code=` with a valid one-time code bound to this host and to the browser's `__Host-Http-platform_bind` cookie | 302 to the stored `rd` + `Set-Cookie: __Host-Http-platform_app`, `Referrer-Policy: no-referrer` |
| No or invalid app cookie, navigation (`GET`/`HEAD` + HTML signals) | 302 to `https://auth.apps.<domain>/login?rd=<original url>&bind=<hash>` + `Set-Cookie: __Host-Http-platform_bind` (short-lived random value) |
| No or invalid app cookie, API/XHR | 401 |
| Authenticated, no access to the app | 403 (HTML for navigation, JSON for API) |
| Authorized | 200 + `Platform-Assertion` |
| platform-api or database failure | 503; fail closed, never 2xx |

### Request classification and edge rules

- Navigation vs API: only `GET`/`HEAD` can be navigation. When `Sec-Fetch-Mode` is present it decides alone
  (`navigate` = navigation, anything else = API, regardless of `Accept`); without it, an HTML `Accept` means navigation.
  Any other method, including a form `POST`, is API (401): a non-idempotent request is never redirected.
- The `__Host-Http-platform_bind` cookie is reused (and its lifetime renewed) when a well-formed one already exists,
  so parallel tabs or API calls do not overwrite each other's binding. API 401 responses also set it, with
  `login_url` pointing to `https://<host>/`.
- An invalid callback answers 403 and keeps the bind cookie: clearing it would let an attacker's link cancel the
  victim's login in progress. The code is burned either way.
- `access_denials` records a denial when the decision is evaluated (cache miss), not on every repeated 403.
- Host-to-app resolution and decisions are cached for 30 s and both caches are flushed by `access_changed`.
  Session state is never cached.

### Conventions

- Reserved paths on every app host: `/_platform/*` (`callback`, `logout`). Apps must not use them. `logout` accepts POST only (block 5).
- JSON errors use RFC 9457 `application/problem+json` with an extra `login_url` member, so app frontends know where to send the user on 401.
- Every `/forwardauth` response carries `Cache-Control: no-store`.
- One decision log line per request: decision, reason, host, app id, `sid`, `jti`. Never tokens or cookies.

### Verified in the spike (Traefik v3.7.14, 2026-10-14)

1. With `authRequestHeaders` as an allowlist, `X-Forwarded-Method/Host/Uri/Proto/For/Port` still reach platform-api
   (Traefik provides them); headers outside the list (`Authorization`, custom ones) do not. A client-sent
   `X-Forwarded-Host` is replaced by the real host.
2. `strip-assertion` (`customRequestHeaders` with an empty value) removes a client-sent assertion on the auth host
   and on preflight requests; authorized requests still receive the assertion issued by platform-api.
3. `maxResponseBodySize` is in bytes; a larger auth response is rejected with 401.
4. `trustForwardHeader` is deprecated in v3.7 (removed in the next major); it stays explicitly `false` and is
   revisited on the Traefik major upgrade.

## 3. Signing keys, rotation, JWKS

### Storage

- ECDSA P-256 keys (ES256) live in the PostgreSQL table `signing_keys`: `kid`, public JWK, encrypted private key, state, timestamps.
- Private keys use envelope encryption: encrypted with a key-encryption key (KEK) mounted from a Kubernetes Secret; the KEK never reaches the database.
- `kid` is the RFC 7638 JWK thumbprint (base64url SHA-256 of the public key).

### Lifecycle

| State | Signs | In JWKS | Duration |
|---|---|---|---|
| `next` | No | Yes | Created at bootstrap and right after every promotion (so it is normally published for ~30 days); promoted by rotation only if published for at least 1 h |
| `active` | Yes | Yes | 30 days |
| `retired` | No | Yes | 1 h (covers token TTL + leeway + JWKS cache) |
| removed | No | No | Row kept for audit, private key material deleted |

- Rotation is automatic; a single replica rotates at a time, guarded by a PostgreSQL advisory lock.
- Emergency rotation (suspected compromise): the key is removed from the JWKS immediately and a new one is promoted.
  Apps may keep accepting it until their JWKS cache expires (max 5 minutes); lowering `max-age` shrinks that window.
  If the JWKS is unreachable at that moment, stale-if-error (block 1) extends it up to 20 minutes (5 + 15),
  plus up to 30 s for a replica that missed the change notification.

### Failure and consistency rules

- There is always a `next` key, created right after every promotion, so an emergency revocation normally promotes a
  key apps already know. Revocation prefers a `next` published for at least 1 h; during the first hour after a
  promotion it takes the oldest one, and if none can be unsealed it activates a fresh key. Only if no `next` exists is a fresh key activated directly
  (apps then pay one rate-limited JWKS refetch, up to 60 s of rejections).
- A key is promoted only after its private key is unsealed successfully inside the same transaction.
- If a replica cannot load the current active key, it stops signing (fail closed) instead of keeping an old one.
- Rotation timestamps come from PostgreSQL `now()`, never from replica clocks.
- A replica that missed the `signing_keys_changed` notification may sign with a revoked key for up to 30 s
  (periodic reload); this stays inside the 5-minute emergency window above.
- Sealed private keys carry a format version byte, so the KEK can be rotated by re-sealing.
- Every key stores the fingerprint (`kek_id`) of the KEK that sealed it: a domain-separated, non-reversible
  derivation of the KEK, never the KEK itself. A replica whose KEK fingerprint differs from the `active`/`next` keys
  reports `KEKMismatch`, fails readiness, and refuses to rotate or revoke without touching any row.
  Only with a matching fingerprint is an undecryptable `next` treated as corrupt and replaced automatically.

### JWKS endpoint

- `GET /.well-known/jwks.json` with `Cache-Control: max-age=300`; public keys only.
- Apps fetch it through the in-cluster service URL, never through the public route, and refetch on unknown `kid` (rate limited, see block 1).

## 4. PostgreSQL data model (authorization)

RBAC with domains: a role only exists inside an initiative. Session, login and key tables belong to blocks 3 and 5.

```
users --< group_members >-- groups
  |                           |
  +---------+       +---------+
            v       v
         initiative_access   (subject: user XOR group)
            |  composite FK (initiative_id, role_id)
            v
  initiative_roles >-- initiatives --< apps (aud) --< deployments (host)
```

| Table | Purpose and key constraints |
|---|---|
| `users` | Created just-in-time on first login. `id` uuid = JWT `sub`; `google_sub` UNIQUE NOT NULL; `email`, `name` informational; `status` active/disabled |
| `groups`, `group_members` | Internal groups; PK `(group_id, user_id)`. Google Workspace group sync is a later extension |
| `initiatives` | `key` UNIQUE, `status` |
| `initiative_roles` | `UNIQUE (initiative_id, key)` and `UNIQUE (initiative_id, id)` (target of the composite FK); `manages_access` flag for delegated administration |
| `apps` | `id` = `app_<ulid>` (the `aud`); belongs to one initiative |
| `deployments` | `host` UNIQUE, lowercase; maps a host to an app (an app may have several hosts) |
| `initiative_access` | Grant: initiative + role + exactly one of user/group (CHECK), optional `expires_at`, `granted_by`, `granted_at`; FK `(initiative_id, role_id)` -> `initiative_roles (initiative_id, id)` |
| `platform_admins` | Platform-wide administrators, separate from initiative roles |
| `audit_events` | Admin changes (actor, action, target, before/after jsonb). Append-only: the app role has INSERT and SELECT only |
| `access_denials` | Deny decisions, partitioned by month; retention 365 days by dropping partitions |

### Rules

1. Decision: allow iff the user is active, the host has a deployment, the app's initiative is active, and a non-expired
   grant exists for the user directly or through a group. `roles` = union of the granted role keys for that initiative.
2. Fail closed by construction: no deployment or no grant means deny. Nothing is open by default.
3. The composite FK makes it impossible to grant a role of initiative A on initiative B.
4. No pre-provisioning by email: access binds to `google_sub`, which Google never reassigns. Users log in once
   (or receive access through a group) before direct grants.

### Conventions

- PostgreSQL >= 18 (`uuidv7()` built in); `timestamptz` everywhere; hosts and keys forced lowercase with CHECK.
- Access changes emit `NOTIFY`; platform-api keeps a decision cache (TTL 30 s) invalidated by `LISTEN`.

## 5. Google login and sessions

Research notes: Engram `architecture/portal-login-research`.

### Google OIDC

1. Authorization Code + PKCE (S256) + `state` + `nonce`; scopes `openid email profile`; `access_type=online`;
   `prompt=select_account`; `hd` sent only as a UI hint. Single redirect URI: `https://auth.apps.<domain>/oauth2/callback`.
2. The Google Cloud OAuth consent screen is configured as **Internal** (external accounts are rejected by Google).
3. ID token validation:
   - `coreos/go-oidc` v3 `Verify`: signature, `iss`, `aud`, `exp`.
   - Explicit checks (go-oidc does not do them): `nonce` (constant-time), `email_verified == true`,
     `hd` present and in the allowed-domains list (personal accounts carry no `hd`).
   - Identity is `sub` (mapped to `users.google_sub`); email is never an identifier.
4. No Google token is stored (no refresh token, no access token).

### Session tables

| Table | Cookie | Lifetime |
|---|---|---|
| `oauth_flows` | `__Host-Http-platform_login` (auth host) | 10 min; holds `state`, `nonce`, PKCE verifier, `rd`, `bind` hash; consumed atomically |
| `sessions` | `__Host-Http-platform_session` (auth host) | 8 h absolute + 30-60 min idle; `last_seen_at` updated with throttling, refreshed by any app request |
| `app_sessions` | `__Host-Http-platform_app` (each app host) | Never beyond the central session; FK to `sessions` with `ON DELETE CASCADE` |
| `login_codes` | none (URL of the callback) | 30 s, single use; bound to session, app, target host and `bind` hash |

### Rules

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

### Resolved doubts (research 2026-10-14, Engram `architecture/portal-open-doubts`)

- go-oidc v3 accepts Google's issuer with and without scheme only when the provider is created with
  `https://accounts.google.com` (special case in `verify.go`); never use `SkipIssuerCheck`.
- `client_secret` rotation: Google allows two active secrets per OAuth client; manual runbook
  (add secret, deploy it, disable the old one, delete it). No automation.
- Cookie prefix: all platform cookies use `__Host-Http-` (draft-ietf-httpbis-layered-cookies; Chrome 140+, Firefox 143+).
  Browsers without support still apply the `__Host-` rules because the name starts with `__Host-`, so it is never weaker.
  The server always sets `Secure`, `HttpOnly`, `Path=/` and no `Domain`. Names: `__Host-Http-platform_session`,
  `__Host-Http-platform_app`, `__Host-Http-platform_login`, `__Host-Http-platform_bind`.
- Pinned versions: golang-jwt/jwt v5.3.1 (no v6 exists), coreos/go-oidc v3.21.0, golang.org/x/oauth2 v0.37.0, jackc/pgx v5.11.0.

### Open items

- `hd` for alias and secondary domains is undocumented by Google: the allowlist is a configurable list of `hd`
  values, rejections log `hd` and email, and it must be tested with real alias and secondary-domain accounts before production.
- Suspended Workspace users: v1 relies on the local `disabled` flag plus the 8 h session cap. A faster cutoff needs the
  Admin SDK Directory API (`users.get`, scope `admin.directory.user.readonly`, domain-wide delegation granted by a super admin);
  decided later together with the company's offboarding process.
