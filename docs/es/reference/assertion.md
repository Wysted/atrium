# JWT de aserción

> English version: [docs/reference/assertion.md](../../reference/assertion.md)

La aserción de identidad firmada que reciben las apps. Por qué tiene esta forma: [ADR 0001](../decisions/0001-per-app-assertion-with-opaque-ids.md).

Traefik la inyecta en cada solicitud autorizada como el header `Platform-Assertion`
(listado en `authResponseHeaders` de ForwardAuth). Es una aserción de identidad para una app,
no un token bearer para reenviar.

## Header JOSE

| Campo | Valor |
|---|---|
| `alg` | `ES256` (único valor permitido) |
| `kid` | Obligatorio; selecciona la llave en el JWKS |
| `typ` | `platform-assertion+jwt` (tipado explícito de RFC 8725 §3.11) |

## Claims

| Claim | Valor | Notas |
|---|---|---|
| `iss` | `https://auth.apps.<domain>` | Coincidencia exacta |
| `aud` | Id de app opaco e inmutable (`app_<ulid>`) | Nunca un hostname ni un slug; inyectado en cada app por configuración |
| `sub` | UUID interno del usuario (humano o robot) | Nunca el email ni el `sub` de Google |
| `email`, `name` | Datos de perfil | Informativos; nunca se usan como identificadores |
| `iat`, `nbf`, `exp` | `nbf = iat`, `exp = iat + 90s` | Se reemite en cada solicitud |
| `jti` | Único por emisión | Solo para correlación de logs; las apps no mantienen lista de replay |
| `sid` | Id de sesión público | Nunca el valor de la cookie; no puede autenticar. Ausente en aserciones autenticadas por token |
| `sub_type`, `tid`, `scope` | Ver [Claims añadidos para tokens de API y robots](#claims-añadidos-para-tokens-de-api-y-robots) | Opcionales: solo en aserciones emitidas para un token de API |
| `roles` | `["editor", ...]` | Solo llaves de rol de la iniciativa dueña de `aud`; cantidad acotada |


### Claims añadidos para tokens de API y robots

Presentes solo en aserciones emitidas por la [ruta de token de ForwardAuth](forwardauth.md#ruta-de-token); las aserciones de sesión no cambian. Solo el portal las recibe: ninguna otra app puede alcanzarse con un token.

| Claim | Valor |
|---|---|
| `sub` | Id de usuario del dueño, humano o robot |
| `sub_type` | `human` o `robot`. No `act` (objeto actor de RFC 8693, significado distinto) |
| `tid` | Id del token (uuid); ausente significa sesión de navegador |
| `scope` | `read` o `admin` (nombre de RFC 9068; valor único) |
| `sid` | Ausente: no hay sesión |
| `email` | `""` para robots; `name` = nombre del robot |

Un `sid` ausente y un `email` vacío no requieren cambios en el SDK: ambos son strings opcionales simples en `Claims`, y el portal no
lee `sid`. En el emisor de la plataforma, `assertion.Request` lleva los campos del token y `SessionID` es opcional exactamente cuando
`APITokenID` está definido (siempre se requiere uno de los dos). `Claims` tiene `SubjectType`, `APITokenID` (`TokenID` guarda el
`jti`) y `Scope`, vacíos para aserciones de sesión.

## Validación del lado de la app

Implementada una sola vez en `sdk/go`.

1. Rechazar a menos que `alg == ES256` y `typ == platform-assertion+jwt` (verificado explícitamente: golang-jwt v5 y jwx no validan `typ`).
2. Seleccionar la llave por `kid` desde una URL de JWKS fija; ignorar `jku`/`x5u`. `kid` desconocido: volver a obtener una vez, con rate limit, luego rechazar.
3. Verificar firma, `iss`, `aud == own app id`, `exp`/`nbf`/`iat` con 30 s de tolerancia; `exp` y `aud` son obligatorios.
4. Leer la identidad solo de la aserción verificada; ignorar `X-Forwarded-User` y headers similares.
5. Eximir de la validación la ruta de salud (`/healthz`): los probes de kubelet no pasan por Traefik.
   Una app que sirve probes en un listener interno separado (el portal) define la ruta exenta como una ruta que su
   mux público nunca sirve, de modo que el listener público no tiene ninguna ruta sin autenticación.
6. Nunca registrar la aserción ni reenviarla a otros servicios.

## Comportamiento del SDK ante fallas

- Aserción inválida: 401 (`application/problem+json`).
- Llaves de firma no disponibles (JWKS inalcanzable y sin caché utilizable): 503, nunca 401; es una falla de la plataforma, no un token malo.
- Si una nueva obtención del JWKS falla después de que la caché venció, las últimas llaves se siguen usando hasta por 15 minutos (stale-if-error);
  las fallas se memorizan solo por un backoff corto, y una nueva obtención nunca queda atada a la solicitud que la disparó.
- Las redirecciones desde el endpoint del JWKS nunca se siguen. El JWKS debería viajar por HTTPS (`PLATFORM_JWKS_TLS_*`,
  [llaves](keys.md#endpoint-jwks)): el SDK confía en las raíces del sistema, o en una CA privada mediante `Config.RootCAs` (`platformauth.LoadRootCAs`).
  `http` plano sigue aceptándose por compatibilidad hacia atrás, con una advertencia al arrancar; su integridad depende entonces
  solo de la NetworkPolicy (las apps solo pueden hablar con platform-api).

## Garantías del lado de la plataforma

1. A cada solicitud se le elimina cualquier `Platform-Assertion` enviado por el cliente antes de que corra ForwardAuth
   (Traefik solo lo sobrescribe cuando platform-api lo devuelve, y algunas respuestas 2xx no llevan ninguno).
2. Las apps son alcanzables solo desde Traefik (NetworkPolicy); la aserción firmada es la defensa si eso falla.

Ver también: [ForwardAuth](forwardauth.md) (cómo se produce el header), [llaves](keys.md) (llaves de firma y JWKS).
