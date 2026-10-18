# 0001. Aserción firmada por app con ids opacos

> English version: [docs/decisions/0001-per-app-assertion-with-opaque-ids.md](../../decisions/0001-per-app-assertion-with-opaque-ids.md)

- Estado: Aceptada
- Fecha: 2026-10-14

## Contexto y problema

Las apps detrás de ForwardAuth necesitan saber quién es el usuario sin implementar login, y una declaración de identidad para una app no debe ser utilizable en otra. Los hostnames pueden cambiar y una app puede tener varios, y las direcciones de email y los subjects de Google no son identificadores internos estables.

## Opciones consideradas

- Un JWT firmado cuyo `aud` es un hostname o slug (nunca).
- Un JWT firmado cuyo `aud` es un id de app opaco e inmutable y cuyo `sub` es un UUID interno (elegida).
- `sub` como el email o el `sub` de Google (nunca): el UUID interno es el subject.

## Resultado de la decisión

ForwardAuth inyecta un JWT ES256 `Platform-Assertion`, emitido para una sola app: `aud` es `app_<ulid>`, inyectado en cada app por configuración; `sub` es el UUID interno del usuario. El `typ` es `platform-assertion+jwt` (tipado explícito de RFC 8725 sección 3.11). El token vive 90 s y se vuelve a emitir en cada request, por lo que las apps no mantienen una lista de replay; `jti` es solo para correlación de logs. `sid` es un id de sesión público, nunca el valor de la cookie. `roles` lleva solo las claves de rol de la iniciativa dueña de `aud`. Las apps leen la identidad únicamente de la aserción verificada e ignoran `X-Forwarded-User` y headers similares.

## Consecuencias

- Los hostnames pueden cambiar o multiplicarse sin tocar `aud`; la aserción es una aserción de identidad para una sola app, no un bearer token para reenviar.
- Las apps deben validar la aserción (`sdk/go` lo hace una vez): chequeo explícito de `typ`, llave por `kid` desde una URL JWKS fija, tolerancia de 30 s.
- Cuando las llaves de firma no están disponibles el SDK responde 503, nunca 401: una falla de la plataforma no es un token inválido.
- Referencia: [Aserción JWT](../reference/assertion.md), [ForwardAuth](../reference/forwardauth.md).
