# 0007. Fetch Metadata instead of CSRF tokens

- Status: Accepted
- Date: 2026-10-15

## Context and Problem

Every app lives under the same registrable domain, so `hello.apps.<domain>` and `portal.apps.<domain>` are same-site and `SameSite=Lax` cookies travel between them. The portal API needs a CSRF defense that holds against sibling apps.

## Considered Options

- CSRF tokens, as the OWASP cheat sheet still suggests as an extra layer.
- Same-origin enforcement through Fetch Metadata and `http.CrossOriginProtection` (chosen, following Go's design).

## Decision Outcome

For `/api/*` the portal rejects any `Sec-Fetch-Site` other than `same-origin` and `none` before authenticating, applies `http.CrossOriginProtection` to non-safe methods, accepts mutations only as `application/json` (415 otherwise) and never sends `Access-Control-*`. CSRF tokens are not used. No `GET`/`HEAD`/`OPTIONS` changes state, `r.Host` is never rewritten from `X-Forwarded-Host`, and the API responses carry a restrictive CSP.

## Consequences

- A request without `Sec-Fetch-Site` and `Origin` passes (non-browser clients); browsers always send it.
- Without `Sec-Fetch-Site`, `Origin` is compared with `Host` without the scheme, so HSTS matters.
- A future WebSocket must validate `Origin` explicitly.
- Reference: [Request protection](../reference/api.md#request-protection).
