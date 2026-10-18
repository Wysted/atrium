# Spike forwardauth-cookie (histórico)

> English version: [spikes/forwardauth-cookie/README.md](../../../../../spikes/forwardauth-cookie/README.md)

> Histórico. Este spike validó el flujo de ForwardAuth antes de que se construyera la plataforma; no está desplegado y
> los nombres de sus cookies y headers son anteriores al producto. El comportamiento actual está especificado en
> [docs/reference/forwardauth.md](../../../reference/forwardauth.md) y [docs/reference/assertion.md](../../../reference/assertion.md).

Valida el flujo IAP: Traefik ForwardAuth -> `authz`, sesión central en el host de autenticación,
cookie `__Host-portal_app` por app plantada mediante un código de un solo uso en un 302 de la respuesta de ForwardAuth.

## Flujo

1. `GET https://app1.apps.wysted.dev/x` sin cookie -> Traefik llama a `authz /forwardauth`
   -> 302 `https://auth.apps.wysted.dev/login?rd=<original url>` (XHR, `/api/`, accept JSON -> 401 JSON; preflight -> 200).
2. `/login?user=alice` (falso) establece `__Host-portal_session` en el host de autenticación, crea un código de un solo uso (30s, vinculado a usuario + host de la app + rd)
   y responde 302 a `https://app1.../_portal/callback?code=...`.
3. Ese request pasa por ForwardAuth otra vez; `authz` consume el código y responde 302 a `rd` con
   `Set-Cookie: __Host-portal_app=...; Path=/; Secure; HttpOnly; SameSite=Lax` (sin Domain).
4. Con la cookie, `authz` responde 200 + `X-Portal-Token` (ES256, aud = id de la app, sub = UUID del usuario, TTL 90s);
   usuario no autorizado -> 403. El binario `app` valida el token contra `/.well-known/jwks.json`.

Usuarios: alice -> app1; bob -> app1, app2. La llave de firma es efímera (nueva en cada arranque).

## Ejecutar los tests

    cd spikes/forwardauth-cookie && ../../gk go test ./...

Env: authz `LISTEN_ADDR`, `AUTH_HOST`; app `APP_ID`, `AUTHZ_JWKS_URL`, `LISTEN_ADDR`.
Imagen: `docker build --build-arg BINARY=authz|app .`

## Por verificar en el cluster (no cubierto por los unit tests)

- El `Set-Cookie` + `Location` del 302 devuelto por `authz` llegan al navegador a través de un ForwardAuth real de Traefik.
- `authResponseHeaders: X-Portal-Token` sobrescribe cualquier `X-Portal-Token` enviado por el cliente.
- El middleware asociado a nivel de entrypoint cubre toda IngressRoute, incluido el host de autenticación: sus requests (`/login`, `/.well-known/jwks.json`) llegan a `/forwardauth` y obtienen 200 sin `X-Portal-Token` (el host de autenticación nunca es una app; `New` rechaza configuraciones que lo listen en `Apps`). Verificar que `/login` y el JWKS sigan siendo alcanzables y que ahí no se inyecte ningún header de token.
- Traefik reenvía `Cookie`, `Accept`, `Sec-Fetch-Mode`, `Origin`, `Access-Control-Request-Method` a authz. Verificado (Traefik v3.7.14): `/forwardauth` siempre se llama con GET; el método original solo llega en `X-Forwarded-Method`, por lo que authz lee ese header primero.
- Comportamiento de `X-Forwarded-Host` (sin puerto) en puertos no estándar.
