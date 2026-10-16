# platform-api contract

Status: v1 complete (2026-10-14); block 6 added (2026-10-15). Blocks were closed one at a time; research notes live in Engram (`architecture/portal-assertion-research`).

| Block | Topic | Status |
|---|---|---|
| 1 | Assertion JWT received by apps | Closed (2026-10-14) |
| 2 | ForwardAuth contract (Traefik <-> platform-api) | Closed (2026-10-14), verified in spike |
| 3 | Signing keys, rotation, JWKS | Closed (2026-10-14) |
| 4 | PostgreSQL data model | Closed (2026-10-14); amended by block 6 |
| 5 | Google login and sessions | Closed (2026-10-14) |
| 6 | Portal app: home, administration, observability | Closed (2026-10-15); in progress (schema and read API done) |

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
   An app that serves probes on a separate internal listener (the portal) sets the exempt path to a route its
   public mux never serves, so the public listener has no unauthenticated path.
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
| Host belongs to the portal app (`PLATFORM_PORTAL_APP_ID`, block 6) | 200 + `Platform-Assertion` with empty `roles` for any active user, while the app is active and its initiative is active |
| Authenticated, no access to the app (no grant, user disabled, app disabled, initiative inactive) | 403 (HTML for navigation, JSON for API) |
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
| `apps` | `id` = `app_<ulid>` (the `aud`); belongs to one initiative; `status` active/disabled (block 6) |
| `deployments` | `host` UNIQUE, lowercase; maps a host to an app (an app may have several hosts); `last_seen_at` and last probe result (block 6) |
| `initiative_access` | Grant: initiative + role + exactly one of user/group (CHECK), optional `expires_at`, `granted_by`, `granted_at`; FK `(initiative_id, role_id)` -> `initiative_roles (initiative_id, id)` |
| `platform_admins` | Platform-wide administrators, separate from initiative roles |
| `audit_events` | Admin changes (actor, action, target, before/after jsonb, nullable `initiative_id` for scoping, block 6). Append-only: the app role has INSERT and SELECT only |
| `access_denials` | Deny decisions, partitioned by month; retention 365 days by dropping partitions |

### Rules

1. Decision: allow iff the user is active, the host has a deployment, the app is active, the app's initiative is active,
   and a non-expired grant exists for the user directly or through a group. `roles` = union of the granted role keys
   for that initiative. The only exception is the portal app (block 6): any active user, empty `roles`.
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

### Configuration and availability

| Variable | Meaning |
|---|---|
| `PLATFORM_GOOGLE_CLIENT_ID` | OAuth client id (not secret) |
| `PLATFORM_GOOGLE_CLIENT_SECRET_FILE` | File with the client secret; same permission policy as the KEK (regular file, at most 0640) |
| `PLATFORM_ALLOWED_HD` | Comma-separated allowlist of `hd` values, lowercase, no empty entries |
| `PLATFORM_FAKE_LOGIN` | Development only. Ignored (with a warning) when Google is configured: the two never coexist |

- The three Google variables go together; without Google, `PLATFORM_FAKE_LOGIN=true` is required.
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

- Verified live (2026-10-16, Cloud Identity Free on `wysted.dev`): a primary-domain user signs in with `hd = wysted.dev`,
  is created just in time, gets 403 without grants and 200 right after a grant (no restart).
- `hd` for alias and secondary domains is undocumented by Google: the allowlist is a configurable list of `hd`
  values, rejections log `hd` and email, and it must be tested with real alias and secondary-domain accounts before production.
- Suspended Workspace users: v1 relies on the local `disabled` flag plus the 8 h session cap. A faster cutoff needs the
  Admin SDK Directory API (`users.get`, scope `admin.directory.user.readonly`, domain-wide delegation granted by a super admin);
  decided later together with the company's offboarding process.

## 6. Portal app: home, administration, observability

The portal is a product adopted by many companies, so nothing here assumes a specific observability stack or VPN.
Research notes: Engram `architecture/portal-web-stack-research`, `architecture/portal-web-csp-spike`,
`architecture/portal-contract-admin-security`.

### Shape and process

- One web app, `portal`, on its own host behind the same ForwardAuth: the home ("which apps can I open") for every
  user, plus administration screens shown according to the caller's permissions. The auth host never serves this UI:
  an XSS there would compromise the login of every app.
