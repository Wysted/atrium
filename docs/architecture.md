# Architecture

atrium is one entrance to a company's internal apps: Google Workspace sign-in, per-app authorization kept in PostgreSQL, and an identity-aware proxy in front of every app through Traefik ForwardAuth. Every request that passes reaches the app with a short-lived signed JWT whose audience is that app. This page explains how the pieces fit and why you can trust the boundaries; exact behavior lives in the [reference](README.md). The individual design decisions are in [decisions/](decisions/).

## Components and processes

| Component | Role |
|---|---|
| Traefik | Entrypoint `websecure`. Runs `strip-assertion` and then ForwardAuth on every router, with no opt-out |
| `platform-api` | Authorizes every request (`/forwardauth`), runs the Google login and sessions, owns the signing keys and publishes the JWKS. Holds the KEK. Four listeners (internal, jwks, public, metrics): [Listeners](reference/forwardauth.md#listeners) |
| `portal` (`cmd/portal`) | The portal app: the home ("which apps can I open") for every user, plus the administration screens and the JSON API behind them. Own Deployment, host and database role; never holds the KEK or any signing key |
| `portalctl` | Go CLI (`platform/api/cmd/portalctl`) that talks to the portal API with a token and only to the portal host. It resolves keys and robot names by listing (the API has no filter by key), treats a 409 on create as `unchanged` only when the existing direct grant is in effect (an expired one is an error: the unique key ignores `expires_at`) and refuses any redirect. Usage: [operate.md](operate.md#portalctl) |
| `sdk/go` | The library apps use to validate the assertion once: [Assertion JWT](reference/assertion.md#app-side-validation) |
| Web UI (`platform/web`) | Nuxt 4 with `ssr: false` + `nuxi generate` (and `experimental.entryImportMap: false`), embedded in the `portal` binary with `embed.FS`, SPA fallback to `200.html`. No Node runtime in production |
| PostgreSQL | Authorization data, sessions, keys, audit: [Data model](reference/data-model.md) |
| Apps | Reachable only from Traefik (NetworkPolicy); each one verifies the assertion |

### Shape of the portal

- One web app, `portal`, on its own host behind the same ForwardAuth. The auth host never serves this UI: an XSS there
  would compromise the login of every app.
- A separate process from platform-api: same Go module, binary `cmd/portal`, its own Deployment, host and database role.
- It is an app like any other: registered in `apps`/`deployments` under the reserved initiative `platform`, and it
  validates the assertion with `sdk/go` (`aud` = its app id). `PLATFORM_PORTAL_APP_ID` tells platform-api which app
  gets the "any active user" rule ([decision table](reference/forwardauth.md#decision-table)); the id is used instead of
  a host because hosts can change and be several. The rule applies only to an app of the reserved initiative `platform`:
  the decision checks it every time (fail closed), and platform-api logs a WARN at start-up if the id points elsewhere.
  The portal process does the same check on its own `PORTAL_APP_ID` (WARN, never fails start-up); `PORTAL_APP_ID`
  (portal) and `PLATFORM_PORTAL_APP_ID` (platform-api) must hold the same id. `access_changed` is emitted by
  `deployments` only on INSERT, DELETE or a change of host or app_id; telemetry columns do not emit it.
- JSON API first (`/api/v1/*`), web UI on top: [Portal API](reference/api.md). The CLI is `portalctl`; it replaced the
  earlier grant and revoke shell scripts.
- Go tooling (build, test, vet, lint) always runs in workspace mode (`platform/go.work`): `cmd/portal` imports
  `sdk/go` without a `require`/`replace`, exactly like `examples/hello`. A build of `platform/api` outside the
  workspace does not resolve the SDK.

Rationale: [ADR 0003](decisions/0003-separate-listeners-per-audience.md), [ADR 0006](decisions/0006-portal-as-a-separate-process-and-database-role.md).

## Request flow

1. A client reaches Traefik on `websecure`. `strip-assertion` removes any client-sent `Platform-Assertion`, then
   ForwardAuth calls `platform-api /forwardauth` (always with GET; the original method arrives in `X-Forwarded-Method`).
2. platform-api resolves the app from `X-Forwarded-Host` against `deployments.host` and decides ([decision table](reference/forwardauth.md#decision-table)):
   allow only if the user is active, the app and its initiative are active and a non-expired grant exists, directly or through a group.
   Anything else is a deny; a platform-api or database failure is a 503, never a 2xx.
3. On allow, platform-api answers 200 with `Platform-Assertion`, an ES256 JWT for that one app (`aud`), valid 90 s, and
   Traefik forwards it to the app. The app validates it with `sdk/go`, using keys from the JWKS ([keys](reference/keys.md)).
4. First visit, with no valid app cookie: a navigation gets a 302 to the login on the auth host
   (`/login?rd=<original url>&bind=<hash>`, plus the short-lived `__Host-Http-platform_bind` cookie); an API call gets a 401
   with `login_url`.
5. The auth host runs the Google OIDC flow ([login](reference/login.md#google-oidc)) and creates the central session
   (`__Host-Http-platform_session`). It then issues a single-use `login_code` bound to the session, the app, the target host
   and the `bind` hash.
6. The browser returns to the app host at `/_platform/callback?code=`. ForwardAuth accepts the code only from the browser
   holding the matching bind cookie, and answers a 302 to the original URL with the app cookie `__Host-Http-platform_app`.
   From then on step 3 applies.
7. Non-browser clients (CLI, automation) send `Authorization: Bearer <token>` to the portal host; the same ForwardAuth
   validates the token and issues an assertion with token claims ([token path](reference/forwardauth.md#token-path)).

## Trust model

| Boundary | What enforces it |
|---|---|
| `/forwardauth` is never reachable from a public route or from app namespaces | Separate internal listener, reachable from Traefik and kubelet via NetworkPolicy ([listeners](reference/forwardauth.md#listeners)) |
| Apps only receive traffic that went through ForwardAuth | NetworkPolicy lets only Traefik reach them; the signed assertion is the defense if that fails. The probe checks the opposite case ([metrics](reference/metrics.md#probe)) |
| A client cannot forge an assertion | Any client-sent `Platform-Assertion` is removed before ForwardAuth runs; the assertion is ES256-signed and bound to one app |
| Signing keys do not leak through the database | Private keys are sealed with a KEK that never reaches the database; only platform-api holds it |
| A compromised process cannot do more than its role | `platform_runtime` (platform-api) and `platform_portal` (portal) hold minimal privileges; both are verified against an expected set at start-up and the process refuses to start on drift ([privileges](reference/data-model.md#platform_runtime-privileges), [roles](reference/data-model.md#database-roles)) |
| Nothing is open by default | No deployment or no grant means deny; permissions are resolved from the database on every portal call, never carried in the assertion |
| Failures close | platform-api or database failure is a 503; a replica that cannot load the active key stops signing; a KEK mismatch fails readiness |

### Non-browser clients

CLI and automation call the portal API with tokens. The product is multi-company, so the design assumes no CI system,
secret manager or stack on the adopter's side. Rationale: [ADR 0008](decisions/0008-opaque-api-tokens-and-robots.md).

| Phase | Mechanism | Status |
|---|---|---|
| 1 | Personal access tokens (humans) and robot tokens, one mechanism | Implemented (personal tokens, robot tokens, `portalctl`) |
| 2 | Workload identity: OIDC issuers configurable per adopter (Kubernetes service accounts, GitHub or GitLab CI), mapped to robots by a trust policy on combined claims (issuer + audience + subject + repository/namespace); no long-lived secret | Direction only; reuses robots, scopes and the token path of phase 1 |
| 3 | OAuth device flow, only if personal tokens prove annoying. Device-code phishing campaigns (Storm-2372 style) target admins, so it would need a verification page that shows the client and requested scope, short code lifetime and no `admin` scope without a browser session | Not planned |

Phases 2 and 3 are tracked in [TODO.md](../TODO.md). Google ID tokens were discarded
(`gcloud auth print-identity-token --audiences` does not work with user accounts, and Google identity would bypass the
platform's revocation and audit).

Threat model of API tokens and robots:

| Threat | Mitigation | Residual risk |
|---|---|---|
| Token leaked in logs, CI output or shell history | Prefix for secret scanners; expiry (cap 30 days personal, 90 robot); `last_used_at` and revocation; Traefik strips `Authorization` before the portal, platform-api never logs it; query strings refused | A bearer secret works for anyone holding it until revoked or expired; no sender constraint (DPoP, mTLS). Traefik or client-side logs outside the platform are the adopter's |
| Database dump | Only SHA-256 of 256-bit secrets is stored; hashes are not readable by `platform_portal` | None for the secret; a dump still exposes token metadata (names, owners) |
| Token replayed to the portal by a sibling app that received it by mistake | Tokens are not accepted on any other host; apps cannot read the portal route's headers (Traefik strips them); clients talk only to the portal host | A malicious sibling that obtains the secret can replay it to the portal until expiry or revocation: the token has no audience binding beyond the host rule |
| Token sent to other app hosts | Ignored by platform-api there; the secret is exposed to that app, which could replay it to the portal | Until expiry or revocation. The CLI refuses any host but the portal's; a stray `Authorization` elsewhere is a client bug |
| Privilege escalation at issuance | Browser session only; owners only for themselves; `admin` only if the owner is admin or manager at that moment; robot tokens only by platform admins; scope never adds permissions | None beyond the owner's own rights at issuance |
| Privilege drift after issuance (grant revoked, owner disabled) | Permissions resolved live per request from the owner; disabling revokes tokens; ForwardAuth checks the owner's `status` per request | The token row has no cache, but the app and initiative rules may come from the 30 s decision cache if the Evaluator is reused |
| Offboarding gap: user suspended in Google but not disabled in the portal | Personal cap of 30 days; offboarding disables the user in the portal, which revokes sessions and tokens at once ([login limitations](reference/login.md#limitations)) | A suspended, not disabled user keeps a working personal token up to 30 days; closing it needs the Directory API check ([login limitations](reference/login.md#limitations)) |
| `Authorization` of every app reaches platform-api | Added to `authRequestHeaders`; read only for the portal app, never logged; internal listener reachable only from Traefik (NetworkPolicy), same exposure as `Cookie` today | A platform-api compromise sees app `Authorization` headers, as it already sees cookies |
| Robot used as backdoor admin | Robots never platform admins (schema pin), never in groups, never effective managers; only platform admins create them; creation and grants audited | A platform admin can still grant a robot `manages_access` in an initiative together with a human manager; reviewed through audit and the robot list |
| Leaked token mints new tokens | Issuance only from a browser session (assertion without `tid`) | Containment only: a leaked `admin` token of an admin can still change grants, disable users and revoke tokens until revoked |
| CSRF or confused deputy through the browser | Bearer is not ambient; preflight never approved; Fetch Metadata and JSON-only unchanged; token path ignores cookies | None identified |
| DoS with random Bearer values | Format and checksum check before the database; one indexed lookup otherwise, no heavier than the existing uncached session lookup; Traefik rate limits are the adopter's tool | A flood of well-formed random tokens costs one indexed query each; same class as a flood of random cookies |
| Revocation latency | No cache: the next request after revocation fails on every replica | A request already past ForwardAuth finishes; its assertion lives at most 90 s and is never visible to the client |
| Phishing an admin into creating a token | Creation needs a browser session on the portal host and shows the scope and expiry; audited | A deceived admin can hand a token to an attacker; mitigated only by expiry and review. Device flow (phase 3) would widen this surface |
| Insider creating a robot | Only platform admins; every robot, grant and token creation audited; robots listed with owner and last use | A malicious platform admin is already trusted with grants; the audit is the control |
| Compromised portal process | Cannot read `token_hash` or write `platform_admins`; `platform_runtime` cannot insert tokens or delete them, and cannot create a robot | The portal role can insert a token with a hash it knows for any user, equivalent to what its grant writes already allow; the audit trail is bypassable by that role |

### Area supervisors

Rationale: [ADR 0010](decisions/0010-local-areas-with-admin-assigned-supervisors.md). Threat model:

| Threat | Mitigation | Residual risk |
|---|---|---|
| Supervisor escalates to access | No action on grants, roles, groups or apps; scope is credentials metadata, revocation and `owner_user_id` | None for access |
| Supervisor administers a peer, an admin or themselves | Supervisors and platform admins are never supervisable (one SQL predicate on the target, evaluated per request, tested) | None identified |
| Privilege by side effect (someone added to an access group falls under a supervisor) | Areas are their own tables, managed only by platform admins | A platform admin's mistake when adding a member; audited |
| Supervisor disrupts automation (revokes a deploy robot's tokens) | Only robots owned by their people; audited with `area_id` | Intended capability: an admin who sets a shared robot's owner hands it to that owner's supervisors |
| Robot moved out of reach | Reassignment only to supervisable people of the actor's areas; clearing the owner is refused | A supervisor of two areas can move a robot between them, so the other supervisors of the target area gain it |
| Leaked token of a supervisor | The supervisor capacity is browser-session only: under a token `Supervised` is empty (one place, `portal.subject`), and supervising never allows an `admin` token | None beyond what the token's owner can do without the capacity |
| Removed supervisor finishes an in-flight action | Next request re-reads the scope; no cache | One request already in its transaction commits; never extends access; audited |

## Use cases

- **Open an app.** `GET /api/v1/me/apps` lists the apps the caller can open with the exact rules of the ForwardAuth
  decision: a deployment appears in the home if and only if `/forwardauth` would answer 200 for that user (the portal app
  itself is never listed).
- **Give access.** A platform admin or an initiative manager grants a role in an initiative to a user (by email), a group
  or a robot ([who administers what](reference/api.md#who-administers-what)). Access binds to `google_sub`, so a user logs
  in once, or receives access through a group, before direct grants.
- **Automate.** A platform admin creates a robot and issues it a token; the robot is granted roles directly, never through
  groups. A human uses a personal token with the CLI. Tokens expire (30 days personal, 90 days robot by default) and are
  revoked on disable ([Tokens](reference/api.md#tokens)).
- **Contain a leaked token.** The owner revokes it by id; a platform admin revokes by id or by secret. A token may
  revoke tokens (its owner's own, or any if the owner is a platform admin and the scope is `admin`), so containment does
  not require a browser.
- **Offboard a person.** See [Offboarding, end to end](#offboarding-end-to-end).

### Offboarding, end to end

1. A person leaves. Their supervisor revokes the person's personal tokens (immediate, no cache) and reassigns each of
   their robots to another member, or revokes the robot's tokens when nobody takes it over (a robot authenticates
   only with tokens, so that stops it; disabling stays with platform admins).
2. A platform admin disables the user when HR or IT says so (sessions and remaining tokens revoked, rule 5 of
   [Token rules](reference/api.md#token-rules)). Disabling does not remove the person from their area: they stay supervisable, so robots still owned by
   them remain in their supervisor's scope. A platform admin removes the person from the area once nothing is left.
3. Session cutoff is not the supervisor's: a browser session ends with the disable or the 8 h cap ([login](reference/login.md#rules)).
4. Offboarding a supervisor is a platform admin task. Disabled, they are an inactive subject and can do nothing; their
   `area_supervisors` rows stay until a platform admin removes them, and while they stay the person remains
   non-supervisable (rule 1), so their own tokens and robots are reachable by platform admins only. An area without an active supervisor falls back to platform admins; there
   is no "last supervisor" rule, because platform admins always reach every person.
5. No notification: the platform has no notification channel. The previous and the new owner learn of a reassignment
   from the audit (platform admins) and the robot list; nothing is sent.

## Glossary

| Term | Meaning |
|---|---|
| Initiative | The unit that groups apps; roles, grants and managers live inside it. Has an immutable `key` and a `status` (active/inactive). The reserved initiative `platform` holds the portal app and cannot be deactivated |
| Role | A named role that only exists inside an initiative (`initiative_roles`). Its key is part of the app's contract: the keys of the roles a user holds on the app's initiative are delivered in the `roles` claim. The `manages_access` flag makes its holders managers of the initiative |
| Grant | A row of `initiative_access`: an initiative, a role and exactly one subject (a user, a group or a robot), with an optional `expires_at`. Robots only get direct grants |
| Group | An internal group of humans, global across initiatives, usable as the subject of a grant. Google Workspace group sync is a later extension |
| Area | A container defined in the portal that holds people (one area per person) for the supervisors of their credentials. It grants nothing by itself and is not a group |
| Area supervisor | A human who administers the robots and API tokens of the people of an area (never access), from a browser session only |
| Platform admin | A human in `platform_admins`, separate from initiative roles, with platform-wide administration. Created by CLI or SQL only |
| Manager (initiative manager) | A user who holds a role with `manages_access` in an initiative; administers the grants of that initiative |
| Robot | A non-human identity: a row in `users` with `kind = 'robot'`, no email, no Google login; created by platform admins and authenticated only with tokens |
| Personal token / robot token | An opaque `ptk_` API access token with scope `read` or `admin` and a mandatory expiration, owned by a human or a robot respectively |
| App | A service behind ForwardAuth, with an opaque immutable id (`app_<ulid>`) that is the assertion `aud`; belongs to one initiative |
| Deployment / host | A deployment maps one lowercase host to an app; an app may have several. ForwardAuth resolves the app only from the host |
| Audience (`aud`) | The app id the assertion is issued for; an app accepts only its own |
| Assertion | The ES256 JWT in `Platform-Assertion` that states who the user is for one app: [Assertion JWT](reference/assertion.md) |
| KEK | The key-encryption key, mounted from a Kubernetes Secret, that seals the private signing keys; never stored in the database |
| JWKS | The public signing keys, served at `/.well-known/jwks.json` on a dedicated listener, used by apps to verify assertions |
| Portal app | The app named by `PLATFORM_PORTAL_APP_ID` inside the `platform` initiative: any active user may open it, and it hosts the home, administration and API |
