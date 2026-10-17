# Portal API

The JSON API of the `portal` process: who may do what, how calls are authorized and protected, and every endpoint. The web UI is a client of this API; permissions are always resolved by the API and the UI only hides what the caller cannot do. Rationale: [ADR 0006](../decisions/0006-portal-as-a-separate-process-and-database-role.md), [ADR 0007](../decisions/0007-fetch-metadata-instead-of-csrf-tokens.md).

## Conventions

- `/api/v1/*`, errors as `application/problem+json`. URLs use ids, never keys. Lists use keyset pagination with an opaque cursor.
- When a mutation loses against concurrent transactions (deadlock or serialization failure) on every one of its few
  attempts, the answer is `503` with `Retry-After`, never `500`; mutations are idempotent by natural key (initiative
  key, role key, host, grant), so the client may simply retry; creating an app has none (the id is generated), so a
  retry after an ambiguous failure can leave a second app.

## Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/me` | Profile and permissions (platform admin, managed initiatives), so the UI can hide actions |
| `GET /api/v1/me/apps` | Home: apps the caller can open, with host, initiative and roles |
| `GET, POST /api/v1/initiatives`; `GET, PATCH /api/v1/initiatives/{id}` | Initiatives (`PATCH`: `status`, `name`; the list shows each caller only their initiatives) |
| `GET, POST /api/v1/initiatives/{id}/roles`; `PATCH, DELETE /api/v1/roles/{id}` | Roles (`PATCH`: `manages_access` only) |
| `GET, POST /api/v1/initiatives/{id}/apps`; `PATCH /api/v1/apps/{id}` | Apps with their deployments (`PATCH`: `status`, `name`). Each app carries a read-only `portal_app` boolean (list, create, patch and health payloads): true for the configured `PORTAL_APP_ID` inside the `platform` initiative, the rule of the lockout guards, so the UI can hide disable / remove-last-deployment |
| `POST /api/v1/apps/{id}/deployments`; `DELETE /api/v1/deployments/{id}` | Hosts (read through the apps listing) |
| `GET, POST /api/v1/initiatives/{id}/grants`; `DELETE /api/v1/grants/{id}` | Grants (subject by email, group id or robot id, role, optional `expires_at` in the future) |
| `GET, POST /api/v1/groups`; `GET, DELETE /api/v1/groups/{id}`; `GET, POST /api/v1/groups/{id}/members`; `DELETE /api/v1/groups/{id}/members/{user_id}` | Groups (members: platform admins only). `GET /groups?q=` filters by a case-insensitive substring of key or name (max 64 characters, `422` on `q` otherwise; wildcards are literal) and keeps the keyset pagination. `GET /groups/{id}` returns `id, key, name, created_at` with the same access as the list (admins and managers; `404` for an unknown id), never members |
| `GET /api/v1/users?email=` (humans only); `PATCH /api/v1/users/{id}`; `DELETE /api/v1/users/{id}/sessions` | Users (`status`) and session revocation |
| `GET, POST /api/v1/me/tokens`; `DELETE /api/v1/me/tokens/{id}` | Own personal tokens ([Tokens](#tokens)). `POST` returns the secret once and only from a browser session; lists never return `token_hash` |
| `GET, POST /api/v1/robots`; `GET, PATCH /api/v1/robots/{id}` | Robots (`PATCH`: `name`, `status`, `owner_user_id`). `GET /robots?status=active|disabled` filters by status (absent or empty = all; `422` on any other value, field `status`) and keeps the keyset pagination. Platform admins get `id, name, status, owner_user_id, owner_email, owner_name, created_at, last_used_at` (owner fields `null` without owner); managers may list and get id, name and status; a human or unknown id is 404 |
| `GET, POST /api/v1/robots/{id}/tokens`; `DELETE /api/v1/tokens/{id}` | Robot tokens (platform admins; `POST` from a browser session only); `DELETE` revokes any token |
| `POST /api/v1/tokens/revoke`; `DELETE /api/v1/me/tokens/current` | Revocation by secret: a platform admin posts `{token}` (a leaked secret from any scanner report); the request that carries an `admin` token revokes that same token (a `read` token gets 403). Both answer 204 |
| `GET /api/v1/audit?initiative_id=&cursor=` | Audit log, newest first, keyset pagination; each event carries `token_id` (the API token that made the request, `null` for a browser session) |
| `GET /api/v1/apps/{id}/health` | Integration health of an app (admins and managers of its initiative) |
| `GET /api/v1/health/orphan-hosts` | Hosts refused as `unknown_host` that have no deployment (platform admins only) |
| `/api/v1/areas*`; `GET /api/v1/users/{id}/tokens`, `GET /api/v1/users/{id}/robots` | Areas, their members and supervisors, and the lists used by area supervisors: see [Area API](#area-api) |

## Who administers what

| Action | Platform admin | Initiative manager (holds a role with `manages_access`) |
|---|---|---|
| Create, rename, deactivate initiatives | Yes | No (read their own) |
| Apps (create, rename, disable) and deployments (add or remove hosts) | Yes | No (list the apps of their initiatives, with their deployments) |
| Initiative roles (create, delete, `manages_access`) | Yes | No: role keys are part of the app's contract (`roles` claim). They list the roles of their initiatives, needed to grant |
| Grants in the initiative (user, robot or group to role, `expires_at`), including `manages_access` roles | Yes | Yes, only in their initiatives |
| Revoke grants in the initiative | Yes, except the last effective `manages_access` grant ([last effective manager](#administration-rules)) | Same exception |
| Areas, their members and supervisors | Yes | No |
| Groups and members | Yes | List groups only (id, key, name; never members), to grant access to a group. No changes: groups are global, a change affects every initiative |
| Disable users, revoke their sessions and tokens | Yes (never themselves, never the last active platform admin) | No |
| Robots (create, rename, disable, owner) and their tokens | Yes (token creation from a browser session only) | No (list id, name and status, to grant) |
| Own personal tokens (create, list, revoke) | Yes (any active user; `admin` scope only with admin permissions at issuance) | Same |
| Revoke any token | Yes | No |
| `platform_admins` | No: CLI or SQL only (bootstrap) | No |
| Audit log | All | Only events of their initiatives |
| Integration health of an app | Yes | Yes, for the apps of their initiatives |
| Orphan hosts (hosts with no deployment) | Yes | No (they belong to no initiative) |

Area supervisors are a third capacity, outside this table: from a browser session only,
they administer the tokens and robots of the people of their areas, never access ([Area supervisors](#area-supervisors)).
Supervising is not an admin permission at token issuance.

## Administration rules

Administration rules, enforced inside the mutation's transaction:

- **Last effective manager.** A `manages_access` grant is *effective* when it is not expired and its subject has at
   least one active **human** user: the grant's own user is an active human, or the group has at least one active
   member (groups hold only humans). A robot grant never counts, so every initiative keeps a human manager. A disabled
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
   The schema enforces the same rule (`deployments_host_valid`, migration 0011), so SQL cannot store a host that
   ForwardAuth would refuse. Adopters with older rows must fix them first: migration 0011 fails on any row that
   violates the rule (the query that finds them is in the migration comment).
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

## Authorization of portal calls

1. Identity comes only from the verified assertion (`sub`). Nothing about permissions travels in the assertion:
   it would be stale for its whole lifetime.
2. On every request the portal loads the caller's permissions from the database: platform admin or not, and the
   initiatives where a non-expired grant with `manages_access` exists (directly or through a group). Also the areas they supervise, empty under a token ([Area supervisors](#area-supervisors), browser session only;
   `portal.subject()` and `Service.Me` apply the token gate).
3. A pure function `adminz.Decide(subject, action, resource)` (AuthZEN-shaped, like `access.Decide`) answers each
   call, with table tests. Actions are named `<resource>.<verb>`: `grant.create`, `app.disable`, `audit.list`.
4. The permission check and the mutation run in the same transaction. The "last manager" rule takes a row lock on
   the initiative's `manages_access` grants inside that transaction, so two managers revoking each other at the
   same time cannot leave the initiative without one.
5. 403 carries no detail about other resources. A resource of an initiative the caller does not manage answers 404,
   so its existence is not confirmed.

## Request protection

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

## Audit

1. Every mutation writes one `audit_events` row in the same transaction: if the insert fails, the mutation rolls
   back. (Login and logout audit stays best-effort: a login must not fail because of the audit.)
2. Each row carries the acting user, `action` (same names as `adminz`), target, `before`/`after` and `initiative_id`
   when the target belongs to one (NULL for groups, users and other global targets).
3. Deletions keep the full `before`: revoking a grant deletes the row, removing a deployment deletes the host.
4. Revoking sessions and disabling users from the portal are audited too, and so are `RevokeUser`/`RevokeApp`
   from platform-api.
5. Token-made mutations also record the token (`token_id`), see [Tokens](#audit-of-token-and-robot-actions).
   Supervisor actions also record the area (`area_id`), see [Area supervisors](#audit-of-area-actions).

### Audit of token and robot actions

Each mutation made with a token records the acting user or robot in `actor_id` and the token in the column
`audit_events.token_id uuid NULL` (no FK, like the rest of the table), with an index like `initiative_id` if filtering
needs it. A column, not jsonb: it is filterable, and `actor_id` alone cannot say which of the owner's tokens acted.
Actions: `token.create` (`after`: name, scope, `expires_at`, owner; never the hash), `token.revoke`, `robot.create`,
`robot.update` (name, status, owner). Disabling a user or robot records the ids of the revoked tokens in the
`user.update` / `robot.update` row (ids, not a count: each one can be traced). Reads and plain uses are not audited
(`last_used_at` is the trace). Audit listings, grants and user documents show a robot by its name (the email of a robot is null).

### Audit of area actions

The six area administration actions below write `area_id` NULL. Actions `area.create`, `area.delete` (target `area`, `before`/`after`: key and name), `area_member.add`,
`area_member.remove`, `area_supervisor.add`, `area_supervisor.remove` (target `area`, document: area id, user id and
email, like group members). Supervisor actions keep their names (`token.revoke`, `robot.update`) and set
`audit_events.area_id`: the capacity, so a reader can tell "as supervisor of area X" from "as owner" or "as platform
admin". It is set only when the permission came from the supervisor capacity (decision reason `area_supervisor`),
never for a platform admin, who passes `scopeArea` before the supervised areas are looked at. `initiative_id` stays NULL for all of them (global targets); managers never see these events. Supervisors get
no audit view in this design.

## Home

`GET /api/v1/me/apps` is one SQL query that applies exactly the rules of `access.Decide`: active user, active app,
active initiative, non-expired grant (direct or through a group). Invariant, enforced by a test over the seed data:
a deployment appears in the home if and only if `/forwardauth` would answer 200 for that user. Internally it has the
shape of an AuthZEN resource search (subject + action -> resources); no public AuthZEN endpoint is exposed until an
external consumer needs one.

The one exception to the invariant is the portal app itself: `/forwardauth` lets every active user in, but the home
never lists it (it is where the user already is, not one of "their" apps). The test enforces the exception explicitly.

## Tokens

Personal access tokens (humans) and robot tokens are one mechanism, for CLI and automation that call this API. Table and secret format: [data model](data-model.md#api-tokens). How they authenticate: [ForwardAuth token path](forwardauth.md#token-path). Rationale: [ADR 0008](../decisions/0008-opaque-api-tokens-and-robots.md).

### Token rules

1. The secret is shown once, in the creation response; it is never retrievable and no endpoint returns `token_hash`.
2. Expiration is mandatory, with one cap per owner kind: `PORTAL_TOKEN_MAX_TTL_PERSONAL` (default `720h`, 30 days) and
   `PORTAL_TOKEN_MAX_TTL_ROBOT` (default `2160h`, 90 days). A human token must not outlive a Google suspension by much:
   the offboarding control is the local `disabled` flag plus the 8 h session cap ([login](login.md#limitations)), and a suspended but
   not disabled user keeps a working token up to the cap. Robots are not offboarded through Google, so they can
   afford a longer cap. Rotation stays routine (create the new token, switch, revoke the old one; several tokens per
   owner overlap, at most 25 unexpired unrevoked ones). Raising a cap is the adopter's decision. A request without
   `expires_in` gets the cap of the owner's kind; a larger one is a 422.
3. Scope is a ceiling, never a grant. `read` allows only `GET` and `HEAD`; `admin` lifts the method restriction. In both
   cases the effective permission is the intersection of the owner's live permissions (resolved from the database on
   every request, as in [Authorization of portal calls](#authorization-of-portal-calls)) and the scope.
4. Permissions are checked at issuance and on every use. At issuance, `admin` is accepted only if the token's owner is
   at that moment a platform admin or holds `manages_access` in at least one initiative; otherwise 422. A token never
   carries more than its owner can do at issuance, and what it can do later shrinks with the owner (grant revoked,
   user or robot disabled).
5. Disabling a user or a robot revokes all their tokens (`revoked_at`) in the same transaction, like their sessions;
   re-enabling does not bring them back. ForwardAuth also checks `status` on every request. The revocation lives in the
   API path (`SetUserStatus` returns early when the status already matches): a user disabled by manual SQL, with no
   revocation, and then re-enabled through the API gets their still-valid tokens back. The supported way to disable is
   the API; after a manual disable, revoke the tokens by hand.
6. Housekeeping (the existing job in platform-api) deletes tokens 30 days after `expires_at` or `revoked_at`, so owners
   still see recently dead tokens. It calls `purge_dead_api_tokens(grace interval)`, a SECURITY DEFINER function with a
   pinned `search_path` that only deletes rows past the grace period and refuses a grace under 7 days (same pattern
   as `drop_expired_access_denials_partitions`, migration 0008); the runtime role has no DELETE on the table. The audit keeps name, scope and expiry in `before`/`after`.

### Issuance and revocation

1. Tokens are created only from a browser session: a request authenticated by a token (assertion with `tid`) can never
   create a token or a robot token, so a leaked token cannot extend itself. The portal checks it, not the client.
2. A human creates tokens only for themselves (`POST /api/v1/me/tokens`). Robot tokens are created only by platform
   admins, from a session (`POST /api/v1/robots/{id}/tokens`).
3. The owner revokes their own tokens; a platform admin revokes any token (and an area
   supervisor the tokens in their scope, from a browser session only, [Area supervisors](#area-supervisors)). A token may revoke tokens (own ones, or any if
   its owner is a platform admin and the scope is `admin`): containment must not require a browser.
   Revocation by secret, so a leak can be contained without knowing the token id:
   - `POST /api/v1/tokens/revoke` with `{token}`, platform admins only (session or `admin` token): the portal checks the
     format, hashes the secret and revokes the matching live token. It answers 204 whether or not a token matched,
     so the endpoint never confirms that a secret exists; a revocation is visible in the audit and the token list.
     It works with any secret scanner (GitHub, GitLab or an internal one): an admin pastes what the report contains.
   - `DELETE /api/v1/me/tokens/current` revokes the token that authenticates the request (RFC 7009 spirit). It is a
     `DELETE`, so it needs an `admin` token like any other mutation: no path is exempt from the `read` restriction
     (ForwardAuth never special-cases paths). A leaked `read` token is contained by its owner (revoke by id) or by a
     platform admin (revoke by secret). A request without a token (browser session) answers 404.
   - No public endpoint for scanner partner programs: they are provider-specific and would need a route outside
     ForwardAuth. An adopter that needs one adds it later.
4. Creation runs in one transaction that takes `LockUser(owner)` (`FOR NO KEY UPDATE`, the lock `SetUserStatus` takes)
   as its first lock, reads the owner's `status` again under it, counts the unexpired unrevoked tokens against the
   limit of 25 and then inserts. The lock is needed because the foreign key of the insert takes only `KEY SHARE` on
   the user, which does not conflict with a concurrent disable: without it, a token could be inserted after the
   disable revoked the others, and two parallel creations could pass the count together. It is a leaf lock (no other
   user lock is held with it), so it cannot join the lock orders of [Administration rules](#administration-rules). `LockUser` sees humans only, so robot
   tokens take the same lock (`FOR NO KEY UPDATE`) through the robot repository, which sees robots only.
5. Every creation and revocation is audited ([Audit](#audit-of-token-and-robot-actions)). Creation has no natural key, so a retry after an ambiguous failure
   can leave a second token; the client lists and revokes the extra one.

### Configuration

Configuration (portal process; both optional Go durations above zero, an invalid value fails start-up):

| Variable | Meaning |
|---|---|
| `PORTAL_TOKEN_MAX_TTL_PERSONAL` | Cap and default lifetime of a personal token. Default `720h` |
| `PORTAL_TOKEN_MAX_TTL_ROBOT` | Cap and default lifetime of a robot token. Default `2160h`; used by the robot token endpoint |

### Personal token API

`POST /api/v1/me/tokens` takes `{name, scope, expires_in}` (`expires_in` in seconds,
optional) and answers 201 with the token and its `secret`; the limit of 25 live tokens answers 409; an `admin` scope
the owner cannot hold at that moment, an `expires_in` above the cap or not positive, and a bad name or scope answer 422;
a request authenticated by a token answers 403. Lists are newest first with the keyset `cursor` and include recently
dead tokens (`last_used_at`, `revoked_at`). Revoking a token that is already revoked answers 204 and writes no audit
row; a token that is not the caller's answers 404 (`DELETE /api/v1/tokens/{id}` too, unless the caller is a platform
admin, or an area supervisor from a browser session only when the token is in their scope).
The audit action names are `token.create` and `token.revoke`; `adminz` uses `token.create_own`, `token.list_own`,
`token.revoke_own`, `token.revoke_supervised` (`DELETE /tokens/{id}` of another owner; platform admins pass it) and `token.revoke_any` (revocation by secret only). `POST /api/v1/tokens/revoke`
answers 422 for an empty or malformed `token` (the format check reveals nothing about existence, and runs after the
authorization check and before the lookup by hash), 403 for a caller who is not a platform admin, and 204 for every well-formed secret, matched or not;
only a match writes an audit row (`token.revoke`, the token id and its owner, never the secret).

### Request protection with tokens

A Bearer request carries no ambient credential: the browser never adds `Authorization` by itself, and to add it from a
page of another origin it needs a CORS preflight, which the portal never approves (no `Access-Control-*`). Fetch Metadata
stays as is: non-browser clients send no `Sec-Fetch-Site`, and a missing header is allowed ([Request protection](#request-protection), middleware order
2 and 3); a browser always sends it, so a browser-originated cross-site request is rejected whatever it carries.
Because the token path ignores cookies, a request cannot mix a victim's cookie with an attacker's token. Mutations keep
the JSON-only rule (415). The portal additionally enforces scope from the `scope` claim in a middleware after authentication and before the
routes: if `tid` is set, `scope` must be `read` or `admin` (anything else: 403, fail closed), and `read` means exactly
`GET` and `HEAD`. It does not reuse `isSafeMethod` (portalhttp `guard.go`), which also accepts `OPTIONS`. Defense in depth
if ForwardAuth and the portal ever disagree.

## Robots

Robot schema rules (kind, pins, grants): [data model](data-model.md#humans-and-robots).

`GET /api/v1/robots` lists by id with the keyset `cursor`: platform admins get `id,
name, status, owner_user_id, owner_email, owner_name, created_at, last_used_at` (the newest use of any of its tokens), managers get only `id, name,
status`, anyone else 403. `POST /api/v1/robots` takes `{name, owner_user_id?}` and answers 201; a duplicate name
(case-insensitive, among robots) is 409, a bad name or an owner that is not an active human is 422. `PATCH
/api/v1/robots/{id}` takes any of `name`, `status` (`active`/`disabled`) and `owner_user_id` (`""` clears it; JSON `null`
reads as absent); an empty patch is 422, a patch that changes nothing writes no audit row, disabling revokes the live tokens
(ids in the `after` of the `robot.update` row) and a human id is 404. `POST /api/v1/robots/{id}/tokens` takes the body of
the personal token (`expires_in` capped by `PORTAL_TOKEN_MAX_TTL_ROBOT`) and answers 201 with the secret, only from a browser
session (403 otherwise) and only for platform admins (403); an unknown or human id is 404, a disabled robot 409, the 25
live tokens 409, and `admin` scope for a robot without a current `manages_access` grant 422 (`not_allowed`).
`GET /api/v1/robots/{id}/tokens` lists them (admins, and area supervisors from a browser
session only for robots in their scope: `robot_token.list` is area-scoped, anyone else gets 404). The grant subject takes `robot_id` (exactly one of `email`,
`group_id`, `robot_id`, otherwise 422 on the three fields); an unknown, human or disabled robot is 422 `unknown` on
`robot_id`. `adminz` actions: `robot.list`, `robot.create`, `robot.update`, `robot_token.create`, `robot_token.list`.
Group membership is by email, which a robot does not have, so the members endpoints cannot receive a robot id.
`DELETE /api/v1/tokens/{id}`, revocation by secret and `DELETE /api/v1/me/tokens/current` (the robot's own token) serve
robot-owned tokens unchanged.

An owner's departure never disables their robots automatically.

## Area supervisors

An area supervisor administers the robots and API tokens of the people of an area, so offboarding a person's automation does not wait for a platform admin. Supervisors administer people's credentials and never access; they are a separate capacity, not the initiative manager. Tables: [data model](data-model.md#areas). Rationale: [ADR 0010](../decisions/0010-local-areas-with-admin-assigned-supervisors.md).

Area changes do not affect access decisions: no `NOTIFY`, and platform-api never reads these tables.

### Scope

1. A person is **supervisable** when they are a human member of an area, are not a platform admin and supervise no
   area. Disabled members stay supervisable: offboarding a departed person's robots is the main use. "Platform admin"
   and "supervises an area" are read from the `platform_admins` and `area_supervisors` rows whatever the person's
   status (fail closed): a disabled supervisor or admin is still not supervisable.
2. A supervisor reaches a supervisable person iff the supervisor is active and supervises that person's area.
   Through that person they reach:
   - the person's personal tokens;
   - the robots whose `owner_user_id` is that person, and those robots' tokens.

   A robot without owner, or owned by someone who is not supervisable, is reachable by platform admins only.
3. On what they reach, a supervisor may: list metadata (never secrets or hashes, which the portal cannot read anyway),
   revoke tokens, and change a robot's `owner_user_id`. The new owner must be an active supervisable person of an area
   the actor supervises (any of them, not necessarily the same area); clearing the owner is not allowed (422
   `not_allowed`: it would drop the robot out of every supervisor's scope).
4. Never: grants, roles, groups, initiative or app access (not even listing a robot's grants), disabling a user or a
   robot, renaming a robot, revoking sessions, creating tokens for anyone else, acting as the person.
5. Guards follow from rule 1. Because a supervisor is never supervisable, nobody reaches their own tokens or robots
   through this path, supervisors cannot administer each other (no chain: supervising an area gives nothing over
   another supervisor or over the areas they supervise) and platform admins are out of every scope. The predicate
   depends only on the target, not on the actor, so the repository resolves "the supervisable area of this person"
   and `adminz.Decide` compares it with the caller's supervised areas.
6. Scope is evaluated on every request inside the transaction of the use case, from the database, never cached.
7. Out of scope answers 404 ([Authorization of portal calls](#authorization-of-portal-calls), rule 5): a supervisor learns nothing about people,
   tokens or robots outside their areas.
8. The supervisor capacity is browser-session only: under a token (`tid`), `Supervised` is empty, so every
   area-scoped action answers as for any other caller (404). Reads included: a leaked `read` token of a supervisor
   must not list other people's tokens or robots. Supervising is not an administrative permission at token issuance
   (rule 4 of [Token rules](#token-rules) is unchanged), so a supervisor gets no `admin` token from it.

`owner_user_id` is still no access authority (ForwardAuth and grants ignore it), but it places a robot in its
owner's area: setting it decides which supervisors administer the robot.

### Authorization (`adminz`)

- `Subject` gains `Supervised` (area ids), loaded by the same permission query as `Managed`; `Resource` gains
  `AreaID`, the supervisable area of the target person (empty when none), resolved from storage by the use case.
- Rule 8 of [Scope](#scope) is implemented in two places: `portal.subject(ctx, tx, id)` builds the subject without
  `Supervised` when `TokenFrom(ctx) != ""` (every use case obtains its subject there, so no handler can
  forget it), and `Service.Me`, which does not use `subject()`, clears `supervised_areas` under a token.
- New rule scope `scopeArea`: allowed to platform admins whatever `AreaID` says (an admin reaches every person,
  whether or not they belong to an area), reason `platform_admin`; to a supervisor iff `AreaID` is non-empty and one
  of `Supervised`, reason `area_supervisor`; anyone else `not_found`. The reason is what tells the use case that the
  permission came from the supervisor capacity ([audit of area actions](#audit-of-area-actions)).
- Actions:

| Action | Scope | Used by |
|---|---|---|
| `area.create`, `area.delete`, `area.list`, `area_member.add`, `area_member.remove`, `area_supervisor.add`, `area_supervisor.remove`, `area_supervisor.list` | global (admins) | Area administration (`GET /areas/{id}` uses `area.list`) |
| `area_member.list` | area (`AreaID` = the area itself) | `GET /areas/{id}/members` |
| `user_token.list`, `user_robot.list` | area | `GET /users/{id}/tokens`, `GET /users/{id}/robots` |
| `robot_token.list` | area (was global) | `GET /robots/{id}/tokens` |
| `token.revoke_supervised` | area | `DELETE /tokens/{id}` on a token that is not the caller's |
| `robot.reassign` | area | `PATCH /robots/{id}` with `owner_user_id` only, by a caller who is not a platform admin; `AreaID` = the current owner's area (the new owner is a field check of the use case) |

`token.revoke_any` stays global (admins) and keeps authorizing revocation by secret: a supervisor never revokes by
secret (it would let them revoke tokens outside their scope, and the endpoint must not say whose token matched).

### Use cases

- **Revoke** (`DELETE /api/v1/tokens/{id}`): own token -> `token.revoke_own`; otherwise
  `token.revoke_supervised` with `AreaID` = supervisable area of the token's person (the owner of a personal token,
  the robot's owner for a robot token), which platform admins pass anyway. No lock: the revocation is one
  `UPDATE ... WHERE revoked_at IS NULL`. The audit row is `token.revoke`, with `area_id` set only when the
  decision reason is `area_supervisor` (never for a platform admin, even one who also supervises the area).
- **Reassign** (`PATCH /api/v1/robots/{id}`): a platform admin keeps the current path. Anyone else: the patch may
  carry only `owner_user_id` (403 otherwise), the scope is checked once before the lock (a caller with no scope locks
  nothing) and again after `LockRobot` from the locked row, so two concurrent reassignments (or an admin patch) of the
  same robot run one after the other and the second one is authorized against the owner the first one wrote.
  `adminz` sees only the current owner's area; the new owner is a validation of the use case, in the same
  transaction: active, supervisable, and with an area in the caller's `Supervised`; otherwise 422 `unknown` on
  `owner_user_id` (the existing code, which does not tell which condition failed). The audit row is `robot.update`
  (`before`/`after` owner) with `area_id` = the area of the previous owner (reason `area_supervisor`).
- **Lists**: `GET /users/{id}/tokens`, `GET /users/{id}/robots`, `GET /robots/{id}/tokens` and
  `GET /areas/{id}/members` answer the same documents as their admin counterparts (tokens as in `/me/tokens`;
  robots with owner fields and `last_used_at`). A supervisor's member list holds only supervisable members (exactly
  the people they can act on); platform admins see every member.

Races, under READ COMMITTED. The admin mutations on areas (remove a member, remove a supervisor, make someone a
platform admin or a supervisor) read nothing a supervisor action writes (tokens, `owner_user_id`), so a supervisor
action that read its scope just before a concurrent removal committed ends in the state of the serial order
"action, then removal": the same as if the admin had removed one second later. Unlike grant creation (which takes the
initiative `FOR SHARE` and reads the permissions again, so a revoked manager cannot finish an in-flight POST), no
area row is locked: a supervisor action never extends anyone's access (revocation removes, `owner_user_id` grants
nothing), so the in-flight window is harmless and the audit records it. A new owner disabled concurrently is the
same case as the admin path (`checkRobotOwner` reads without a lock): the robot keeps working, as with any disabled
owner. The supervisor paths take no new lock (`LockRobot` is the existing leaf lock), so they cannot join the lock
orders of [Administration rules](#administration-rules). Area mutations take no explicit lock on `users`; their foreign keys take `KEY SHARE`,
compatible with `FOR NO KEY UPDATE`. Deleting an area takes no lock (it would need `UPDATE` on `areas`, which the
portal role does not hold): a plain `DELETE`, guarded by `ON DELETE RESTRICT`. `RESTRICT` raises 23001
(`restrict_violation`), not the 23503 of `NO ACTION` and of the inserts: a 23001 on `area_members_area_id_fkey` or
`area_supervisors_area_id_fkey` maps to 409 on the delete (`portal.ErrInUse`), and a 23503 on the same constraints to
404 when adding a member or supervisor to an area deleted concurrently (all four foreign keys of the two tables join
`referenceFields`, portal repository).

### `/me`

`GET /api/v1/me` gains `permissions.supervised_areas`: `[{id, key, name}]` sorted by key, like `managed_initiatives`.
The UI shows the supervisor screens iff it is non-empty. Under a token it is always `[]` (rule 8 of [Scope](#scope)).

### Area API

| Endpoint | Purpose |
|---|---|
| `GET, POST /api/v1/areas`; `GET, DELETE /api/v1/areas/{id}` | Areas (platform admins). `POST` takes `{key, name}`; a duplicate key is 409. `DELETE` with members or supervisors is 409. No `PATCH`: the key is immutable and renaming is a later extension, like groups |
| `GET, POST /api/v1/areas/{id}/members`; `DELETE /api/v1/areas/{id}/members/{user_id}` | Members. `POST {email}` resolves exactly one active human (422 otherwise); a person already in an area (this one or another) is 409: moving someone is remove then add; an unknown area (deleted concurrently included) is 404. `GET`: platform admins, all members; supervisors of the area, supervisable members only. Each member: `user_id, email, name, status, added_at` |
| `GET, POST /api/v1/areas/{id}/supervisors`; `DELETE /api/v1/areas/{id}/supervisors/{user_id}` | Supervisors (platform admins). `POST {email}` as for members; a duplicate is 409 |
| `GET /api/v1/users/{id}/tokens` | A human's tokens, newest first, keyset `cursor`, recently dead included (platform admins: any human; supervisors: supervisable people of their areas). A robot id is 404, like the rest of `/users/*` |
| `GET /api/v1/users/{id}/robots` | Robots owned by a human, by id, keyset `cursor`, admin document (same access as the line above) |

Widened for supervisors, always from a browser session and within their scope: `GET /api/v1/robots/{id}/tokens`,
`DELETE /api/v1/tokens/{id}`, `PATCH /api/v1/robots/{id}` (`owner_user_id` only); and `GET /api/v1/me`
(`supervised_areas`, `[]` under a token), `GET /api/v1/audit` (each event carries `area_id`). `GET /api/v1/robots` and
`GET /api/v1/robots/{id}` are unchanged: a supervisor reaches robots through their people.

For callers who are neither platform admins nor supervisors in scope, `GET /robots/{id}/tokens`, `GET /areas/{id}/members`
and `PATCH /robots/{id}` with only `owner_user_id` answer 404, because the area scope hides what it refuses. A
`PATCH /robots/{id}` by a non-admin that carries any other field, or nothing, is 403 before anything is looked up.
`GET /areas/{id}/supervisors` stays admin-only (403).

Reusing these endpoints keeps one revocation and one robot patch, so the token and robot rules (lock, no-op patch
without audit, 204 on an already revoked token) hold for supervisors without a second implementation. The two new
`/users/{id}/*` lists close a gap for platform admins too: they could revoke any token but had no way to list a
human's tokens.



## Content-Security-Policy and the web UI

The web UI pages and components must fit this policy:

- The portal sends `default-src 'none'; style-src 'self'` (no `style-src-elem` / `style-src-attr` / `'unsafe-inline'`;
  `platform/api/internal/webui/csp.go`), so both `<style>` elements and `style=` attributes in markup are blocked, while
  CSSOM writes (`el.style.x = ...`, `setProperty`) are allowed.
- Reka UI (reka-ui 2.11.0, pinned) is compatible: the web app imports only `DialogRoot`, `DialogPortal`, `DialogOverlay`,
  `DialogContent`, `DialogTitle`, `DialogDescription`, `DialogClose` (`UiDialog`) and `AlertDialogRoot`,
  `AlertDialogPortal`, `AlertDialogOverlay`, `AlertDialogContent`, `AlertDialogTitle`, `AlertDialogDescription`,
  `AlertDialogCancel` (`UiConfirmDialog`). Their source and transitive imports (Primitive, Teleport, Presence,
  DismissableLayer, FocusScope, `useBodyScrollLock`, `bodyPointerEvents`, `aria-hidden`) never create a `<style>`
  element or set a `style` attribute; they only use CSSOM (`body.style.overflow` / `paddingRight` / `marginRight` /
  `pointerEvents`, `--scrollbar-width` on `<html>`, `animationFillMode` in Presence) and Vue object `:style` bindings
  (`pointer-events`), all allowed.
- The only `<style>` creators in the library are Select, Combobox, ScrollArea and Splitter (not used; eslint
  `no-restricted-imports` bans them). The generated bundle has one `createElement('style')`, Vue's
  `defineCustomElement` path, which the app does not use.
- Rule to keep: never bind `:style` to a string (Vue writes `style.cssText`, which this policy blocks); object bindings
  are fine.
- Coverage: `platform-gitops/overlays/local/browser-check/check.mjs` listens to `securitypolicyviolation` on every page
  and asserts 0 events; it opens `UiDialog` (create initiative) and `UiConfirmDialog` (deactivate, revoke grant, remove
  host) and checks the Reka scroll lock. A Dialog opened from the groups, users, roles, robots and tokens screens (same
  components, same code path) is not exercised there.
