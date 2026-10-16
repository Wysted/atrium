# platform-api contract

Status: v1 complete (2026-10-14); block 6 added (2026-10-15). Blocks were closed one at a time; research notes live in Engram (`architecture/portal-assertion-research`).

| Block | Topic | Status |
|---|---|---|
| 1 | Assertion JWT received by apps | Closed (2026-10-14) |
| 2 | ForwardAuth contract (Traefik <-> platform-api) | Closed (2026-10-14), verified in spike |
| 3 | Signing keys, rotation, JWKS | Closed (2026-10-14) |
| 4 | PostgreSQL data model | Closed (2026-10-14); amended by block 6 |
| 5 | Google login and sessions | Closed (2026-10-14) |
| 6 | Portal app: home, administration, observability | Closed (2026-10-15); API implemented, web UI and observability pending |

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
| metrics | `/metrics` only (`PLATFORM_METRICS_ADDR`, required) | the monitoring namespace only, via NetworkPolicy |

A NetworkPolicy filters by port, never by path, so `/metrics` has a listener of its own: it cannot share one that Traefik or the app namespaces reach.

`/readyz` fails when the database is unreachable, when no signer is loaded, or on `KEKMismatch`.

### Inputs read by platform-api

`X-Forwarded-Host`, `X-Forwarded-Method`, `X-Forwarded-Uri`, `Cookie`, `Accept`, `Sec-Fetch-Mode`,
`Origin`, `Access-Control-Request-Method`. Ignored: `X-Forwarded-Prefix`, `X-Forwarded-Port`, `X-Forwarded-Proto`.
The app is resolved only from `X-Forwarded-Host` (exact, lowercase) against `deployments.host`.

### Decision table

| Case | Response |
|---|---|
| Host is the auth host | 200, no assertion |
| Host not in deployments | 403 (an `unknown_host` row goes to `access_denials`, see below) |
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
- A host without deployment (an orphan host: Traefik routed it here but no app owns it) is recorded as an
  `unknown_host` denial with no user. The host is hostile input (it comes from the network), so recording is
  bounded at every step and never part of the response: the host must be a valid DNS name by the same rules as
  `deployments.host` (lowercase, labels of 1 to 63, at most 253 bytes; anything else is never recorded); a host is
  recorded at most once per 10 minutes per replica (marker cache of at most 10 000 hosts); each replica has a token
  bucket of 30 rows per minute with a burst of 30 for all hosts together; accepted hosts go through a bounded queue
  (128) to one background writer, so ForwardAuth never waits for the database for them. Over budget, with a full
  marker cache or a full queue, the host is dropped and counted (a WARN summarizes the drops once a minute).
  The real bound is therefore 30 rows per minute per replica sustained (43 200 per day), whatever an attacker
  sends. Block 6 reads these rows.
- Host-to-app resolution and decisions are cached for 30 s and both caches are flushed by `access_changed`.
  A host that is not a valid DNS name (same rules as `deployments.host`) is refused with the same 403 before any
  lookup: no query, no cache slot. Hosts that are valid but have no deployment are cached apart (at most 10 000,
  same TTL, also flushed by `access_changed`), so a flood of random names can never evict a known host.
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
  closed), and platform-api logs a WARN at start-up if the id points elsewhere. The portal process does the same
  check on its own `PORTAL_APP_ID` (WARN, never fails start-up); `PORTAL_APP_ID` (portal) and `PLATFORM_PORTAL_APP_ID`
  (platform-api) must hold the same id. `access_changed` is emitted by
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
| Create, rename, deactivate initiatives | Yes | No (read their own) |
| Apps (create, rename, disable) and deployments (add or remove hosts) | Yes | No (list the apps of their initiatives, with their deployments) |
| Initiative roles (create, delete, `manages_access`) | Yes | No: role keys are part of the app's contract (`roles` claim). They list the roles of their initiatives, needed to grant |
| Grants in the initiative (user or group to role, `expires_at`), including `manages_access` roles | Yes | Yes, only in their initiatives |
| Revoke grants in the initiative | Yes, except the last effective `manages_access` grant (last-manager rule below) | Same exception |
| Groups and members | Yes | List groups only (id, key, name; never members), to grant access to a group. No changes: groups are global, a change affects every initiative |
| Disable users, revoke their sessions | Yes (never themselves, never the last active platform admin) | No |
| `platform_admins` | No: CLI or SQL only (bootstrap) | No |
| Audit log | All | Only events of their initiatives |
| Integration health of an app | Yes | Yes, for the apps of their initiatives |
| Orphan hosts (hosts with no deployment) | Yes | No (they belong to no initiative) |