- Separate process from platform-api: same Go module, binary `cmd/portal`, its own Deployment, host and database role.
  It never holds the KEK or any signing key.
- It is an app like any other: registered in `apps`/`deployments` under the reserved initiative `platform`, and it
  validates the assertion with `sdk/go` (`aud` = its app id). `PLATFORM_PORTAL_APP_ID` tells platform-api which app
  gets the "any active user" rule (block 2); the id is used instead of a host because hosts can change and be several.
  The rule applies only to an app of the reserved initiative `platform`: the decision checks it every time (fail
  closed), and platform-api logs a WARN at start-up if the id points elsewhere. `access_changed` is emitted by
  `deployments` only on INSERT, DELETE or a change of host or app_id; telemetry columns do not emit it.
- JSON API first (`/api/v1/*`, `application/problem+json` errors), web UI on top. The API also replaces
  `grant.sh`/`revoke.sh`. Permissions are always resolved by the API; the UI only hides what the caller cannot do.
- Go tooling (build, test, vet, lint) always runs in workspace mode (`platform/go.work`): `cmd/portal` imports
  `sdk/go` without a `require`/`replace`, exactly like `examples/hello`. A build of `platform/api` outside the
  workspace does not resolve the SDK.
- Web UI in `platform/web/`: Nuxt 4 with `ssr: false` + `nuxi generate` (and `experimental.entryImportMap: false`),
  embedded in the Go binary with `embed.FS`, SPA fallback to `200.html`. No Node runtime in production.

### Who administers what

| Action | Platform admin | Initiative manager (holds a role with `manages_access`) |
|---|---|---|
| Create, rename, deactivate initiatives | Yes | No |
| Apps (create, rename, disable) and deployments (add or remove hosts) | Yes | No |
| Initiative roles (create, delete, `manages_access`) | Yes | No: role keys are part of the app's contract (`roles` claim) |
| Grants in the initiative (user or group to role, `expires_at`), including `manages_access` roles | Yes | Yes, only in their initiatives |
| Revoke grants in the initiative | Yes | Yes, except the last `manages_access` grant of the initiative |
| Groups and members | Yes | List groups only (id, key, name; never members), to grant access to a group. No changes: groups are global, a change affects every initiative |
| Disable users, revoke their sessions | Yes | No |
| `platform_admins` | No: CLI or SQL only (bootstrap) | No |
| Audit log | All | Only events of their initiatives |

A manager can name another manager: they already control every grant of the initiative, so it adds no privilege.
Roles, groups and hosts are the escalation paths, and they stay with platform admins.

### Authorization of portal calls

1. Identity comes only from the verified assertion (`sub`). Nothing about permissions travels in the assertion:
   it would be stale for its whole lifetime.
2. On every request the portal loads the caller's permissions from the database: platform admin or not, and the
   initiatives where a non-expired grant with `manages_access` exists (directly or through a group).
3. A pure function `adminz.Decide(subject, action, resource)` (AuthZEN-shaped, like `access.Decide`) answers each
   call, with table tests. Actions are named `<resource>.<verb>`: `grant.create`, `app.disable`, `audit.list`.
4. The permission check and the mutation run in the same transaction. The "last manager" rule takes a row lock on
   the initiative's `manages_access` grants inside that transaction, so two managers revoking each other at the
   same time cannot leave the initiative without one.
5. 403 carries no detail about other resources. A resource of an initiative the caller does not manage answers 404,
   so its existence is not confirmed.

### Request protection (sibling apps are same-site)

Every app lives under the same registrable domain, so `hello.apps.<domain>` and `portal.apps.<domain>` are same-site:
`SameSite=Lax` cookies travel between them. The defense is same-origin, through Fetch Metadata. CSRF tokens are not
used; this follows Go's `http.CrossOriginProtection` design and deliberately departs from the OWASP cheat sheet,
which still suggests tokens as an extra layer.

Middleware order:

1. Recover and request logging.
2. Read isolation for `/api/*`: reject any `Sec-Fetch-Site` value other than `same-origin` and `none` (`same-site`,
   `cross-site` and unknown values) before authenticating, and send `Vary: Sec-Fetch-Dest, Sec-Fetch-Mode,
   Sec-Fetch-Site`. A missing header is allowed (non-browser client, same choice as Go).
