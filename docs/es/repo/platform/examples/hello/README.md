# hello

> English version: [platform/examples/hello/README.md](../../../../../../platform/examples/hello/README.md)

App mínima detrás de la plataforma: valida el `Platform-Assertion` con el SDK de Go y muestra la identidad de quien llama. Cópiala como punto de partida. Para poner una app detrás de atrium de punta a punta, incluida la integración del SDK, ver [docs/onboard-an-app.md](../../../../onboard-an-app.md).

## Ejecutar

    HELLO_LISTEN_ADDR=:8080 HELLO_APP_ID=app_... HELLO_ISSUER=https://auth.apps.example.com \
    HELLO_JWKS_URL=http://platform-api-jwks.platform.svc.cluster.local:8083/.well-known/jwks.json ./hello

`HELLO_JWKS_CA_FILE` es opcional (una CA privada para un JWKS HTTPS). Todas las variables: [docs/reference/configuration.md](../../../../reference/configuration.md).

## Compilar

Build con Docker (el contexto es `platform/` por `go.work`):

    docker build -f platform/examples/hello/Dockerfile -t portal-hello:dev platform
