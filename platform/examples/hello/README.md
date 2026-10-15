# hello

Minimal app behind the platform. Copy it as a starting point for an initiative.

## How an app integrates the SDK

1. Get the app id (`aud`), the issuer and the JWKS URL from the platform; inject them by environment
   (`HELLO_APP_ID`, `HELLO_ISSUER`, `HELLO_JWKS_URL`; `HELLO_LISTEN_ADDR` is the listen address).
2. Build one `platformauth.Verifier` at startup (`platformauth.NewVerifier`) and wrap your whole handler
   with `verifier.Middleware` (or `MiddlewareWithLogger`).
3. Read the identity in handlers with `platformauth.ClaimsFrom(r.Context())`. Use `Subject` as the user
   identifier; `Email` and `Name` are informational. Never trust `X-Forwarded-User` or similar headers.
4. Keep `/healthz` for probes: the SDK exempts it from validation (kubelet does not go through Traefik).
5. Do not use `/_platform/*`: it is reserved. Sign out with a plain `POST /_platform/logout` form.
6. Never log or forward the assertion; the middleware already strips it from the request it hands to your handlers.

Behavior: invalid or missing assertion gives 401, unavailable signing keys give 503.

## Run

    HELLO_LISTEN_ADDR=:8080 HELLO_APP_ID=app_... HELLO_ISSUER=https://auth.apps.example.com \
    HELLO_JWKS_URL=http://platform-api.platform.svc:8080/.well-known/jwks.json ./hello

Docker build (context is `platform/` because of `go.work`):

    docker build -f platform/examples/hello/Dockerfile -t portal-hello:dev platform
