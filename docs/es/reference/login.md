# Login con Google y sesiones

> English version: [docs/reference/login.md](../../reference/login.md)

Cómo inician sesión los usuarios con Google y cómo funcionan las sesiones, las cookies y los códigos de un solo uso. Fundamento: [ADR 0004](../decisions/0004-google-only-login-with-hd-allowlist.md), [ADR 0005](../decisions/0005-central-session-with-per-app-sessions.md).

## Google OIDC

1. Authorization Code + PKCE (S256) + `state` + `nonce`; scopes `openid email profile`; `access_type=online`;
   `prompt=select_account`; `hd` se envía solo como pista de UI: el dominio permitido cuando `PLATFORM_ALLOWED_HD` tiene uno, `*`
   cuando tiene varios (la página de Google fija la dirección al dominio indicado). Un único redirect URI: `https://auth.apps.<domain>/oauth2/callback`.
2. La pantalla de consentimiento OAuth de Google Cloud se configura como **Internal** (Google rechaza las cuentas externas).
3. Validación del ID token:
   - `Verify` de `coreos/go-oidc` v3: firma, `iss`, `aud`, `exp`.
   - Verificaciones explícitas (go-oidc no las hace): `nonce` (tiempo constante), `email_verified == true`,
     `hd` presente y en la lista de dominios permitidos (las cuentas personales no llevan `hd`).
   - La identidad es `sub` (mapeado a `users.google_sub`); el email nunca es un identificador.
4. No se guarda ningún token de Google (ni refresh token, ni access token).
5. `coreos/go-oidc` v3 acepta el emisor de Google con y sin esquema solo cuando el proveedor se crea con
   `https://accounts.google.com` (caso especial en `verify.go`); `SkipIssuerCheck` nunca se usa.

## Configuración y disponibilidad