Administration rules, enforced inside the mutation's transaction:

- **Last effective manager.** A `manages_access` grant is *effective* when it is not expired and its subject has at
   least one active user: the grant's own user is active, or the group has at least one active member. A disabled
   user is no manager (the portal refuses them), so grants held only by disabled users do not keep an initiative
   managed. Revoking an effective `manages_access` grant fails with 409 if no other effective one remains in the
   initiative; revoking a grant that is not effective is always allowed. It applies to platform admins too: an
   initiative is decommissioned by deactivating it, never by removing its managers (an admin who needs to replace
   the managers grants first and revokes afterwards). It is enforced on revocation only: removing a group member or
   disabling a user can still leave an initiative without an effective manager, and a platform admin repairs it.
- **Lockout guards.** Disabling yourself fails with 409. Disabling a user never leaves zero active platform admins
   (409; two admins disabling each other at the same time cannot both succeed). The same kind of guard protects the
   way into the portal: the `platform` initiative cannot be deactivated (409; its key is immutable like every key),
   the portal app (`PORTAL_APP_ID` inside the `platform` initiative, the rule of the ForwardAuth evaluator, so a
   mistyped id that points elsewhere protects nothing) cannot be disabled (409) and its last deployment cannot be
   removed (409). Removing a deployment locks the app row (`FOR NO KEY UPDATE`) before counting, so two removals of
   the last two deployments cannot both succeed.
- **Immutable keys.** The key of an initiative, the key of a role (it is part of the `roles` claim), the id of an app
   and the initiative of an app never change after creation: a patch that names them is rejected (400, unknown
   field). Keys follow the schema (`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`; at most 63 characters for initiatives and 32 for
   roles); an app id is generated by the server (`app_` plus a ULID).
- **Role `manages_access`.** Only platform admins toggle it. Turning it off removes the role's grants from the managers,
   so it follows the last-manager rule exactly like a revocation: 409 when the role holds an effective manager grant
   and no grant of another role would remain effective; a role without an effective grant can always be turned off.
   A role is deleted only without grants (409).
- **Inputs.** Empty names are valid (the schema default is `''`; the UI shows the key). For initiatives,
   body validation precedes the existence check, so an invalid `PATCH` on a missing initiative answers 422; roles and
   apps are loaded first to authorize the call, so a missing role or app answers 404 before its body is validated.
- **Hosts.** A deployment host is a lowercase DNS name: at most 253 bytes, labels of 1 to 63 characters from
   `[a-z0-9-]` that neither start nor end with a hyphen, no trailing dot, no wildcard, no port, and a last label that
   is not all digits (no IP addresses); internationalized names come in their `xn--` form, whose labels are not validated as punycode (the lookup is an
   exact match on `X-Forwarded-Host`, so a malformed one is harmless). The auth host (the host of
   `PORTAL_ISSUER`) is rejected (422, `reserved`), and a host that already exists on any app, the portal's own hosts
   included, is a 409 (the unique index is the guard).
- **Lock order.** Grant revocation locks the initiative row (`FOR NO KEY UPDATE`) and then the initiative's
   `manages_access` grants ordered by id; so does a change of a role's `manages_access`, and an initiative `PATCH`
   takes the initiative row first. Any path that updates or deletes a role row locks the initiative first, role
   deletion included: its foreign-key check takes `FOR KEY SHARE` locks on the initiative's grants, which would
   cross the `FOR UPDATE` of a concurrent `manages_access` change and deadlock. The role is read again after the
   lock (gone: 404), and every catalog `UPDATE` that matches no row answers 404, never a silent success. The app row (`FOR NO KEY UPDATE`: app `PATCH`, deployment removal) is a leaf:
   nothing is locked while holding it. Grant creation takes the initiative row `FOR SHARE` first and reads the
   caller's permissions again, so a manager revoked a moment ago cannot finish an in-flight POST. Disabling a user
   locks the active platform admins ordered by id (`FOR NO KEY UPDATE`), then the target. No path takes explicit
   locks of both families. Inserts that reference `users` (grants, group members) take implicit `KEY SHARE` locks
   through their foreign keys, which never conflict with `FOR NO KEY UPDATE`: that compatibility is what rules out a
   cycle, so locks on `users` must never be raised to `FOR UPDATE`.