3. `http.CrossOriginProtection` for non-safe methods. Verified in Go 1.27.2: `same-origin` and `none` pass, any other
   `Sec-Fetch-Site` is rejected; without it, `Origin` must match `Host` (scheme not compared, so HSTS matters);
   without both, the request passes.
4. Mutations accept only `Content-Type: application/json` (parsed with `mime.ParseMediaType`, parameters allowed),
   otherwise 415. Handlers never call `ParseForm` or `FormValue`.

Response headers on every portal response. The Content-Security-Policy below is the one of the web UI; API
responses (`/api/*`, problem documents) carry `default-src 'none'; frame-ancestors 'none'` instead, and the UI
handler is the only place that replaces it with the full policy:

- `Content-Security-Policy: default-src 'none'; script-src 'self' 'sha256-<inline>'; style-src 'self';
  img-src 'self'; font-src 'self'; connect-src 'self'; form-action 'self' https://auth.apps.<domain>;
  frame-ancestors 'none'; base-uri 'none'`. At startup the server computes the SHA-256 of every inline script in the
  embedded HTML (only `window.__NUXT__` remains, and it changes with every build). Hashing whatever is there is safe:
  the HTML is a build artifact inside the binary and, without SSR, no user data ever reaches it. `form-action`
  includes the auth host because Chrome applies it to the 303 that follows the logout POST.
- `Strict-Transport-Security` with `includeSubDomains`, `X-Frame-Options: DENY`, `Cross-Origin-Opener-Policy: same-origin`,
  `Cross-Origin-Resource-Policy: same-origin`, `Referrer-Policy: no-referrer`, `X-Content-Type-Options: nosniff`.
- Never any `Access-Control-*` header.

Rules:

- No `GET`/`HEAD`/`OPTIONS` changes state.
- A future WebSocket validates `Origin` explicitly: its handshake is a GET, which `CrossOriginProtection` skips.
- The portal reads no cookies at all (Traefik turns the app cookie into the assertion). Any future cookie must use
  the `__Host-` prefix: siblings can plant cookies on the parent domain.
- `r.Host` is never rewritten from `X-Forwarded-Host`; Traefik keeps `passHostHeader: true` for the portal route.

Required tests: POST with `Sec-Fetch-Site: same-site` or `cross-site` -> 403; POST with a sibling `Origin` and no
`Sec-Fetch-Site` -> 403; `text/plain` -> 415; `application/json; charset=utf-8` -> accepted; GET `/api/*` with
`same-site` -> 403; no response carries `Access-Control-*`; the CSP hash matches the embedded HTML.

### Audit

1. Every mutation writes one `audit_events` row in the same transaction: if the insert fails, the mutation rolls
   back. (Login and logout audit stays best-effort: a login must not fail because of the audit.)
2. Each row carries the acting user, `action` (same names as `adminz`), target, `before`/`after` and `initiative_id`
   when the target belongs to one (NULL for groups, users and other global targets).
3. Deletions keep the full `before`: revoking a grant deletes the row, removing a deployment deletes the host.
4. Revoking sessions and disabling users from the portal are audited too, and so are `RevokeUser`/`RevokeApp`
   from platform-api.

### Data model changes

| Change | Why |
|---|---|
| `audit_events.initiative_id uuid NULL` (no FK, like the rest of the table) + index `(initiative_id, occurred_at)` | Managers filter by column instead of parsing jsonb |
| `apps.status` active/disabled; new decision reason `app_disabled` | Deactivate instead of delete |
| `deployments.last_seen_at`, `last_probe_at`, `last_probe_status` | Integration health |
| Role `platform_portal`: CRUD on authorization tables except `users` (SELECT + `UPDATE (status, updated_at)` only: users are created just-in-time by platform-api and `google_sub` is never writable), SELECT+INSERT on `audit_events`, SELECT on `access_denials`, DELETE plus SELECT without the token hashes on `sessions` and `app_sessions`; nothing on `signing_keys`, `oauth_flows` or `login_codes` | Least privilege for the admin process |

