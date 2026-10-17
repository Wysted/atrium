# 0001. Per-app signed assertion with opaque ids

- Status: Accepted
- Date: 2026-10-14

## Context and Problem

Apps behind ForwardAuth need to know who the user is without implementing login, and an identity statement for one app must not be usable at another. Hostnames can change and an app can have several, and email addresses and Google subjects are not stable internal identifiers.

## Considered Options

- A signed JWT whose `aud` is a hostname or slug (never).
- A signed JWT whose `aud` is an opaque, immutable app id and whose `sub` is an internal UUID (chosen).
- `sub` as the email or the Google `sub` (never): the internal UUID is the subject.

## Decision Outcome

ForwardAuth injects a `Platform-Assertion` ES256 JWT, issued for one app: `aud` is `app_<ulid>`, injected into each app by config; `sub` is the internal user UUID. The `typ` is `platform-assertion+jwt` (RFC 8725 section 3.11 explicit typing). The token lives 90 s and is re-issued on every request, so apps keep no replay list; `jti` is for log correlation only. `sid` is a public session id, never the cookie value. `roles` carries only the role keys of the initiative that owns `aud`. Apps read identity only from the verified assertion and ignore `X-Forwarded-User` and similar headers.

## Consequences

- Hostnames can change or multiply without touching `aud`; the assertion is an identity assertion for one app, not a bearer token to forward.
- Apps must validate the assertion (`sdk/go` does it once): explicit `typ` check, key by `kid` from a fixed JWKS URL, 30 s leeway.
- When the signing keys are unavailable the SDK answers 503, never 401: a platform failure is not a bad token.
- Reference: [Assertion JWT](../reference/assertion.md), [ForwardAuth](../reference/forwardauth.md).