- **Accepted by design.** Audit `before`/`after` keep emails (the security log must stay readable after a user
   changes), and granting by email lets a manager learn that an email is a unique active user (inherent to naming
   people by email).

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
| `GET, POST /api/v1/initiatives`; `GET, PATCH /api/v1/initiatives/{id}` | Initiatives (`PATCH`: `status`, `name`; the list shows each caller only their initiatives) |
| `GET, POST /api/v1/initiatives/{id}/roles`; `PATCH, DELETE /api/v1/roles/{id}` | Roles (`PATCH`: `manages_access` only) |
| `GET, POST /api/v1/initiatives/{id}/apps`; `PATCH /api/v1/apps/{id}` | Apps with their deployments (`PATCH`: `status`, `name`) |
| `POST /api/v1/apps/{id}/deployments`; `DELETE /api/v1/deployments/{id}` | Hosts (read through the apps listing) |
| `GET, POST /api/v1/initiatives/{id}/grants`; `DELETE /api/v1/grants/{id}` | Grants (subject by email or group id, role, optional `expires_at` in the future) |
| `GET, POST /api/v1/groups`; `DELETE /api/v1/groups/{id}`; `GET, POST /api/v1/groups/{id}/members`; `DELETE /api/v1/groups/{id}/members/{user_id}` | Groups (members: platform admins only) |
| `GET /api/v1/users?email=`; `PATCH /api/v1/users/{id}`; `DELETE /api/v1/users/{id}/sessions` | Users (`status`) and session revocation |
| `GET /api/v1/audit?initiative_id=&cursor=` | Audit log, newest first, keyset pagination |
| `GET /api/v1/apps/{id}/health` | Integration health of an app (admins and managers of its initiative) |
| `GET /api/v1/health/orphan-hosts` | Hosts refused as `unknown_host` that have no deployment (platform admins only) |

URLs use ids, never keys. Lists use keyset pagination with an opaque cursor.

Errors are `application/problem+json`. When a mutation loses against concurrent transactions (deadlock or
serialization failure) on every one of its few attempts, the answer is `503` with `Retry-After`, never `500`;
mutations are idempotent by natural key (initiative key, role key, host, grant), so the client may simply retry;
creating an app has none (the id is generated), so a retry after an ambiguous failure can leave a second app.

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
| Integration health (per app) | Deployments never or no longer seen (`last_seen_at`); result of the last probe; apps without grants or without a manager; disabled apps and inactive initiatives |
| Integration health (platform) | Orphan hosts: `unknown_host` denials of a host routed by Traefik with no deployment |
| Logs | JSON on stdout (already the case), for any collector |
| Metrics | `/metrics` in Prometheus format on a dedicated metrics listener of each process: decisions by reason, logins by outcome, JWKS fetches, key state, probe results (catalog below) |
| Dashboards and alerts | Optional Grafana dashboards and alert rules versioned in `platform-gitops/`; the local overlay adds Prometheus and Grafana |

#### Metrics

Both processes (`platform-api` and `portal`) serve `GET /metrics` in the Prometheus text format on a **dedicated
metrics listener** (`PLATFORM_METRICS_ADDR`, `PORTAL_METRICS_ADDR`; both required and different from the other
addresses of the process; same server timeouts). One rule for both: `/metrics` is never served on the public or the
internal listener, which answer 404 (tests per process enforce it), because a NetworkPolicy cannot filter by path.
There is no authentication: the NetworkPolicy must allow only the monitoring namespace to reach the metrics port, and
no IngressRoute may route to it. Each process uses its own registry (never the global default one) with the Go runtime and process
collectors, built with `github.com/prometheus/client_golang` (pinned in `go.mod`).

**Cardinality rule (hard).** No label takes a value supplied by a client: not the Host, the path, the user agent,
the query, an email or a user id. Labels take a value from a closed set (decision, reason, outcome, result, status
class), a `ServeMux` route pattern (`Request.Pattern`, never `URL.Path`; a request rejected before the router is
`unmatched`) or a fixed text. The code that records an event clamps its strings to the closed set and reports
anything else as `other`, and a test per process sends thousands of requests with random hosts, paths, methods and
cookies and asserts that the number of series does not move and that none of those values is exposed.
`app_id` is **not** a label anywhere: the set of apps grows with every company that adopts the portal, it would
multiply every other label, and the per-app view already exists (decision log line, `last_seen_at`, probe status).

