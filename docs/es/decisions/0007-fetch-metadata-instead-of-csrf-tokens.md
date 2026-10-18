# 0007. Fetch Metadata en lugar de tokens CSRF

> English version: [docs/decisions/0007-fetch-metadata-instead-of-csrf-tokens.md](../../decisions/0007-fetch-metadata-instead-of-csrf-tokens.md)

- Estado: Aceptada
- Fecha: 2026-10-15

## Contexto y problema

Todas las apps viven bajo el mismo dominio registrable, por lo que `hello.apps.<domain>` y `portal.apps.<domain>` son same-site y las cookies `SameSite=Lax` viajan entre ellas. La API del portal necesita una defensa CSRF que resista a apps hermanas.

## Opciones consideradas

- Tokens CSRF, como la cheat sheet de OWASP aún sugiere como capa adicional.
- Cumplimiento de same-origin mediante Fetch Metadata y `http.CrossOriginProtection` (elegida, siguiendo el diseño de Go).

## Resultado de la decisión

Para `/api/*` el portal rechaza cualquier `Sec-Fetch-Site` distinto de `same-origin` y `none` antes de autenticar, aplica `http.CrossOriginProtection` a los métodos no seguros, acepta mutaciones solo como `application/json` (415 en caso contrario) y nunca envía `Access-Control-*`. No se usan tokens CSRF. Ningún `GET`/`HEAD`/`OPTIONS` cambia estado, `r.Host` nunca se reescribe desde `X-Forwarded-Host`, y las respuestas de la API llevan una CSP restrictiva.

## Consecuencias

- Una solicitud sin `Sec-Fetch-Site` ni `Origin` pasa (clientes que no son navegadores); los navegadores siempre lo envían.
- Sin `Sec-Fetch-Site`, `Origin` se compara con `Host` sin el esquema, por lo que HSTS importa.
- Un futuro WebSocket debe validar `Origin` explícitamente.
- Referencia: [Protección de solicitudes](../reference/api.md#protección-de-las-solicitudes).
