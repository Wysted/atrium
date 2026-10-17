# Assertion JWT

The signed identity assertion that apps receive. Why it is shaped this way: [ADR 0001](../decisions/0001-per-app-assertion-with-opaque-ids.md).

Injected by Traefik into every authorized request as the `Platform-Assertion` header
(listed in ForwardAuth `authResponseHeaders`). It is an identity assertion for one app,
not a bearer token to forward.

## JOSE header

| Field | Value |
|---|---|
| `alg` | `ES256` (only allowed value) |
| `kid` | Required; selects the key in the JWKS |
| `typ` | `platform-assertion+jwt` (RFC 8725 §3.11 explicit typing) |

## Claims

| Claim | Value | Notes |
|---|---|---|
| `iss` | `https://auth.apps.<domain>` | Exact match |
| `aud` | Opaque, immutable app id (`app_<ulid>`) | Never a hostname or slug; injected into each app by config |
| `sub` | Internal user UUID (human or robot) | Never the email or the Google `sub` |
| `email`, `name` | Profile data | Informational; never used as identifiers |
| `iat`, `nbf`, `exp` | `nbf = iat`, `exp = iat + 90s` | Re-issued on every request |
| `jti` | Unique per issuance | Log correlation only; apps keep no replay list |
| `sid` | Public session id | Never the cookie value; cannot authenticate. Absent on token-authenticated assertions |
| `sub_type`, `tid`, `scope` | See [Claims added for API tokens and robots](#claims-added-for-api-tokens-and-robots) | Optional: only on assertions issued for an API token |
| `roles` | `["editor", ...]` | Only role keys of the initiative that owns `aud`; capped count |


### Claims added for API tokens and robots

Present only on assertions issued by the [ForwardAuth token path](forwardauth.md#token-path); session assertions are unchanged. Only the portal receives them: no other app can be reached with a token.

| Claim | Value |
|---|---|
| `sub` | User id of the owner, human or robot |
| `sub_type` | `human` or `robot`. Not `act` (RFC 8693 actor object, different meaning) |
| `tid` | Token id (uuid); absent means browser session |
| `scope` | `read` or `admin` (RFC 9068 name; single value) |
| `sid` | Absent: there is no session |
| `email` | `""` for robots; `name` = robot name |

Absent `sid` and empty `email` need no SDK change: both are plain optional strings in `Claims`, and the portal does not
read `sid`. In the platform issuer, `assertion.Request` carries the token fields and `SessionID` is optional exactly when
`APITokenID` is set (one of the two is always required). `Claims` has `SubjectType`, `APITokenID` (`TokenID` holds the
`jti`) and `Scope`, empty for session assertions.

## App-side validation

Implemented once in `sdk/go`.

1. Reject unless `alg == ES256` and `typ == platform-assertion+jwt` (checked explicitly: golang-jwt v5 and jwx do not validate `typ`).
2. Select the key by `kid` from a fixed JWKS URL; ignore `jku`/`x5u`. Unknown `kid`: refetch once, rate limited, then reject.
3. Verify signature, `iss`, `aud == own app id`, `exp`/`nbf`/`iat` with 30 s leeway; `exp` and `aud` are mandatory.
4. Read identity only from the verified assertion; ignore `X-Forwarded-User` and similar headers.
5. Exempt the health path (`/healthz`) from validation: kubelet probes do not go through Traefik.
   An app that serves probes on a separate internal listener (the portal) sets the exempt path to a route its
   public mux never serves, so the public listener has no unauthenticated path.
6. Never log the assertion and never forward it to other services.

## SDK failure behavior

- Invalid assertion: 401 (`application/problem+json`).
- Signing keys unavailable (JWKS unreachable and no usable cache): 503, never 401; this is a platform failure, not a bad token.
- If a JWKS refetch fails after the cache expired, the last keys keep being used for up to 15 minutes (stale-if-error);
  failures are memoized only for a short backoff, and a refetch is never tied to the request that triggered it.
- Redirects from the JWKS endpoint are never followed. The JWKS should travel over HTTPS (`PLATFORM_JWKS_TLS_*`,
  [keys](keys.md#jwks-endpoint)): the SDK trusts the system roots, or a private CA through `Config.RootCAs` (`platformauth.LoadRootCAs`).
  Plain `http` is still accepted for backwards compatibility, with a start-up warning; its integrity then depends
  only on the NetworkPolicy (apps may only talk to platform-api).

## Platform-side guarantees

1. Every request has any client-sent `Platform-Assertion` removed before ForwardAuth runs
   (Traefik only overwrites it when platform-api returns it, and some 2xx responses carry none).
2. Apps are reachable only from Traefik (NetworkPolicy); the signed assertion is the defense if that fails.

See also: [ForwardAuth](forwardauth.md) (how the header is produced), [keys](keys.md) (signing keys and JWKS).