Deactivate instead of delete: initiatives through `status` (already exists); apps through `status`; a deployment is
deleted (nothing references it, the audit keeps it); a role is deleted only without grants (the FK already blocks
it); a group is deleted only when it has no members and no grants. Grants by email resolve to exactly one active
user: zero or several matches fail (`users.email` is not unique, `google_sub` is the identity).

### API

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/me` | Profile and permissions (platform admin, managed initiatives), so the UI can hide actions |
| `GET /api/v1/me/apps` | Home: apps the caller can open, with host, initiative and roles |
| `GET, POST /api/v1/initiatives`; `GET, PATCH /api/v1/initiatives/{id}` | Initiatives (`status`, `name`) |
| `GET, POST /api/v1/initiatives/{id}/roles`; `PATCH, DELETE /api/v1/roles/{id}` | Roles |
| `GET, POST /api/v1/initiatives/{id}/apps`; `PATCH /api/v1/apps/{id}` | Apps (`status`, `name`) |
| `POST /api/v1/apps/{id}/deployments`; `DELETE /api/v1/deployments/{id}` | Hosts |
| `GET, POST /api/v1/initiatives/{id}/grants`; `DELETE /api/v1/grants/{id}` | Grants (subject by email or group id, role, optional `expires_at` in the future) |
| `GET, POST /api/v1/groups`; `DELETE /api/v1/groups/{id}`; `GET, POST, DELETE /api/v1/groups/{id}/members` | Groups (members: platform admins only) |
| `GET /api/v1/users?email=`; `PATCH /api/v1/users/{id}`; `DELETE /api/v1/users/{id}/sessions` | Users (`status`) and session revocation |
| `GET /api/v1/audit?initiative_id=&cursor=` | Audit log, newest first, keyset pagination |
| `GET /api/v1/apps/{id}/health` | Integration health |

URLs use ids, never keys. Lists use keyset pagination with an opaque cursor.

Errors are `application/problem+json`. When a mutation loses against concurrent transactions (deadlock or
serialization failure) on every one of its few attempts, the answer is `503` with `Retry-After`, never `500`;
mutations are idempotent by natural key, so the client may simply retry.

### Home

`GET /api/v1/me/apps` is one SQL query that applies exactly the rules of `access.Decide`: active user, active app,
active initiative, non-expired grant (direct or through a group). Invariant, enforced by a test over the seed data:
a deployment appears in the home if and only if `/forwardauth` would answer 200 for that user. Internally it has the
shape of an AuthZEN resource search (subject + action -> resources); no public AuthZEN endpoint is exposed until an
external consumer needs one.

The one exception to the invariant is the portal app itself: `/forwardauth` lets every active user in, but the home
never lists it (it is where the user already is, not one of "their" apps). The test enforces the exception explicitly.

### Observability

Everything critical works inside the portal, with no external tool; standard outputs feed whatever the adopter runs.

| Layer | Provided |
|---|---|
| Audit | `audit_events`, browsable in the portal and scoped by initiative for managers |
| Integration health (per app) | Orphan hosts (`unknown_host` denials, a host routed by Traefik with no deployment); deployments never seen (`last_seen_at`); apps without grants or without a manager; result of the last probe |
| Logs | JSON on stdout (already the case), for any collector |
| Metrics | `/metrics` in Prometheus format on the internal listener of each process: decisions by host and reason, logins by outcome, JWKS fetches, key state |
| Dashboards and alerts | Optional Grafana dashboards and alert rules versioned in `platform-gitops/`; the local overlay adds Prometheus and Grafana |

- `last_seen_at` is updated by platform-api from authorized requests, aggregated in memory and flushed at most once
  per minute per deployment; never one write per request.
- Probe: the portal periodically requests every deployment host through the public entrypoint without cookies and
  expects a 302 to the auth host. Anything else means the app is reachable without ForwardAuth (for example an
  IngressRoute without the middleware), the most dangerous failure and one platform-api logs can never show.
  A matching check belongs in gitops CI.

### Open items

- Non-browser clients (CLI, automation replacing `grant.sh`) have no way to authenticate to the portal API yet:
  today everything enters through the session cookie.
- `platform_runtime` still has write access to the authorization tables; once the portal owns administration it
  can be reduced to what ForwardAuth and login need (JIT users, sessions).
- Reka UI injects `<style>` in some components: verify each one used against `style-src 'self'`.
- The probe interval and its alerting threshold are set during implementation.