Variables, formatos y archivos: [configuration.md](configuration.md#platform-api).

- Las tres variables de Google van juntas; sin Google, se requiere `PLATFORM_FAKE_LOGIN=true` (solo desarrollo, se ignora con una advertencia cuando Google está configurado).
- El emisor de Google está fijado a `https://accounts.google.com`; nunca es configurable mediante el entorno.
- El discovery de Google corre en segundo plano con backoff y nunca bloquea el arranque. Hasta que está listo, `/login`
  sin sesión y `/oauth2/callback` responden 503 con `Retry-After`. `/readyz` y `/forwardauth` no
  dependen de Google: los usuarios con sesiones vivas siguen funcionando durante una caída de Google.
- El flujo se consume solo cuando el `state` del callback coincide con él (`UPDATE ... WHERE id AND state`), de modo que un callback
  falsificado con un `state` ajeno, o `?error=` sin el `state` correcto, nunca cancela un login en curso.
  La cookie de login se borra solo cuando el flujo realmente se quemó.
- Resultados del callback, siempre con páginas genéricas que nunca reflejan parámetros de la solicitud:
  - 400: ningún flujo coincidente, código faltante, Google rechazó el código (`invalid_grant` y otros 4xx), ID token inválido,
    `?error=` con el `state` correcto.
  - 403: `hd` ausente o no permitido, `email_verified` no verdadero, `sub` vacío o con el prefijo reservado `fake:`,
    usuario deshabilitado.
  - 503 con `Retry-After`: Google inalcanzable, 5xx o 429 del endpoint de tokens, redirecciones desde Google
    (nunca se siguen, de modo que el secreto de cliente nunca se reenvía), plazo excedido, o llaves de Google no disponibles.
- Dos pestañas: un segundo `/login` reemplaza la cookie de login, de modo que el último flujo se completa y la pestaña anterior recibe un 400
  sin afectarlo.

Rotación de `client_secret`: Google permite dos secretos activos por cliente OAuth; no hay automatización, solo un runbook manual
(agregar un secreto, desplegarlo, deshabilitar el anterior, eliminarlo): [Rotación del secreto de cliente de Google](../runbooks/google-client-secret-rotation.md).

## Tablas de sesión

| Tabla | Cookie | Vida |
|---|---|---|
| `oauth_flows` | `__Host-Http-platform_login` (host de autenticación) | 10 min; guarda `state`, `nonce`, verificador PKCE, `rd`, hash de `bind`; se consume atómicamente |
| `sessions` | `__Host-Http-platform_session` (host de autenticación) | 8 h absolutas + 30-60 min de inactividad; `last_seen_at` se actualiza con throttling, refrescado por cualquier solicitud a una app |
| `app_sessions` | `__Host-Http-platform_app` (cada host de app) | Nunca más allá de la sesión central; FK a `sessions` con `ON DELETE CASCADE` |
| `login_codes` | ninguna (URL del callback) | 30 s, un solo uso; ligado a la sesión, la app, el host destino y el hash de `bind` |

## Reglas

1. Los valores de las cookies son aleatorios de 256 bits; solo se guarda su SHA-256. Todas las cookies: `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`,
   sin `Domain` (`Strict` rompería el retorno desde Google y los enlaces desde el correo o el chat).
2. Se emite un nuevo id de sesión en cada login (sin session fixation).
3. El uso único es atómico: `UPDATE ... SET consumed_at = now() WHERE consumed_at IS NULL RETURNING ...`.
4. CSRF de login / intercambio de sesión: un `login_code` lo acepta solo el navegador que tiene la cookie
   `__Host-Http-platform_bind` correspondiente establecida en el host de la app cuando comenzó el login.
5. `rd` debe apuntar a un host registrado en `deployments` (solo https); cualquier otra cosa se rechaza.
6. La revocación es borrado: usuario (todas las `sessions`, en cascada a `app_sessions`), app (sus `app_sessions`).
   Un usuario deshabilitado pierde el acceso dentro de la caché de decisiones de 30 s; esta es la defensa contra cuentas
   suspendidas de Workspace (Google no envía eventos RISC para usuarios de Workspace).
7. Logout: `POST /_platform/logout` con una verificación de `Origin`/`Sec-Fetch-Site` revoca la sesión central (y sus
   sesiones de app) en la base de datos, luego muestra una página de "signed out" en el host de autenticación en lugar de redirigir al login
   (Google mantiene su propia sesión y volvería a iniciar la del usuario en silencio).
8. Rate limits en `/login` y `/oauth2/callback`; los flujos, códigos y sesiones vencidos los purga un job.
9. Auditoría: logins, logouts, denials; los usuarios pueden listar y cerrar sus sesiones activas.
10. Los cambios de IP / User-Agent se registran y alertan, nunca se usan para cortar la sesión (NAT y redes móviles).

## Cookies

Todas las cookies de la plataforma usan el prefijo `__Host-Http-`. Nombres: `__Host-Http-platform_session`, `__Host-Http-platform_app`,
`__Host-Http-platform_login`, `__Host-Http-platform_bind`. El servidor siempre establece `Secure`, `HttpOnly`, `Path=/` y no
`Domain`. Soporte para la parte `Http`: Chrome 140+, Firefox 143+. Los navegadores sin él igualmente aplican las reglas de `__Host-` porque el nombre comienza con
`__Host-`, de modo que la cookie nunca es más débil.

## Dependencias fijadas

golang-jwt/jwt v5.3.1, coreos/go-oidc v3.21.0, golang.org/x/oauth2 v0.37.0, jackc/pgx v5.11.0.

## Limitaciones

- `hd` para usuarios de un dominio secundario no está documentado por Google: la lista de permitidos es una lista configurable de valores `hd`,
  los rechazos registran `hd` y email, y debe probarse con un usuario real de dominio secundario antes de producción. Los alias de
  email y los dominios alias de usuario nunca llegan al ID token: Google inicia sesión solo con la dirección principal.
- Usuarios suspendidos de Workspace: el corte es el flag local `disabled` más el tope de sesión de 8 h. La baja (offboarding) debe deshabilitar
  al usuario en el portal: eso revoca sus sesiones y tokens de API ([tokens de API](api.md#tokens)). Un usuario suspendido en
  Google pero no deshabilitado en el portal conserva tokens funcionales hasta el tope personal (30 días por defecto). Un corte más rápido
  requeriría la Directory API del Admin SDK.