platform-api (`platform_`):

| Metric | Type | Labels |
|---|---|---|
| `platform_forwardauth_decisions_total` | counter | `decision`, `reason` (the values of the decision log line) |
| `platform_forwardauth_decision_duration_seconds` | histogram | `decision` |
| `platform_forwardauth_cache_lookups_total` | counter | `cache` (`decision`, `host`, `negative_host`), `result` (`hit`, `miss`) |
| `platform_logins_total` | counter | `outcome` (a fake login, development only, counts as `success`): `success`, `provider_refused`, `rejected`, `invalid_flow`, `failed`, `provider_unavailable`, `user_disabled`, `rate_limited`, `invalid_request`, `error` |
| `platform_logouts_total` | counter | `via` (`app`, `auth_host`); only logouts that revoked a session |
| `platform_jwks_requests_total` | counter | `listener` (`internal`, `public`), `status_class` |
| `platform_signing_key_loaded` | gauge | none; 1 when this replica can sign |
| `platform_signing_key_active_age_seconds` | gauge | none; absent while no signer is loaded |
| `platform_signing_key_kek_mismatch` | gauge | none; 1 when the KEK does not match the active or next keys |
| `platform_lastseen_pending_hosts` | gauge | none |
| `platform_lastseen_flush_errors_total` | counter | none |
| `platform_unknown_host_drops_total` | counter | `reason` (`invalid`, `budget`, `marker`, `queue`) |
| `platform_db_pool_{acquired,idle,total,max}_connections` | gauge | none |
| `platform_db_pool_{acquires,acquires_waited,acquires_canceled}_total`, `platform_db_pool_acquire_wait_seconds_total` | counter | none |

portal (`portal_`):

| Metric | Type | Labels |
|---|---|---|
| `portal_http_requests_total` | counter | `method` (standard methods, else `other`), `route`, `status_class` |
| `portal_http_request_duration_seconds` | histogram | `route` |
| `portal_contention_responses_total` | counter | none |
| `portal_probe_rounds_total` | counter | `result`: `written`, `inconclusive`, `skipped` (another replica holds the lock), `idle` (nothing due), `error` |
| `portal_probe_round_duration_seconds` | histogram | `result` |
| `portal_probe_interval_seconds` | gauge | none; the configured probe interval (`PORTAL_PROBE_INTERVAL`) in seconds, set at startup, `0` = probe disabled; used to gate the probe alerts |
| `portal_probe_last_write_timestamp_seconds` | gauge | none; Unix time (seconds) of the last probe round with result `written`; starts at the process start time and stays there while the probe is disabled |
| `portal_deployments_by_probe_status` | gauge | `status` (`ok`, `unprotected`, `unexpected`, `unreachable`, `never`) |
| `portal_probe_status_count_errors_total` | counter | none |
| `portal_db_pool_*` | as above | none |

Notes:

- `portal_deployments_by_probe_status` is a scrape-time collector: it counts `deployments` by `last_probe_status`
  (`never` for none yet) with a 30 s cache and a 2 s timeout, on every replica and whether or not the probe runs, so
  any replica reports the state of the database. It has no host label. If the count fails the gauge is absent for
  that scrape (a stale value would hide the problem), `portal_probe_status_count_errors_total` grows, and the failure
  is cached for the TTL.
- Counters of a vector with open label combinations (decisions, requests) appear with their first event;
  the small closed sets (logins, logouts, caches, probe rounds) start at zero.
- The pool metrics are read from `pool.Stat()` at scrape time.
- Audit write failures are not a metric: the portal writes the audit row in the same transaction as the change, so a
  failure is the failure of the request itself (`portal_http_requests_total` with `5xx`), and platform-api logs
  the best-effort session audit failures.

#### `last_seen_at`

- platform-api notes every authorized decision (a 200 with assertion, the portal app included; not denials,
  redirects, preflights or the auth host) in an in-memory map `host -> latest time`. Only hosts that resolved to a
  deployment get there, so the map is bounded by the number of deployments (and hard-capped at 10 000 entries).
  A request never writes to the database.
