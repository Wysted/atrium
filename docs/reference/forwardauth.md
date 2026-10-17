# ForwardAuth

The contract between Traefik and platform-api: how every request is authorized and what the app receives. Why it is built this way: [ADR 0001](../decisions/0001-per-app-assertion-with-opaque-ids.md), [ADR 0003](../decisions/0003-separate-listeners-per-audience.md).

## Traefik configuration

Attached to the `websecure` entrypoint (applies to every router, no opt-out), in this order:

1. `strip-assertion` (`headers` middleware): removes any client-sent `Platform-Assertion`.
2. `forwardauth`: calls `platform-api /forwardauth` with
   - `authResponseHeaders: [Platform-Assertion]`
   - `authRequestHeaders` as an allowlist: `Cookie`, `Accept`, `Sec-Fetch-Mode`, `Sec-Fetch-Site`, `Origin`,
     `Access-Control-Request-Method`, `Access-Control-Request-Headers`, `X-Requested-With`, `User-Agent`,
     `Authorization` (`Sec-Fetch-Site` and `Origin` are needed for the logout CSRF check; `Authorization` is read only for
     portal hosts and reaches platform-api for every host, see [Token path](#token-path))
   - bounded `maxResponseBodySize`
   - client `X-Forwarded-*` never trusted (entrypoint `forwardedHeaders` without `trustedIPs`)

Traefik always calls `/forwardauth` with GET; the original method arrives only in `X-Forwarded-Method`.

### Traefik behavior this contract relies on

Traefik v3.7.14:

1. With `authRequestHeaders` as an allowlist, `X-Forwarded-Method/Host/Uri/Proto/For/Port` still reach platform-api
   (Traefik provides them); headers outside the list (custom ones) do not. A client-sent `X-Forwarded-Host` is replaced
   by the real host.
2. `strip-assertion` (`customRequestHeaders` with an empty value) removes a client-sent assertion on the auth host and
   on preflight requests; authorized requests still receive the assertion issued by platform-api.
3. `maxResponseBodySize` is in bytes; a larger auth response is rejected with 401.
4. `trustForwardHeader` is deprecated in v3.7 (removed in the next major); it stays explicitly `false` and is revisited
   on the Traefik major upgrade.
5. `Authorization` in `authRequestHeaders` reaches platform-api and nothing else changes for apps: an app behind
   ForwardAuth still receives the `Authorization` it was sent, behind a valid session; without one the request is a 401
   as before.
6. On 401/403 from ForwardAuth, Traefik copies the auth response's `WWW-Authenticate` to the client
   (`Bearer error="invalid_token"`, bare `Bearer`, `insufficient_scope`), with no redirect and no `Set-Cookie`.

## Listeners

platform-api serves four listeners so that `/forwardauth` is never reachable through a public route or from app namespaces:

| Listener | Routes | Reachable from |
|---|---|---|
| internal | `/forwardauth`, `/healthz`, `/readyz` (answers 404 for the JWKS) | Traefik (ForwardAuth) and kubelet (probes) via NetworkPolicy; app namespaces cannot reach it |
| jwks | `GET /.well-known/jwks.json` only (`PLATFORM_JWKS_ADDR`, required; HTTPS-only with TLS 1.3 when `PLATFORM_JWKS_TLS_CERT_FILE` and `PLATFORM_JWKS_TLS_KEY_FILE` are set, plain HTTP otherwise) | app namespaces and the portal namespace via NetworkPolicy |
| public | auth host browser routes: `/login`, `/oauth2/callback`, `/logout`, signed-out page, `/.well-known/jwks.json` | Traefik IngressRoute for the auth host only |
| metrics | `/metrics` only (`PLATFORM_METRICS_ADDR`, required) | the monitoring namespace only, via NetworkPolicy |

A NetworkPolicy filters by port, never by path, so `/metrics` and the JWKS each have a listener of their own: neither can share one that Traefik or the app namespaces reach, and the app namespaces reach a port that serves nothing but the JWKS. The four addresses (`PLATFORM_INTERNAL_ADDR`, `PLATFORM_PUBLIC_ADDR`, `PLATFORM_METRICS_ADDR`, `PLATFORM_JWKS_ADDR`) are required and must use different ports (a wildcard host conflicts with any host on the same port).

`/readyz` fails when the database is unreachable, when no signer is loaded, or on `KEKMismatch`.

## Inputs read by platform-api

`X-Forwarded-Host`, `X-Forwarded-Method`, `X-Forwarded-Uri`, `Cookie`, `Accept`, `Sec-Fetch-Mode`,
`Origin`, `Access-Control-Request-Method`, and `Authorization` only when the host is a portal host (never logged). Ignored: `X-Forwarded-Prefix`, `X-Forwarded-Port`, `X-Forwarded-Proto`.
The app is resolved only from `X-Forwarded-Host` (exact, lowercase) against `deployments.host`.

## Decision table

| Case | Response |
|---|---|
| Host is the auth host | 200, no assertion |
| Host not in deployments | 403 (an `unknown_host` row goes to `access_denials`, see [Request classification and edge rules](#request-classification-and-edge-rules)) |
| CORS preflight (`OPTIONS` + `Origin` + `Access-Control-Request-Method`) | 200, no assertion (CORS is the app's job) |
| Portal host with an `Authorization` header (any value; the preflight row above wins) | Token path only, cookie ignored (a valid cookie with an invalid token is 401), never a redirect: valid `Bearer` token and active owner -> 200 + `Platform-Assertion` (empty `roles`, token claims); malformed, unknown, expired or revoked token, inactive owner -> 401 problem+json with `WWW-Authenticate: Bearer error="invalid_token"`; method not allowed by the token scope -> 403. Any other host ignores `Authorization` |
| `/_platform/callback?code=` with a valid one-time code bound to this host and to the browser's `__Host-Http-platform_bind` cookie | 302 to the stored `rd` + `Set-Cookie: __Host-Http-platform_app`, `Referrer-Policy: no-referrer` |
| No or invalid app cookie, navigation (`GET`/`HEAD` + HTML signals) | 302 to `https://auth.apps.<domain>/login?rd=<original url>&bind=<hash>` + `Set-Cookie: __Host-Http-platform_bind` (short-lived random value) |
| No or invalid app cookie, API/XHR | 401 |
| Host belongs to the portal app (`PLATFORM_PORTAL_APP_ID`, see [Components and processes](../architecture.md#components-and-processes)) | 200 + `Platform-Assertion` with empty `roles` for any active user, while the app is active and its initiative is active |
| Authenticated, no access to the app (no grant, user disabled, app disabled, initiative inactive) | 403 (HTML for navigation, JSON for API) |
| Authorized | 200 + `Platform-Assertion` |
| platform-api or database failure | 503; fail closed, never 2xx |

## Request classification and edge rules

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
  sends. The portal reads these rows ([orphan hosts](metrics.md#orphan-hosts)).
- Host-to-app resolution and decisions are cached for 30 s and both caches are flushed by `access_changed`.
  A host that is not a valid DNS name (same rules as `deployments.host`) is refused with the same 403 before any
  lookup: no query, no cache slot. Hosts that are valid but have no deployment are cached apart (at most 10 000,
  same TTL, also flushed by `access_changed`), so a flood of random names can never evict a known host.
  Session state is never cached.

## Conventions

- Reserved paths on every app host: `/_platform/*` (`callback`, `logout`). Apps must not use them. `logout` accepts POST only ([login](login.md#rules)).
- JSON errors use RFC 9457 `application/problem+json` with an extra `login_url` member, so app frontends know where to send the user on 401.
- Every `/forwardauth` response carries `Cache-Control: no-store`.
- One decision log line per request: decision, reason, host, app id, `sid`, `jti`. Never tokens or cookies.

## Token path

Lets non-browser clients (CLI, automation) call the portal API with a personal or robot token. Only platform-api decides. Token model and lifecycle: [API](api.md#tokens); claims in the assertion: [assertion](assertion.md#claims-added-for-api-tokens-and-robots). Rationale: [ADR 0008](../decisions/0008-opaque-api-tokens-and-robots.md).

1. Traefik adds `Authorization` to `authRequestHeaders`. This reaches platform-api for **every** host, so platform-api
   reads it only when the resolved app satisfies the same portal rule as the Evaluator (`Facts.PortalApp`: app id equal
   to `PLATFORM_PORTAL_APP_ID` **and** initiative `platform`, not the app id alone) and ignores it, and never logs it,
   on any other host. Like `Cookie`, it reaches only the internal listener, which only Traefik reaches (NetworkPolicy). Apps behind ForwardAuth keep their own `Authorization` (Grafana, for example): ForwardAuth never
   changes the request that reaches the app.
2. On a portal host, any `Authorization` header takes the token path exclusively; the cookie is not consulted and there
   is no fallback to it. Accepted form: one header, scheme `Bearer` (case-insensitive), one token. Tokens are never
   accepted in a query string, a cookie or another header.
3. Order: format check (prefix, length, checksum) -> one query by `token_hash` joined with the owner, no cache:
   `revoked_at IS NULL AND expires_at > now()` and owner `status = 'active'` -> the portal rules of the [decision table](#decision-table) (app and
   initiative active; any active owner; empty `roles`) -> scope: with `read`, a method other than `GET`/`HEAD`
   (`X-Forwarded-Method`) answers 403. The token row and the owner's `status` are read live; if the app and
   initiative rules reuse the Evaluator, they may come from its 30 s decision cache (flushed by `access_changed`).
4. Failures (RFC 6750): a malformed, unknown, expired or revoked token, or an inactive owner (human or robot), answer
   401 `application/problem+json` with `WWW-Authenticate: Bearer error="invalid_token"`, the same body for every cause
   (no oracle; the reason goes to the log). An `Authorization` header with another scheme, or `Bearer` without
   credentials, answers 401 with `WWW-Authenticate: Bearer` and no `error`. Never a redirect, no `Set-Cookie`, no
   `login_url`. Scope violation: 403 with `WWW-Authenticate: Bearer error="insufficient_scope"`. Database failure:
   503 (fail closed).
5. Success: 200 + `Platform-Assertion` ([claims](assertion.md#claims-added-for-api-tokens-and-robots)). `last_used_at` is written throttled (once per minute per token per
   replica, like `deployments.last_seen_at`) and best effort, through a bounded queue: ForwardAuth never waits for it.
6. The decision log line carries decision, reason, host, app id, `tid` and `jti`; never the `Authorization` header or the token.
7. Traefik: on the portal route, a `headers` middleware `strip-authorization` removes `Authorization` after ForwardAuth
   (`customRequestHeaders` with an empty value, as `strip-assertion`), so the secret never reaches the portal process,
   its logs or its panics. The portal never reads `Authorization`. The middleware lives in the `portal` namespace and
   the routes reference it locally by name (`allowCrossNamespace` is false: one in `platform` would break the portal
   route). Invariant: every IngressRoute that matches a portal host references it; the smoke check lists them and
   fails if one lacks it. The access log keeps dropping headers (`fields.headers.defaultMode: drop`); that setting
   protects the secret and must not be relaxed.
8. CORS preflights (`OPTIONS` + `Origin` + `Access-Control-Request-Method`) keep answering 200 without assertion before
   any token handling; the portal never answers CORS.

### Implementation choices

Where the rules above left a choice:

- The portal fact is read from the Evaluator (`Decision.PortalApp`, set whenever the host is known) through the decision
  cache under a nil-UUID subject, only when a request carries `Authorization`; on a host that is not the portal the
  header is not read again and the cookie path runs as before. The app and initiative rules of rule 3 come from
  that same cache (`authorize` for the owner); the token row and the owner's `status` never do.
- The lookup selects the row by hash and the database computes `revoked`, `expired` (against `now()`) and `owner active`
  as flags, instead of filtering them in the `WHERE`: same single indexed query, and the log can tell unknown,
  revoked, expired and inactive-owner apart. The response cannot.
- Several `Authorization` headers, an empty value, another scheme and `Bearer` without credentials all answer
  `WWW-Authenticate: Bearer` (reason `token_no_bearer`); a Bearer value with the wrong format (including two tokens in
  one header) answers `error="invalid_token"` (`token_malformed`), before any query. All 401 share one body.
- App or initiative inactive (or any other deny of the portal rules) answers 403 problem+json without a challenge;
  an `access_denials` row is written for it like for sessions (once per decision-cache miss; `user_id` = the token owner, which may be a robot, and `sid` NULL), so token denials are queryable like session ones (no portal screen lists per-user denials yet; integration health only reads `unknown_host`). The scope check is a token ceiling, not an access rule, and records nothing. A scope that is not
  `read` or `admin` fails closed as `insufficient_scope`.
- Reasons added to the decision log and `platform_forwardauth_decisions_total`: `token_no_bearer`, `token_malformed`,
  `token_unknown`, `token_revoked`, `token_expired`, `token_owner_inactive`, `token_scope` (401 and the scope 403 use
  decision `unauthenticated` and `deny`); the line gains `tid` (empty for sessions).
- `last_used_at` goes through `internal/tokenuse` (a bounded set of at most 10 000 tokens per replica flushed once a
  minute by one writer, with a final flush at shutdown; observations beyond the bound are dropped and counted, see
  the metrics table). The statement locks rows with `SKIP LOCKED`, caps the time at the database clock and never moves
  the column backwards; a token whose row is locked is kept in memory and retried on the next tick (a revoked one
  is simply not written). Both this and `lastseen` are thin users of `internal/touchset`.

### Traefik manifests and invariants

`strip-authorization` is in `platform-gitops/base/portal/ingressroute.yaml` next to the route that references it. The
smoke check lists every IngressRoute that matches a portal host and fails if one lacks the middleware; it also lists
standard `Ingress` objects and fails on any public route without `Host(`, with `HostRegexp`, or an Ingress without a
host rule, because those could match the portal host unnoticed. A request with a Bearer on a navigation is a 401, never
a login redirect.

### Limitations

A rate limit on failed tokens per client IP waits for the real client IP (see [TODO.md](../../TODO.md): in k3d every client appears as `10.42.0.1`).
