# forwardauth-cookie spike (disposable)

Validates the IAP flow: Traefik ForwardAuth -> `authz`, central session on the auth host,
per-app `__Host-portal_app` cookie planted through a one-time code in a 302 of the ForwardAuth response.

## Flow

1. `GET https://app1.apps.wysted.dev/x` without cookie -> Traefik calls `authz /forwardauth`
   -> 302 `https://auth.apps.wysted.dev/login?rd=<original url>` (XHR, `/api/`, JSON accept -> 401 JSON; preflight -> 200).
2. `/login?user=alice` (fake) sets `__Host-portal_session` on the auth host, creates a one-time code (30s, bound to user + app host + rd)
   and 302s to `https://app1.../_portal/callback?code=...`.
3. That request goes through ForwardAuth again; `authz` consumes the code and answers 302 to `rd` with
   `Set-Cookie: __Host-portal_app=...; Path=/; Secure; HttpOnly; SameSite=Lax` (no Domain).
4. With the cookie, `authz` answers 200 + `X-Portal-Token` (ES256, aud = app id, sub = user UUID, TTL 90s);
   unauthorized user -> 403. The `app` binary validates the token against `/.well-known/jwks.json`.

Users: alice -> app1; bob -> app1, app2. Signing key is ephemeral (new on every start).

## Run tests

    cd spikes/forwardauth-cookie && ../../gk go test ./...

Env: authz `LISTEN_ADDR`, `AUTH_HOST`; app `APP_ID`, `AUTHZ_JWKS_URL`, `LISTEN_ADDR`.
Image: `docker build --build-arg BINARY=authz|app .`

## To verify in the cluster (not covered by unit tests)

- The `Set-Cookie` + `Location` of the 302 returned by `authz` reach the browser through real Traefik ForwardAuth.
- `authResponseHeaders: X-Portal-Token` overwrites any `X-Portal-Token` sent by the client.
- Middleware attached at entrypoint level covers every IngressRoute, including the auth host: its requests (`/login`, `/.well-known/jwks.json`) reach `/forwardauth` and get 200 without `X-Portal-Token` (the auth host is never an app; `New` rejects configs listing it in `Apps`). Check that `/login` and the JWKS stay reachable and that no token header is injected there.
- Traefik forwards `Cookie`, `Accept`, `Sec-Fetch-Mode`, `Origin`, `Access-Control-Request-Method` to authz. Verified (Traefik v3.7.14): `/forwardauth` is always called with GET; the original method only arrives in `X-Forwarded-Method`, which is why authz reads that header first.
- Behaviour of `X-Forwarded-Host` (no port) on non-standard ports.