- A background flusher runs once per minute and writes the whole map with one statement, as `platform_runtime`;
  at most one write per minute per deployment and replica. Times come from the replica that served the request,
  capped at the database clock and never moving the column backwards. Rows are locked in id order with
  `SKIP LOCKED`: a flush never waits for, or deadlocks with, another replica or a deployment removal. The statement
  returns the hosts it skipped (an existing row locked by someone else); they stay in memory, with their time unless
  a newer request touched them, and are written by the next tick. Hosts without deployment and rows that already
  hold a newer time are done, not skipped.
- A failed flush is logged and its batch goes back to memory (newer observations win), to be retried on the next
  tick; it never affects a decision. At shutdown, after the listeners stopped, the flusher makes one last
  best-effort flush bounded to 5 seconds.
- The statement touches neither `host` nor `app_id`, so it emits no `access_changed`.

#### Probe

- The portal (not platform-api) probes every deployment host through the public entrypoint without cookies:
  `GET https://<host>/` with `Accept: text/html` and `Sec-Fetch-Mode: navigate` (so ForwardAuth classifies it as a
  navigation), redirects not followed, no cookies kept, 10 seconds per host, 8 hosts at a time. A protected host
  answers a 302 to the auth host login. The response body is never read or logged.
- Anything else means the app is reachable without ForwardAuth (for example an IngressRoute without the
  middleware), the most dangerous failure and one platform-api logs can never show. A matching check belongs in
  gitops CI.
- Classification, stored in `deployments.last_probe_status` together with `last_probe_at`:

| Result | Meaning |
|---|---|
| `ok` | 302, 303 or 307 (the temporary redirects ForwardAuth uses) whose `Location` is `https://<auth host>/login...` (auth host from `PORTAL_ISSUER`; host, including port, compared case-insensitively; path exactly `/login`) |
| `unexpected` | Any other redirect: 301 or 308, another host or path, `http`, credentials in the URL, a relative `Location` (it resolves against the deployment host), or a redirect without `Location` |
| `unprotected` | Any response that is not a redirect, whatever its status (2xx, 4xx, 5xx): the host was served without ForwardAuth. A 401, 403 or 503 counts too: ForwardAuth answers a cookie-less navigation with a 302. A platform-api outage would look the same, which is what the control sample below prevents |
| `unreachable` | DNS, TCP, TLS (including a certificate that does not verify) or timeout: nothing can be said about the host |

A host that has a deployment but no Traefik router answers a 404 from Traefik itself, and classifies as
`unprotected` too: nothing in the response tells the two apart. In the health report the combination `never_seen`
plus `unprotected` points to a missing or wrong route (no authorized traffic ever arrived); `unprotected` on a host
that was seen means a route without the ForwardAuth middleware.

- Every `unprotected` and `unexpected` result logs a WARN with host, app id and HTTP status.
- Configuration (portal process):

| Variable | Meaning |
|---|---|
| `PORTAL_PROBE_INTERVAL` | Time between rounds. Default `5m`; `0` disables the probe; otherwise between `1m` and `24h`. Each wait is the interval plus or minus 10 percent |
| `PORTAL_PROBE_ADDR` | `host:port` the probe connects to, whatever the deployment host is (the Traefik service inside the cluster). TLS server name and `Host` stay the deployment host. **Required when the probe is enabled**: start-up fails with a config error otherwise (set `PORTAL_PROBE_INTERVAL=0` to run without a probe) |
| `PORTAL_PROBE_CA_FILE` | Optional PEM bundle added to the system roots to verify the entrypoint certificate |
| `PORTAL_PROBE_INSECURE_TLS` | Development only, default `false`: skips the certificate verification. Logged at Error level on every start, like `PORTAL_ALLOW_PRIVILEGED_DB_LOGIN` |

- Single runner: every replica runs the loop, but a round takes a session-level PostgreSQL advisory lock (try, never
  waits) on a dedicated connection and probes only the deployments never probed or last probed more than half an
  interval ago (database clock). A replica that wakes up while another one holds the lock, or just after it
  finished, finds nothing to do. Results are written with one statement (same `SKIP LOCKED` rules as above) and emit
  no `access_changed`. The lock is released when the round ends, or by the database if the connection dies.
