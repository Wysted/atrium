# hello

Minimal app behind the platform: it validates the `Platform-Assertion` with the Go SDK and shows the caller's identity. Copy it as a starting point. To put an app behind atrium end to end, including the SDK integration, see [docs/onboard-an-app.md](../../../docs/onboard-an-app.md).

## Run

    HELLO_LISTEN_ADDR=:8080 HELLO_APP_ID=app_... HELLO_ISSUER=https://auth.apps.example.com \
    HELLO_JWKS_URL=http://platform-api-jwks.platform.svc.cluster.local:8083/.well-known/jwks.json ./hello

`HELLO_JWKS_CA_FILE` is optional (a private CA for an HTTPS JWKS). All variables: [docs/reference/configuration.md](../../../docs/reference/configuration.md).

## Build

Docker build (context is `platform/` because of `go.work`):

    docker build -f platform/examples/hello/Dockerfile -t portal-hello:dev platform