- Control sample: every round starts by probing the first host (by name) of the portal app (`PORTAL_APP_ID`), which
  is known to sit behind ForwardAuth, whether or not it is due. If the control result is not `ok`, the round is
  inconclusive: a platform-api or Traefik outage would make every host look `unprotected` or `unreachable`. Nothing
  is stored (the previous statuses stay), a WARN logs the control status and host, and the next tick retries. If the
  portal app has no deployment the round is skipped the same way, with a WARN. When nothing is due the control is
  not probed. The control is probed again at the end of the round, and if it is no longer `ok` the whole round is
  discarded (WARN, nothing stored): an outage that started during the round taints the results already measured.
  Results are therefore written once, after that second check, and the WARN per `unprotected` or `unexpected` host
  is logged then; a process killed in the middle of a round (SIGKILL) loses that round, which the next tick
  repeats.
- A round pins one pooled connection for its whole duration (the advisory lock), so `portal serve` raises its
  connection pool to at least 8 (a larger configured size is kept).
- The portal needs network egress to `PORTAL_PROBE_ADDR` over HTTPS. Because the dial address is fixed, the portal
  never resolves the host names that admins write into `deployments` (no blind SSRF through the probe): they only
  travel as SNI and `Host`.

#### Health of an app

`GET /api/v1/apps/{id}/health` (action `app.health`, scoped to the app's initiative: platform admins and the
managers of that initiative; anyone else gets 404). It is a read: no audit row, no side effect. The answer holds:

- `app`: the app with its deployments, each with `last_seen_at`, `last_probe_at` and `last_probe_status` (`null`
  means never; the same three fields appear in every app listing);
- `initiative_status`;
- `grant_count`: non-expired grants of the initiative, whoever holds them;
- `effective_manager_grants`: `manages_access` grants that are effective by the definition of the last-manager rule
  (same SQL expression);
- `problems`: a list of `{code, host}` (`host` is `null` for findings about the app as a whole), app-level findings
  first and then each deployment in host order;
- `checked_at`.

| Code | Scope | When |
|---|---|---|
| `app_disabled` | app | `apps.status` is `disabled` |
| `initiative_inactive` | app | the initiative is `inactive` |
| `no_grants` | app | `grant_count` is 0 |
| `no_manager` | app | `effective_manager_grants` is 0 |
| `unprotected` | host | last probe `unprotected` |
| `unexpected_redirect` | host | last probe `unexpected` |
| `unreachable` | host | last probe `unreachable` |
| `never_probed` | host | no probe result yet (probe enabled) |
| `probe_stale` | host | `last_probe_at` older than 3 probe intervals (probe enabled): rounds are not completing, for example the control sample keeps failing |
| `probe_disabled` | app | the portal runs with `PORTAL_PROBE_INTERVAL=0`; replaces `never_probed` and `probe_stale` (a status stored earlier is still reported) |
| `never_seen` | host | `last_seen_at` is null |
| `not_seen_recently` | host | `last_seen_at` older than 24 hours |

`never_seen` and `not_seen_recently` are not reported while the app is disabled or its initiative inactive: nobody
can be authorized, so no traffic is expected. Probe findings are always reported.

#### Orphan hosts

An orphan host belongs to no app, hence to no initiative, so it is not part of any app's health.
`GET /api/v1/health/orphan-hosts` (action `orphan_host.list`, platform admins only; others get 403) lists the
hosts with `unknown_host` denials in the last 7 days that have no deployment now (a host that was added afterwards
is no longer an orphan), most recent denial first, at most 100: `{hosts: [{host, count, last_denied_at}]}`. `count`
is the number of recorded denials; platform-api records at most one per host every 10 minutes per replica (block 2),
so it measures how long a host has been hit, not how many requests it got. The query reads `access_denials`, which
the `platform_portal` role may select; no extra index is kept: `unknown_host` rows are bounded to 30 per minute per replica (block 2), so a week holds at most about 300 000 per replica, and the endpoint is admin-only.

### Open items

- Non-browser clients (CLI, automation replacing `grant.sh`) have no way to authenticate to the portal API yet:
  today everything enters through the session cookie.
- `platform_runtime` still has write access to the authorization tables; once the portal owns administration it
  can be reduced to what ForwardAuth and login need (JIT users, sessions).
- Reka UI injects `<style>` in some components: verify each one used against `style-src 'self'`.
